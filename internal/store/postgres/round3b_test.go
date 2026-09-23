package postgres_test

import (
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// R2-10: entry_count is derived from the normalized entries, so a disagreeing
// Snapshot metadata count cannot change the completeness classification.
func TestEntryCountMetadataMismatchIgnored(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	mt := time.Now().UTC()
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	snapID := "fa000000-0000-0000-0000-000000000001"
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: pipeRoot, Provenance: []byte(`{}`), ObservedAt: mt,
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft,
		EntryCount: int64p(999), // deliberately wrong
	}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), snap); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatal(err)
	}
	e := entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)
	e.SnapshotID = snapID
	if err := st.InsertSnapshotEntry(ctx, st.Pool(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AllocateAdmission(ctx, st.Pool(), pipeRoot, snapID); err != nil {
		t.Fatal(err)
	}
	res, err := st.EvaluateSnapshot(ctx, pipeRoot, snapID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Acceptance != domain.AcceptanceComplete {
		t.Fatalf("derived entry_count (1) must drive classification, got %s", res.Acceptance)
	}
}
