package postgres_test

import (
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
)

// R2-10 retained: entry_count is derived from normalized entries, so a
// disagreeing Snapshot metadata count cannot change classification.
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
		EntryCount: int64p(999),
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
	processSnapshot(t, st, ctx, snapID, reconcile.Config{})
	if got := snapshotAcceptance(t, st, ctx, snapID); got != domain.AcceptanceComplete {
		t.Fatalf("derived entry_count (1) must drive classification, got %s", got)
	}
}
