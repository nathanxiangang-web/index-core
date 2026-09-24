package incrementalorch_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

var p6Now = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

type p6Clock struct {
	t     time.Time
	calls int
}

func (c *p6Clock) now() time.Time { c.calls++; return c.t }

type fakeStore struct {
	due       []state.ScopeWatchState
	listErr   error
	listCalls int
	listLimit int
	listNow   time.Time

	emitCalls []string
	emitNows  []time.Time
	emitFn    func(ctx context.Context, rootID, scopeKey string, now time.Time) error
}

func (f *fakeStore) ListDueWatches(_ context.Context, now time.Time, limit int) ([]state.ScopeWatchState, error) {
	f.listCalls++
	f.listNow = now
	f.listLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := f.due
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) EmitDuePoll(ctx context.Context, rootID, scopeKey string, _ int64, now time.Time) (state.DirtyScopeWork, error) {
	f.emitCalls = append(f.emitCalls, rootID+"|"+scopeKey)
	f.emitNows = append(f.emitNows, now)
	if f.emitFn != nil {
		if err := f.emitFn(ctx, rootID, scopeKey, now); err != nil {
			return state.DirtyScopeWork{}, err
		}
	}
	return state.DirtyScopeWork{}, nil
}

type fakeExec struct {
	calls int
	cfg   incrementalexec.CycleConfig
	res   incrementalexec.CycleResult
	err   error
	fn    func(ctx context.Context, cfg incrementalexec.CycleConfig) (incrementalexec.CycleResult, error)
}

func (f *fakeExec) Run(ctx context.Context, cfg incrementalexec.CycleConfig) (incrementalexec.CycleResult, error) {
	f.calls++
	f.cfg = cfg
	if f.fn != nil {
		return f.fn(ctx, cfg)
	}
	return f.res, f.err
}

func p6Watches(n int) []state.ScopeWatchState {
	out := make([]state.ScopeWatchState, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, state.ScopeWatchState{
			RootID:   fmt.Sprintf("root-%02d", i),
			ScopeKey: fmt.Sprintf("/scope-%02d", i),
			Version:  int64(i + 1),
		})
	}
	return out
}

func p6ValidConfig() incrementalorch.Config {
	return incrementalorch.Config{MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: time.Minute}
}

func p6Runner(t *testing.T, store incrementalorch.DueWatchStore, exec incrementalorch.ExecutorCycle, now func() time.Time) *incrementalorch.Runner {
	t.Helper()
	r, err := incrementalorch.NewRunner(store, exec, now)
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	return r
}

func TestP6NewRunnerRequiresDependencies(t *testing.T) {
	if _, err := incrementalorch.NewRunner(nil, &fakeExec{}, nil); err == nil {
		t.Fatal("nil store must be rejected")
	}
	if _, err := incrementalorch.NewRunner(&fakeStore{}, nil, nil); err == nil {
		t.Fatal("nil executor must be rejected")
	}
}

func TestP6ConfigBounds(t *testing.T) {
	invalid := []incrementalorch.Config{
		{MaxDueWatchAttempts: 0, MaxExecuteItems: 1, MaxWallTime: time.Second},
		{MaxDueWatchAttempts: 6, MaxExecuteItems: 1, MaxWallTime: time.Second},
		{MaxDueWatchAttempts: 1, MaxExecuteItems: 0, MaxWallTime: time.Second},
		{MaxDueWatchAttempts: 1, MaxExecuteItems: 6, MaxWallTime: time.Second},
		{MaxDueWatchAttempts: 1, MaxExecuteItems: 1, MaxWallTime: 0},
		{MaxDueWatchAttempts: 1, MaxExecuteItems: 1, MaxWallTime: 61 * time.Second},
	}
	for _, cfg := range invalid {
		store := &fakeStore{}
		exec := &fakeExec{}
		res, err := p6Runner(t, store, exec, nil).RunCycle(context.Background(), cfg)
		if err == nil {
			t.Fatalf("config %+v must be rejected", cfg)
		}
		if store.listCalls != 0 || len(store.emitCalls) != 0 || exec.calls != 0 {
			t.Fatalf("invalid config %+v must perform zero Store/P5 work", cfg)
		}
		if res.StopReason != "" {
			t.Fatalf("invalid config must not report a cycle result, got %+v", res)
		}
	}

	valid := []incrementalorch.Config{
		{MaxDueWatchAttempts: 1, MaxExecuteItems: 1, MaxWallTime: time.Second},
		{MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: incrementalorch.MaxWallTimeCap},
	}
	for _, cfg := range valid {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("valid config %+v rejected: %v", cfg, err)
		}
	}
}

func TestP6NoDueWatchesStillRunsP5(t *testing.T) {
	store := &fakeStore{}
	exec := &fakeExec{res: incrementalexec.CycleResult{StopReason: incrementalexec.StopNoEligibleWork}}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), p6ValidConfig())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.StopReason != incrementalorch.StopCompleted {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if !res.ExecutorRan || exec.calls != 1 {
		t.Fatalf("P5 must run exactly once even with zero due watches, calls=%d", exec.calls)
	}
	if res.Executor.StopReason != incrementalexec.StopNoEligibleWork {
		t.Fatalf("nested P5 result must be retained, got %+v", res.Executor)
	}
}

func TestP6DueSnapshotHardBound(t *testing.T) {
	store := &fakeStore{due: p6Watches(6)}
	exec := &fakeExec{}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), p6ValidConfig())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if store.listLimit != 6 {
		t.Fatalf("due query limit must be attempts+1=6, got %d", store.listLimit)
	}
	if store.listCalls != 1 {
		t.Fatalf("no requery/paging allowed, got %d list calls", store.listCalls)
	}
	if res.DueCandidates != 6 || !res.MoreDueWatches {
		t.Fatalf("candidates/more = %d/%v, want 6/true", res.DueCandidates, res.MoreDueWatches)
	}
	if res.DueAttempted != 5 || res.DueEmitted != 5 || len(store.emitCalls) != 5 {
		t.Fatalf("only the first five rows may be attempted, got %+v (emits=%v)", res, store.emitCalls)
	}
	if store.emitCalls[4] != "root-04|/scope-04" {
		t.Fatalf("sixth row must never be emitted, emits=%v", store.emitCalls)
	}
}

func TestP6FixedObservedAt(t *testing.T) {
	store := &fakeStore{due: p6Watches(3)}
	exec := &fakeExec{}
	clock := &p6Clock{t: p6Now}
	res, err := p6Runner(t, store, exec, clock.now).RunCycle(context.Background(), p6ValidConfig())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !store.listNow.Equal(res.ObservedAt) {
		t.Fatalf("ListDueWatches time = %s, want observed_at %s", store.listNow, res.ObservedAt)
	}
	for i, n := range store.emitNows {
		if !n.Equal(res.ObservedAt) {
			t.Fatalf("EmitDuePoll[%d] time = %s, want observed_at %s", i, n, res.ObservedAt)
		}
	}
	// now() must not be called once per watch: 3 watches, but a fixed small
	// number of clock reads (start/observed + finished).
	if clock.calls > 4 {
		t.Fatalf("now() called %d times for 3 watches; observed_at must be captured once", clock.calls)
	}
}

func TestP6CASStaleSkipsAndContinues(t *testing.T) {
	store := &fakeStore{due: p6Watches(2), emitFn: func(_ context.Context, rootID, _ string, _ time.Time) error {
		if rootID == "root-00" {
			return postgres.ErrStateCASConflict
		}
		return nil
	}}
	exec := &fakeExec{}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), p6ValidConfig())
	if err != nil {
		t.Fatalf("stale CAS must not be fatal, got %v", err)
	}
	if res.DueStale != 1 || res.DueAttempted != 2 || res.DueEmitted != 1 {
		t.Fatalf("counts = %+v", res)
	}
	if len(store.emitCalls) != 2 || store.emitCalls[0] != "root-00|/scope-00" || store.emitCalls[1] != "root-01|/scope-01" {
		t.Fatalf("stale watch attempted once, second row still attempted, emits=%v", store.emitCalls)
	}
	if res.StopReason != incrementalorch.StopCompleted {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
}

func TestP6StaleConsumesBudgetNoReplacement(t *testing.T) {
	store := &fakeStore{due: p6Watches(6), emitFn: func(_ context.Context, rootID, _ string, _ time.Time) error {
		if rootID == "root-01" {
			return postgres.ErrStateCASConflict
		}
		return nil
	}}
	exec := &fakeExec{}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), p6ValidConfig())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.DueAttempted != 5 || res.DueStale != 1 || res.DueEmitted != 4 {
		t.Fatalf("counts = %+v", res)
	}
	if len(store.emitCalls) != 5 {
		t.Fatalf("a stale attempt must consume one of five attempts and pull no replacement, emits=%v", store.emitCalls)
	}
	if !res.MoreDueWatches {
		t.Fatal("MoreDueWatches must remain true")
	}
}

func TestP6FatalMaterializationErrorPreservesCommittedAndSkipsP5(t *testing.T) {
	store := &fakeStore{due: p6Watches(3), emitFn: func(_ context.Context, rootID, _ string, _ time.Time) error {
		if rootID == "root-01" {
			return errors.New("store failure")
		}
		return nil
	}}
	exec := &fakeExec{}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), p6ValidConfig())
	if err == nil {
		t.Fatal("a non-CAS materialization error must be fatal")
	}
	if res.StopReason != incrementalorch.StopMaterializationError {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if res.DueAttempted != 2 || res.DueEmitted != 1 {
		t.Fatalf("counts must reflect A committed + B attempted, got %+v", res)
	}
	if len(store.emitCalls) != 2 {
		t.Fatalf("must stop materialization after B, emits=%v", store.emitCalls)
	}
	if res.ExecutorRan || exec.calls != 0 {
		t.Fatal("P5 must not run after a fatal materialization error")
	}
}

func TestP6WallBudgetBeforeNextOperation(t *testing.T) {
	store := &fakeStore{due: p6Watches(3), emitFn: func(_ context.Context, rootID, _ string, _ time.Time) error {
		if rootID == "root-00" {
			time.Sleep(60 * time.Millisecond)
		}
		return nil
	}}
	exec := &fakeExec{}
	cfg := incrementalorch.Config{MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: 25 * time.Millisecond}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), cfg)
	if err != nil {
		t.Fatalf("wall-budget exhaustion is a normal stop, got %v", err)
	}
	if res.StopReason != incrementalorch.StopMaxWallTime {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if len(store.emitCalls) != 1 {
		t.Fatalf("no further Store call may start, emits=%v", store.emitCalls)
	}
	if res.ExecutorRan || exec.calls != 0 {
		t.Fatal("P5 must not run after the wall budget is exhausted")
	}
}

func TestP6WallBudgetInterruptsEmit(t *testing.T) {
	store := &fakeStore{due: p6Watches(2), emitFn: func(ctx context.Context, _ string, _ string, _ time.Time) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	exec := &fakeExec{}
	cfg := incrementalorch.Config{MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: 20 * time.Millisecond}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), cfg)
	if err != nil {
		t.Fatalf("deadline-interrupted materialization is a normal stop, got %v", err)
	}
	if res.StopReason != incrementalorch.StopMaxWallTime {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if !res.MaterializationInterrupted {
		t.Fatal("MaterializationInterrupted must be true")
	}
	if res.DueEmitted != 0 || len(store.emitCalls) != 1 {
		t.Fatalf("interrupted watch must be attempted once and not retried, got %+v (emits=%v)", res, store.emitCalls)
	}
	if res.ExecutorRan || exec.calls != 0 {
		t.Fatal("P5 must not run after an interrupted materialization")
	}
}

func TestP6ParentCancellationDuringMaterialization(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	store := &fakeStore{due: p6Watches(2), emitFn: func(_ context.Context, _ string, _ string, _ time.Time) error {
		cancel()
		return context.Canceled
	}}
	exec := &fakeExec{}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(parent, p6ValidConfig())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want parent context.Canceled, got %v", err)
	}
	if res.StopReason != incrementalorch.StopContextCancelled {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if res.ExecutorRan || exec.calls != 0 {
		t.Fatal("no later phase may begin after parent cancellation")
	}
}

func TestP6ParentCancellationDuringExecution(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	store := &fakeStore{}
	exec := &fakeExec{fn: func(_ context.Context, _ incrementalexec.CycleConfig) (incrementalexec.CycleResult, error) {
		cancel()
		return incrementalexec.CycleResult{}, context.Canceled
	}}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(parent, p6ValidConfig())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want parent context.Canceled, got %v", err)
	}
	if res.StopReason != incrementalorch.StopContextCancelled {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if exec.calls != 1 {
		t.Fatalf("P5 must not run again, calls=%d", exec.calls)
	}
}

func TestP6ExecutorSystemicError(t *testing.T) {
	store := &fakeStore{}
	exec := &fakeExec{err: errors.New("unexpected executor failure")}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), p6ValidConfig())
	if err == nil {
		t.Fatal("a non-context executor error must be propagated")
	}
	if res.StopReason != incrementalorch.StopExecutorError {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if exec.calls != 1 {
		t.Fatalf("P5 must not run a second time, calls=%d", exec.calls)
	}
}

func TestP6ExecutorDeadlineDuringNestedCycle(t *testing.T) {
	store := &fakeStore{}
	exec := &fakeExec{fn: func(ctx context.Context, _ incrementalexec.CycleConfig) (incrementalexec.CycleResult, error) {
		<-ctx.Done()
		return incrementalexec.CycleResult{InterruptedInFlight: true}, fmt.Errorf("%w: %w",
			incrementalexec.ErrCompletionFailed, ctx.Err())
	}}
	cfg := incrementalorch.Config{MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: 20 * time.Millisecond}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), cfg)
	if err != nil {
		t.Fatalf("P6-owned deadline during P5 is a normal bounded stop, got %v", err)
	}
	if res.StopReason != incrementalorch.StopMaxWallTime {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if !res.Executor.InterruptedInFlight {
		t.Fatal("nested P5 InterruptedInFlight must be retained")
	}
}

func TestP6NilP5ErrorButOrchContextExpired(t *testing.T) {
	store := &fakeStore{}
	exec := &fakeExec{fn: func(context.Context, incrementalexec.CycleConfig) (incrementalexec.CycleResult, error) {
		time.Sleep(60 * time.Millisecond)
		return incrementalexec.CycleResult{StopReason: incrementalexec.StopNoEligibleWork}, nil
	}}
	cfg := incrementalorch.Config{MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: 25 * time.Millisecond}
	res, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.StopReason != incrementalorch.StopMaxWallTime {
		t.Fatalf("an expired own deadline must report MAX_WALL_TIME, got %s", res.StopReason)
	}
}

func TestP6ExecutorConfigIsBounded(t *testing.T) {
	store := &fakeStore{}
	exec := &fakeExec{}
	cfg := incrementalorch.Config{MaxDueWatchAttempts: 2, MaxExecuteItems: 3, MaxWallTime: 45 * time.Second}
	if _, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), cfg); err != nil {
		t.Fatalf("run: %v", err)
	}
	if exec.cfg.MaxItems != 3 || exec.cfg.MaxWallTime != 45*time.Second {
		t.Fatalf("nested P5 config = %+v, want MaxItems=3 MaxWallTime=45s", exec.cfg)
	}
}

func TestP6SerialOnly(t *testing.T) {
	var active, maxActive int32
	bump := func(n int32) {
		for {
			m := atomic.LoadInt32(&maxActive)
			if n <= m || atomic.CompareAndSwapInt32(&maxActive, m, n) {
				return
			}
		}
	}
	store := &fakeStore{due: p6Watches(3), emitFn: func(context.Context, string, string, time.Time) error {
		n := atomic.AddInt32(&active, 1)
		bump(n)
		time.Sleep(3 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return nil
	}}
	exec := &fakeExec{fn: func(context.Context, incrementalexec.CycleConfig) (incrementalexec.CycleResult, error) {
		n := atomic.AddInt32(&active, 1)
		bump(n)
		time.Sleep(3 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return incrementalexec.CycleResult{}, nil
	}}
	if _, err := p6Runner(t, store, exec, func() time.Time { return p6Now }).RunCycle(context.Background(), p6ValidConfig()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := atomic.LoadInt32(&maxActive); n != 1 {
		t.Fatalf("orchestration must be strictly serial, max concurrent = %d", n)
	}
}
