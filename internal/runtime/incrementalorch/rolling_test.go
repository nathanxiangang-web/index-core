package incrementalorch_test

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

type rollingFake struct {
	fakeStore
	prepared, maintained, rollingEmits int
}

func (f *rollingFake) PrepareMirrorRolling(context.Context) error { f.prepared++; return nil }
func (f *rollingFake) MaintainMirrorBindings(_ context.Context, _ time.Time, limit int) error {
	if limit != 5 {
		panic("unbounded maintenance")
	}
	f.maintained++
	return nil
}
func (f *rollingFake) ListDueWatchesRolling(ctx context.Context, now time.Time, limit int) ([]state.ScopeWatchState, error) {
	return f.ListDueWatches(ctx, now, limit)
}
func (f *rollingFake) EmitRollingPoll(context.Context, string, string, int64, time.Time) (state.DirtyScopeWork, error) {
	f.rollingEmits++
	return state.DirtyScopeWork{}, postgres.ErrMirrorDeferred
}

func TestIssue134RollingBoundedDeferredStillDrainsForeground(t *testing.T) {
	f := &rollingFake{fakeStore: fakeStore{due: []state.ScopeWatchState{{RootID: "root", ScopeKey: "/", WatchState: state.WatchHot}, {RootID: "root", ScopeKey: "/managed", WatchState: state.WatchWarm, CadenceClass: "MIRROR_REVISIT", Priority: state.PriorityNormal, SourceSet: []state.WatchSource{state.WatchSourceBackstopEnroll}}}}}
	exec := &fakeExec{}
	runner, err := incrementalorch.NewRunner(f, exec, func() time.Time { return p6Now })
	if err != nil {
		t.Fatal(err)
	}
	cfg := incrementalorch.Config{MirrorRevisitEnabled: true, MirrorRollingEnabled: true, MaxDueWatchAttempts: 2, MaxExecuteItems: 5, MaxWallTime: time.Second}
	for i := 0; i < 2; i++ {
		result, e := runner.RunCycle(context.Background(), cfg)
		if e != nil || result.DueAttempted != 2 || result.DueEmitted != 1 || result.DueDeferred != 1 || !result.ExecutorRan {
			t.Fatal(result, e)
		}
	}
	if f.prepared != 1 || f.maintained != 2 || f.rollingEmits != 2 || exec.calls != 2 || len(f.emitCalls) != 2 {
		t.Fatal("rolling repeated setup or starved existing work", f, exec)
	}
}
