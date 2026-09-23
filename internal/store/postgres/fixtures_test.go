package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// insertSubmittedSnapshotUnknownSkips builds a SUBMITTED snapshot whose skip
// evidence is UNKNOWN (not confirmed empty), which the completeness gate must
// treat as non-destructive (PARTIAL).
func insertSubmittedSnapshotUnknownSkips(t *testing.T, st *postgres.Store, ctx context.Context, snapID string, entries []domain.SnapshotEntry) []domain.SnapshotEntry {
	t.Helper()
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: pipeRoot, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: false,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft,
		EntryCount: int64p(int64(len(entries))),
	}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), snap); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatalf("submit snapshot: %v", err)
	}
	for i := range entries {
		entries[i].SnapshotID = snapID
		if err := st.InsertSnapshotEntry(ctx, st.Pool(), entries[i]); err != nil {
			t.Fatalf("insert entry: %v", err)
		}
	}
	return entries
}

func snapshotLifecycle(t *testing.T, st *postgres.Store, ctx context.Context, snapID string) domain.SnapshotLifecycleState {
	t.Helper()
	var lc string
	if err := st.Pool().QueryRow(ctx, `SELECT lifecycle_state FROM index_snapshot WHERE snapshot_id=$1::uuid`, snapID).Scan(&lc); err != nil {
		t.Fatalf("snapshot lifecycle: %v", err)
	}
	return domain.SnapshotLifecycleState(lc)
}

func TestFixtureDeltaBehaviour(t *testing.T) {
	mt := time.Now().UTC()
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1, RemovalGracePeriod: 0}

	t.Run("initial_population_and_add", func(t *testing.T) {
		st, ctx := newStore(t)
		seedPipelineRoot(t, st, ctx)
		v1 := []domain.SnapshotEntry{
			entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt),
			entryWithProviderID("b.txt", "/", "P2", "hb", 2, mt),
		}
		insertSubmittedSnapshot(t, st, ctx, "e1000000-0000-0000-0000-000000000001", v1)
		out, _ := processSnapshot(t, st, ctx, "e1000000-0000-0000-0000-000000000001", v1, cfg)
		if out.AppliedGeneration != 1 {
			t.Fatalf("initial population must reach generation 1, got %+v", out)
		}
		if lc := snapshotLifecycle(t, st, ctx, "e1000000-0000-0000-0000-000000000001"); lc != domain.SnapshotReconciled {
			t.Fatalf("successful reconcile must leave the Snapshot RECONCILED (B6), got %s", lc)
		}
	})

	t.Run("partial_unknown_skips_no_removal", func(t *testing.T) {
		st, ctx := newStore(t)
		seedPipelineRoot(t, st, ctx)
		v1 := []domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}
		insertSubmittedSnapshot(t, st, ctx, "e2000000-0000-0000-0000-000000000001", v1)
		processSnapshot(t, st, ctx, "e2000000-0000-0000-0000-000000000001", v1, cfg)

		insertSubmittedSnapshotUnknownSkips(t, st, ctx, "e2000000-0000-0000-0000-000000000002", nil)
		out, _ := processSnapshot(t, st, ctx, "e2000000-0000-0000-0000-000000000002", nil, cfg)
		if out.Mutated {
			t.Fatalf("UNKNOWN-skip (PARTIAL) absence must not advance removal evidence, got %+v", out)
		}
		if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, "/a.txt"); len(rows) != 1 {
			t.Fatalf("a.txt must remain PRESENT under PARTIAL, got %d", len(rows))
		}
	})

	t.Run("noop_then_re_reconcile", func(t *testing.T) {
		st, ctx := newStore(t)
		seedPipelineRoot(t, st, ctx)
		v1 := []domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}
		insertSubmittedSnapshot(t, st, ctx, "e3000000-0000-0000-0000-000000000001", v1)
		out1, id1 := processSnapshot(t, st, ctx, "e3000000-0000-0000-0000-000000000001", v1, cfg)

		insertSubmittedSnapshot(t, st, ctx, "e3000000-0000-0000-0000-000000000002", v1)
		eval2, err := st.EvaluateSnapshot(ctx, "e3000000-0000-0000-0000-000000000002", nil)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if !eval2.Identity.Equal(id1) {
			t.Fatalf("identical evaluated content must yield the same IO3 identity")
		}
		seq, _ := st.AllocateAdmission(ctx, st.Pool(), pipeRoot, "e3000000-0000-0000-0000-000000000002")
		out2, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
			RootID: pipeRoot, AdmissionSeq: seq, SnapshotID: "e3000000-0000-0000-0000-000000000002", Identity: eval2.Identity,
		}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
			return &postgres.Plan{}, nil
		})
		if err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if out2.Status != domain.AdmissionNoop {
			t.Fatalf("same identity at same generation must be NOOP, got %s", out2.Status)
		}
		_ = out1
	})

	t.Run("deleted_root_rejected_and_lifecycle", func(t *testing.T) {
		st, ctx := newStore(t)
		seedPipelineRoot(t, st, ctx)
		if err := st.SetRootLifecycle(ctx, st.Pool(), pipeRoot, domain.RootDeleted); err != nil {
			t.Fatalf("delete root: %v", err)
		}
		v1 := []domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}
		insertSubmittedSnapshot(t, st, ctx, "e4000000-0000-0000-0000-000000000001", v1)
		out, _ := processSnapshot(t, st, ctx, "e4000000-0000-0000-0000-000000000001", v1, cfg)
		if out.Status != domain.AdmissionRejected {
			t.Fatalf("DELETED root must reject, got %s", out.Status)
		}
		if lc := snapshotLifecycle(t, st, ctx, "e4000000-0000-0000-0000-000000000001"); lc != domain.SnapshotRejected {
			t.Fatalf("rejected snapshot must be REJECTED (B6), got %s", lc)
		}
	})
}
