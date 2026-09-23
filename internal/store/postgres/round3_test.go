package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func insertSubmittedSnapshotAt(t *testing.T, st *postgres.Store, ctx context.Context, snapID string, entries []domain.SnapshotEntry, observedAt time.Time) {
	t.Helper()
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: pipeRoot, Provenance: []byte(`{}`), ObservedAt: observedAt,
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
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
}

func keepers(t *testing.T, mt time.Time) []domain.SnapshotEntry {
	t.Helper()
	var out []domain.SnapshotEntry
	for i, name := range []string{"k1.txt", "k2.txt", "k3.txt", "k4.txt", "k5.txt"} {
		out = append(out, entryWithProviderID(name, "/", "PK-"+name, "hk-"+name, int64(i+10), mt))
	}
	return out
}

// R2-3: the first MISSING observation is evidence-only and must not advance the
// canonical generation.
func TestFirstMissingEvidenceDoesNotAdvanceGeneration(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	mt := time.Now().UTC()

	v1 := append([]domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}, keepers(t, mt)...)
	insertSubmittedSnapshot(t, st, ctx, "f1000000-0000-0000-0000-000000000001", v1)
	processSnapshot(t, st, ctx, "f1000000-0000-0000-0000-000000000001", v1, cfg)
	genAfterV1, _ := st.GetRoot(ctx, st.Pool(), pipeRoot)

	keeper := keepers(t, mt)
	insertSubmittedSnapshot(t, st, ctx, "f1000000-0000-0000-0000-000000000002", keeper)
	processSnapshot(t, st, ctx, "f1000000-0000-0000-0000-000000000002", keeper, cfg)

	genAfterV2, _ := st.GetRoot(ctx, st.Pool(), pipeRoot)
	if genAfterV2.CurrentGeneration != genAfterV1.CurrentGeneration {
		t.Fatalf("first MISSING evidence must not advance generation: %d -> %d",
			genAfterV1.CurrentGeneration, genAfterV2.CurrentGeneration)
	}
	if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, "/a.txt"); len(rows) != 1 {
		t.Fatalf("a.txt must remain PRESENT with MISSING evidence, got %d", len(rows))
	}
}

// R2-12: a FAILED verdict must be REJECTED, never APPLIED/RECONCILED.
func TestFailedSnapshotRejected(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	snapID := "f2000000-0000-0000-0000-000000000001"
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: pipeRoot, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalFailed, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagPartial, LifecycleState: domain.SnapshotDraft,
	}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), snap); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := st.AllocateAdmission(ctx, st.Pool(), pipeRoot, snapID); err != nil {
		t.Fatalf("alloc: %v", err)
	}
	eval, err := st.EvaluateSnapshot(ctx, pipeRoot, snapID)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if eval.Acceptance != domain.AcceptanceFailed {
		t.Fatalf("failed traversal must evaluate FAILED, got %s", eval.Acceptance)
	}
	out, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: pipeRoot, AdmissionSeq: 1, SnapshotID: snapID, Identity: eval.Identity,
	}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		t.Fatal("a FAILED snapshot must be rejected before plan computation")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if out.Status != domain.AdmissionRejected || out.SnapshotLifecycle != domain.SnapshotRejected {
		t.Fatalf("FAILED must map to REJECTED/REJECTED, got %+v", out)
	}
}

// R2-6: scope-shrink corroboration is Kernel-derived from persisted admitted
// observations, not injected.
func TestCorroborationDerivedEndToEnd(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	mt := time.Now().UTC()

	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	full := append([]domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}, keepers(t, mt)...)
	insertSubmittedSnapshot(t, st, ctx, "f3000000-0000-0000-0000-000000000001", full)
	processSnapshot(t, st, ctx, "f3000000-0000-0000-0000-000000000001", full, cfg)

	reduced := []domain.SnapshotEntry{entryWithProviderID("k1.txt", "/", "PK-k1.txt", "hk-k1.txt", 10, mt)}
	insertSubmittedSnapshot(t, st, ctx, "f3000000-0000-0000-0000-000000000002", reduced)
	if _, err := st.AllocateAdmission(ctx, st.Pool(), pipeRoot, "f3000000-0000-0000-0000-000000000002"); err != nil {
		t.Fatal(err)
	}
	first, err := st.EvaluateSnapshot(ctx, pipeRoot, "f3000000-0000-0000-0000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if first.Acceptance != domain.AcceptanceSuspicious || first.Corroboration != domain.ShrinkNone {
		t.Fatalf("first significant shrink must be SUSPICIOUS/NONE, got %s/%s", first.Acceptance, first.Corroboration)
	}

	insertSubmittedSnapshot(t, st, ctx, "f3000000-0000-0000-0000-000000000003", reduced)
	if _, err := st.AllocateAdmission(ctx, st.Pool(), pipeRoot, "f3000000-0000-0000-0000-000000000003"); err != nil {
		t.Fatal(err)
	}
	second, err := st.EvaluateSnapshot(ctx, pipeRoot, "f3000000-0000-0000-0000-000000000003")
	if err != nil {
		t.Fatal(err)
	}
	if second.Corroboration != domain.ShrinkCorroborated || second.Acceptance != domain.AcceptanceComplete {
		t.Fatalf("independent later shrink must be CORROBORATED/COMPLETE, got %s/%s", second.Acceptance, second.Corroboration)
	}
}

// R2-9: IdentityEvidence observed_at is the Snapshot observation time.
func TestObservationTimeIsSnapshotTime(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	mt := time.Now().UTC().Truncate(time.Second)

	entries := []domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}
	insertSubmittedSnapshotAt(t, st, ctx, "f4000000-0000-0000-0000-000000000001", entries, mt)
	processSnapshot(t, st, ctx, "f4000000-0000-0000-0000-000000000001", entries, cfg)

	var observed time.Time
	if err := st.Pool().QueryRow(ctx,
		`SELECT min(observed_at) FROM index_identity_evidence_observation`).Scan(&observed); err != nil {
		t.Fatalf("observed_at: %v", err)
	}
	if !observed.UTC().Truncate(time.Second).Equal(mt) {
		t.Fatalf("evidence observed_at must equal the snapshot observation time %s, got %s", mt, observed.UTC())
	}
}

// R2-11: Q1 get_root + Q4 list_resources/hierarchy children, and no leak of a
// DELETED root's PRESENT children through default reads.
func TestQueryGetRootListResourcesAndDeletedLeak(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	mt := time.Now().UTC()

	v1 := []domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}
	insertSubmittedSnapshot(t, st, ctx, "f5000000-0000-0000-0000-000000000001", v1)
	processSnapshot(t, st, ctx, "f5000000-0000-0000-0000-000000000001", v1, cfg)
	r := requirePresent(t, st, ctx, "/a.txt")
	qr := newQueryReader(st)

	root, err := qr.GetRoot(ctx, pipeRoot, false, false)
	if err != nil || root == nil {
		t.Fatalf("Q1 get_root must return the ACTIVE root, got %+v err=%v", root, err)
	}
	children, err := qr.ListResources(ctx, pipeRoot, nil, false, 100)
	if err != nil || len(children.Items) != 1 {
		t.Fatalf("Q4 list_resources must return the root-level child, got %d err=%v", len(children.Items), err)
	}

	// Delete the root: PRESENT children must not leak through default reads.
	if err := st.SetRootLifecycle(ctx, st.Pool(), pipeRoot, domain.RootDeleted); err != nil {
		t.Fatal(err)
	}
	if got, _ := qr.GetResource(ctx, r.ResourceID, false); got != nil {
		t.Fatalf("DELETED root child must not leak through default GetResource, got %+v", got)
	}
	page, _ := qr.ListActivePage(ctx, pipeRoot, nil, 100)
	if len(page.Items) != 0 {
		t.Fatalf("DELETED root child must not leak through default ListActivePage, got %d", len(page.Items))
	}
	res, _ := qr.ResolvePath(ctx, pipeRoot, "/a.txt", false)
	if len(res.Matches) != 0 {
		t.Fatalf("DELETED root child must not leak through default ResolvePath, got %d", len(res.Matches))
	}
	if got, _ := qr.GetResource(ctx, r.ResourceID, true); got == nil || got.ResourcePresence != domain.ResourcePresent {
		t.Fatalf("explicit audit access must still read the retained partition, got %+v", got)
	}
}
