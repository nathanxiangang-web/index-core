package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

const (
	pipeRoot = "cccccccc-0000-0000-0000-000000000001"
)

// buildPrior loads canonical resources of a root as reconcile inputs. For the
// PoC identity matching here, provider identity evidence is unavailable, so R3
// (content_hash) and R4 (path+size+mtime) drive matching.
func buildPrior(t *testing.T, st *postgres.Store, ctx context.Context, rootID string) []reconcile.PriorResource {
	t.Helper()
	rows, err := st.ListCanonicalResources(ctx, st.Pool(), rootID)
	if err != nil {
		t.Fatalf("list canonical: %v", err)
	}
	out := make([]reconcile.PriorResource, 0, len(rows))
	for _, r := range rows {
		out = append(out, reconcile.PriorResource{
			CanonicalResource:   r,
			ProviderIDAssurance: domain.IdentityUnavailable,
		})
	}
	return out
}

// runSnapshot inserts and fully processes one snapshot through evaluation and the
// Stage-2 reconcile transaction, returning the reconcile outcome.
func runSnapshot(t *testing.T, st *postgres.Store, ctx context.Context, snapshotID string,
	acceptance domain.AcceptanceState, identityValue string,
	entries []domain.SnapshotEntry, cfg reconcile.Config) postgres.ReconcileOutcome {
	t.Helper()

	snap := domain.Snapshot{
		SnapshotID: snapshotID, RootID: pipeRoot, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, CompletenessFlag: domain.CompletenessFlagComplete,
		LifecycleState: domain.SnapshotDraft,
	}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), snap); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapshotID); err != nil {
		t.Fatalf("submit snapshot: %v", err)
	}
	if err := st.SetSnapshotEvaluated(ctx, st.Pool(), snapshotID, acceptance, nil); err != nil {
		t.Fatalf("evaluate snapshot: %v", err)
	}
	for _, e := range entries {
		e.SnapshotID = snapshotID
		if err := st.InsertSnapshotEntry(ctx, st.Pool(), e); err != nil {
			t.Fatalf("insert entry: %v", err)
		}
	}
	seq, err := st.AllocateAdmission(ctx, st.Pool(), pipeRoot, snapshotID)
	if err != nil {
		t.Fatalf("allocate admission: %v", err)
	}
	id := domain.SnapshotIdentity{
		Kind: domain.IdentityDeterministicDigest, Namespace: "kernel.index-core/io3", Version: "v1", Value: identityValue,
	}
	out, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: pipeRoot, AdmissionSeq: seq, SnapshotID: snapshotID, Identity: id,
	}, func(_ []domain.CanonicalResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		prior := buildPrior(t, st, ctx, pipeRoot)
		res := reconcile.Reconcile(prior, entries, acceptance, cfg, time.Now().UTC())
		return st.PlanFromResult(pipeRoot, res), nil
	})
	if err != nil {
		t.Fatalf("reconcile %s: %v", snapshotID, err)
	}
	return out
}

func requirePresence(t *testing.T, st *postgres.Store, ctx context.Context, path string) domain.CanonicalResource {
	t.Helper()
	rows, err := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, path)
	if err != nil {
		t.Fatalf("present at path %s: %v", path, err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one PRESENT at %s, got %d", path, len(rows))
	}
	return rows[0]
}

func TestPipelineAddThenConfirmedRemoval(t *testing.T) {
	st, ctx := newStore(t)
	if err := st.CreateRoot(ctx, st.Pool(), pipeRoot, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}

	v1 := time.Now().UTC()
	alg := "sha256"
	entry := func(name, hash string, size int64) domain.SnapshotEntry {
		return domain.SnapshotEntry{EntryLocalID: name, Name: name, ParentRef: "/",
			Size: &size, Mtime: &v1, ContentHash: &hash, HashAlgorithm: &alg}
	}

	// V1: two new files -> generation 1, two resource-added events.
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1}
	out1 := runSnapshot(t, st, ctx, "d0000000-0000-0000-0000-000000000001",
		domain.AcceptanceComplete, "v1", []domain.SnapshotEntry{
			entry("a.txt", "ha", 1),
			entry("b.txt", "hb", 2),
		}, cfg)
	if !out1.Mutated || out1.AppliedGeneration != 1 {
		t.Fatalf("V1 must mutate to generation 1, got %+v", out1)
	}
	requirePresence(t, st, ctx, "/a.txt")
	requirePresence(t, st, ctx, "/b.txt")
	events, _ := st.ReadJournal(ctx, st.Pool(), pipeRoot, 0, 10)
	if len(events) != 2 {
		t.Fatalf("V1 must emit 2 resource-added events, got %d", len(events))
	}

	// V2: only b.txt observed -> a.txt missing in a COMPLETE snapshot.
	// grace 0 + min 1 -> confirmed removal; generation advances to 2.
	cfg2 := reconcile.Config{MinConsecutiveCompleteMissing: 1, RemovalGracePeriod: 0}
	out2 := runSnapshot(t, st, ctx, "d0000000-0000-0000-0000-000000000002",
		domain.AcceptanceComplete, "v2", []domain.SnapshotEntry{
			entry("b.txt", "hb", 2),
		}, cfg2)
	if out2.AppliedGeneration != 2 {
		t.Fatalf("V2 must advance to generation 2, got %+v", out2)
	}
	requirePresence(t, st, ctx, "/b.txt")

	// a.txt must now be a REMOVED tombstone; confirm it is excluded from PRESENT reads.
	if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, "/a.txt"); len(rows) != 0 {
		t.Fatalf("a.txt must be REMOVED (excluded from PRESENT reads), got %d rows", len(rows))
	}
	events2, _ := st.ReadJournal(ctx, st.Pool(), pipeRoot, 2, 10)
	if len(events2) == 0 || events2[len(events2)-1].EventType != domain.EventResourceRemoved {
		t.Fatalf("V2 must emit a resource-removed event, got %+v", events2)
	}
}

func TestPipelinePartialDoesNotRemove(t *testing.T) {
	st, ctx := newStore(t)
	if err := st.CreateRoot(ctx, st.Pool(), pipeRoot, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	now := time.Now().UTC()
	size := int64(1)
	hash := "ha"
	alg := "sha256"
	e := domain.SnapshotEntry{EntryLocalID: "a.txt", Name: "a.txt", ParentRef: "/",
		Size: &size, Mtime: &now, ContentHash: &hash, HashAlgorithm: &alg}
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1}
	runSnapshot(t, st, ctx, "e0000000-0000-0000-0000-000000000001",
		domain.AcceptanceComplete, "p1", []domain.SnapshotEntry{e}, cfg)
	genAfterV1, _ := st.GetRoot(ctx, st.Pool(), pipeRoot)

	// A PARTIAL snapshot observing nothing must NOT advance removal evidence.
	out := runSnapshot(t, st, ctx, "e0000000-0000-0000-0000-000000000002",
		domain.AcceptancePartial, "p2", nil, cfg)
	if out.Mutated {
		t.Fatalf("PARTIAL snapshot with no observation must not mutate, got %+v", out)
	}
	requirePresence(t, st, ctx, "/a.txt")
	genAfterPartial, _ := st.GetRoot(ctx, st.Pool(), pipeRoot)
	if genAfterPartial.CurrentGeneration != genAfterV1.CurrentGeneration {
		t.Fatalf("PARTIAL reconcile must not advance generation, %d -> %d",
			genAfterV1.CurrentGeneration, genAfterPartial.CurrentGeneration)
	}
}
