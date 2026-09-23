package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/query"
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

// R3-1: reappearance must not false-NOOP, and must reset MISSING evidence.
func TestReappearanceAfterMissingIsNotNOOP(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	mt := time.Now().UTC()

	v1 := append([]domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}, keepers(t, mt)...)
	insertSubmittedSnapshot(t, st, ctx, "fa100000-0000-0000-0000-000000000001", v1)
	processSnapshot(t, st, ctx, "fa100000-0000-0000-0000-000000000001", cfg)
	a := requirePresent(t, st, ctx, "/a.txt")

	keeper := keepers(t, mt)
	insertSubmittedSnapshot(t, st, ctx, "fa100000-0000-0000-0000-000000000002", keeper)
	processSnapshot(t, st, ctx, "fa100000-0000-0000-0000-000000000002", cfg)
	if r, _ := st.GetCanonicalResource(ctx, st.Pool(), a.ResourceID); r.RemovalEvidenceState == domain.RemovalEvidenceNone {
		t.Fatal("first MISSING must record removal evidence")
	}

	// Snapshot 3 is evidence-identical to snapshot 1; a.txt reappears.
	insertSubmittedSnapshot(t, st, ctx, "fa100000-0000-0000-0000-000000000003", v1)
	out := processSnapshot(t, st, ctx, "fa100000-0000-0000-0000-000000000003", cfg)
	if out.Status == domain.AdmissionNoop {
		t.Fatal("reappearance must NOT be IO3-NOOP against the pre-missing identity (R3-1)")
	}
	if r, _ := st.GetCanonicalResource(ctx, st.Pool(), a.ResourceID); r.RemovalEvidenceState != domain.RemovalEvidenceNone {
		t.Fatalf("reappearance must reset removal evidence, got %s", r.RemovalEvidenceState)
	}
}

// R3-2: corroboration requires a qualifying earlier admitted observation with the
// same reduced scope signature; an arbitrary historical same-size Snapshot must
// not corroborate.
func TestCorroborationRequiresQualifyingIndependentObservation(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	mt := time.Now().UTC()

	full := append([]domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}, keepers(t, mt)...)
	insertSubmittedSnapshot(t, st, ctx, "fb100000-0000-0000-0000-000000000001", full)
	processSnapshot(t, st, ctx, "fb100000-0000-0000-0000-000000000001", cfg)

	reduced := []domain.SnapshotEntry{entryWithProviderID("k1.txt", "/", "PK-k1.txt", "hk-k1.txt", 10, mt)}
	insertSubmittedSnapshot(t, st, ctx, "fb100000-0000-0000-0000-000000000002", reduced)
	processSnapshot(t, st, ctx, "fb100000-0000-0000-0000-000000000002", cfg)
	if snapshotAcceptance(t, st, ctx, "fb100000-0000-0000-0000-000000000002") != domain.AcceptanceSuspicious {
		t.Fatalf("first significant shrink must be SUSPICIOUS, got %s", snapshotAcceptance(t, st, ctx, "fb100000-0000-0000-0000-000000000002"))
	}

	// A later admitted observation with the same reduced signature corroborates.
	insertSubmittedSnapshot(t, st, ctx, "fb100000-0000-0000-0000-000000000003", reduced)
	processSnapshot(t, st, ctx, "fb100000-0000-0000-0000-000000000003", cfg)
	if snapshotCorroboration(t, st, ctx, "fb100000-0000-0000-0000-000000000003") != domain.ShrinkCorroborated {
		t.Fatalf("later qualifying observation must derive CORROBORATED, got %s", snapshotCorroboration(t, st, ctx, "fb100000-0000-0000-0000-000000000003"))
	}
	if snapshotAcceptance(t, st, ctx, "fb100000-0000-0000-0000-000000000003") != domain.AcceptanceComplete {
		t.Fatalf("corroborated shrink must be COMPLETE, got %s", snapshotAcceptance(t, st, ctx, "fb100000-0000-0000-0000-000000000003"))
	}
}

// R3-2 negative: an earlier admitted observation with a DIFFERENT reduced scope
// signature must not corroborate.
func TestCorroborationRejectsUnrelatedObservation(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	mt := time.Now().UTC()

	full := append([]domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}, keepers(t, mt)...)
	insertSubmittedSnapshot(t, st, ctx, "fc100000-0000-0000-0000-000000000001", full)
	processSnapshot(t, st, ctx, "fc100000-0000-0000-0000-000000000001", cfg)

	// A different small scope (k2), unrelated to the later reduce-to-k1.
	other := []domain.SnapshotEntry{entryWithProviderID("k2.txt", "/", "PK-k2.txt", "hk-k2.txt", 11, mt)}
	insertSubmittedSnapshot(t, st, ctx, "fc100000-0000-0000-0000-000000000002", other)
	processSnapshot(t, st, ctx, "fc100000-0000-0000-0000-000000000002", cfg)

	reduced := []domain.SnapshotEntry{entryWithProviderID("k1.txt", "/", "PK-k1.txt", "hk-k1.txt", 10, mt)}
	insertSubmittedSnapshot(t, st, ctx, "fc100000-0000-0000-0000-000000000003", reduced)
	processSnapshot(t, st, ctx, "fc100000-0000-0000-0000-000000000003", cfg)
	if snapshotCorroboration(t, st, ctx, "fc100000-0000-0000-0000-000000000003") != domain.ShrinkNone {
		t.Fatalf("unrelated earlier observation must not corroborate, got %s", snapshotCorroboration(t, st, ctx, "fc100000-0000-0000-0000-000000000003"))
	}
}

// R3-6: invalid config fails closed in the normal Kernel path.
func TestCoordinatorRejectsInvalidConfig(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	bad := reconcile.Config{RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: 2 * time.Hour}
	if _, err := postgres.NewCoordinator(st, bad).ProcessSnapshot(ctx, pipeRoot, "nope"); err == nil {
		t.Fatal("grace < horizon must fail closed in the coordinator")
	}
	if _, err := postgres.NewCoordinator(st, reconcile.Config{MinIndependentConfirmations: 2}).ProcessSnapshot(ctx, pipeRoot, "nope"); err == nil {
		t.Fatal("MinIndependentConfirmations > 1 must fail closed in the coordinator")
	}
}

// R3-7: Q4 real hierarchy + generation-bound cursor, and independent root visibility.
func TestQueryHierarchyCursorAndRootVisibility(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	mt := time.Now().UTC()

	dir := domain.SnapshotEntry{EntryLocalID: "d", Name: "d", ParentRef: "/", IsDir: true,
		ProviderObjectID: sp("PD"), ProviderObjectIDScope: sp("root"), ProviderIdentityAssurance: stablePtr()}
	child := entryWithProviderID("c.txt", "/d", "PC", "hc", 3, mt)
	insertSubmittedSnapshot(t, st, ctx, "fd100000-0000-0000-0000-000000000001", []domain.SnapshotEntry{dir, child})
	processSnapshot(t, st, ctx, "fd100000-0000-0000-0000-000000000001", cfg)

	dirRes := requirePresent(t, st, ctx, "/d")
	childRes := requirePresent(t, st, ctx, "/d/c.txt")
	if childRes.ParentResourceID == nil || *childRes.ParentResourceID != dirRes.ResourceID {
		t.Fatalf("child parent_resource_id must be the directory's resource_id (R3-7)")
	}
	qr := newQueryReader(st)
	page, err := qr.ListResources(ctx, pipeRoot, &dirRes.ResourceID, query.ReadOptions{}, nil, 100)
	if err != nil {
		t.Fatalf("Q4 list children: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ResourceID != childRes.ResourceID {
		t.Fatalf("Q4 must return the child by parent resource_id, got %+v", page.Items)
	}

	// Delete the root: PRESENT children must not leak without deleted-root opt-in.
	if err := st.SetRootLifecycle(ctx, st.Pool(), pipeRoot, domain.RootDeleted); err != nil {
		t.Fatal(err)
	}
	if got, _ := qr.GetResource(ctx, childRes.ResourceID, query.ReadOptions{}); got != nil {
		t.Fatal("DELETED root child must not leak without explicit deleted-root opt-in")
	}
	if got, _ := qr.GetResource(ctx, childRes.ResourceID, query.ReadOptions{IncludeDeletedRoot: true}); got == nil {
		t.Fatal("explicit deleted-root opt-in must read the retained partition")
	}
}

// R2-12 retained: FAILED acceptance must be REJECTED (now via the coordinator).
func TestFailedSnapshotRejectedViaCoordinator(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	snapID := "fe100000-0000-0000-0000-000000000001"
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: pipeRoot, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalFailed, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagPartial, LifecycleState: domain.SnapshotDraft,
	}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), snap); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatal(err)
	}
	out := processSnapshot(t, st, ctx, snapID, reconcile.Config{})
	if out.Status != domain.AdmissionRejected || out.SnapshotLifecycle != domain.SnapshotRejected {
		t.Fatalf("FAILED must map to REJECTED/REJECTED, got %+v", out)
	}
}

func stablePtr() *domain.ProviderIdentityAssurance {
	v := domain.IdentityStableWithinScope
	return &v
}

func sp(s string) *string { return &s }
