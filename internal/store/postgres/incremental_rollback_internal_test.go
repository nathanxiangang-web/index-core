package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// TestP3EmitDuePollRollsBackBothHalves proves the atomic due-poll: the work
// merge and the watch advance commit together, and an injected failure after
// the work merge exposes NEITHER half.
func TestP3EmitDuePollRollsBackBothHalves(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	const rootID = "00000000-0000-0000-0000-00000000b001"
	if _, err := pool.Exec(ctx,
		`INSERT INTO index_root(root_id, scope_descriptor, lifecycle_state)
		 VALUES ($1, '{}'::jsonb, 'ACTIVE')`, rootID); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	st := New(pool)

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	due := now.Add(-time.Second)
	iv := int64(120)
	w, err := st.CreateWatch(ctx, state.ScopeWatchState{
		RootID: rootID, ScopeKey: "/p", WatchState: state.WatchHot, CadenceClass: "HOT_120",
		EffectiveIntervalSeconds: &iv,
		SourceSet:                []state.WatchSource{state.WatchSourceOperatorPolicy},
		Priority:                 state.PriorityNormal, NextDueAt: &due,
	})
	if err != nil {
		t.Fatalf("create watch: %v", err)
	}

	emitDuePollAfterMergeHook = func() error { return errors.New("injected watch-half failure") }
	defer func() { emitDuePollAfterMergeHook = nil }()

	if _, err := st.EmitDuePoll(ctx, rootID, "/p", w.Version, now); err == nil {
		t.Fatal("expected the injected failure to abort EmitDuePoll")
	}

	// First half (work merge) must be rolled back.
	if _, err := st.GetWork(ctx, rootID, "/p"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("work merge must roll back, got %v", err)
	}
	// Second half (watch advance) must be rolled back.
	after, err := st.GetWatch(ctx, rootID, "/p")
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != w.Version || after.LastDueAt != nil {
		t.Fatalf("watch half must roll back, got version=%d last_due=%v", after.Version, after.LastDueAt)
	}
	if after.NextDueAt == nil || !after.NextDueAt.Equal(*w.NextDueAt) {
		t.Fatalf("watch next_due_at must be unchanged, got %v", after.NextDueAt)
	}
}
