package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestIssue130SelectedSequenceFence(t *testing.T) {
	p := testutil.Issue130Pool(t)
	testutil.ResetSchema(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	s := New(p)
	root, snap := reconcile.NewUUID(), reconcile.NewUUID()
	if err := s.CreateRoot(ctx, p, root, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	fresh, strong := domain.FreshDirect, domain.StrongFailureVisibility
	n := int64(0)
	input := domain.Snapshot{SnapshotID: snap, RootID: root, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(), TraversalStatus: domain.TraversalPartial, SkippedScopesKnownEmpty: true, FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong, CompletenessFlag: domain.CompletenessFlagPartial, LifecycleState: domain.SnapshotDraft, EntryCount: &n}
	if err := s.CreateDraftSnapshot(ctx, input, nil); err != nil {
		t.Fatal(err)
	}
	seq, err := s.SubmitAndAdmitSnapshot(ctx, root, snap)
	if err != nil {
		t.Fatal(err)
	}
	cfg := reconcile.Config{RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour}
	original, err := NewCoordinator(s, cfg).ProcessSnapshot(ctx, root, snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []int64{seq + 1, 0, -1} {
		if _, err = s.reconcileHeadSafe(ctx, root, wrong, snap, cfg); err == nil {
			t.Fatal("wrong selected admission sequence accepted")
		}
	}
	if got, err := s.reconcileHeadSafe(ctx, root, seq, snap, cfg); err != nil || got != original {
		t.Fatalf("correct selected receipt rejected: %+v %v", got, err)
	}
	// The ordinary admission primitive stays SUBMITTED-only, even after repair.
	if _, _, err = s.AdmitOrResumeSnapshot(ctx, root, snap); err != ErrNotSubmitted {
		t.Fatalf("public admission contract changed: %v", err)
	}
}
