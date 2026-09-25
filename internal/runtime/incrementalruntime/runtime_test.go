package incrementalruntime_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

type fakeStore struct {
	mu sync.Mutex

	due      []state.DirtyScopeWork
	dueErr   error
	dueLimit int

	retryErrFn func(rootID, scopeKey string) error
	retryCalls []string

	inflightSeq  [][]string
	inflightIdx  int
	inflightErr  error
	recoverCalls []string
	recoverTotal int
	recoverErr   error
}

func (f *fakeStore) ListDueRetryWork(_ context.Context, _ time.Time, limit int) ([]state.DirtyScopeWork, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dueLimit = limit
	if f.dueErr != nil {
		return nil, f.dueErr
	}
	out := append([]state.DirtyScopeWork(nil), f.due...)
	f.due = nil // candidates are consumed once, mirroring promoted rows leaving RETRY_WAIT
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) RetryReady(_ context.Context, rootID, scopeKey string, _ int64, _ time.Time) (state.DirtyScopeWork, error) {
	f.mu.Lock()
	f.retryCalls = append(f.retryCalls, rootID+"|"+scopeKey)
	fn := f.retryErrFn
	f.mu.Unlock()
	if fn != nil {
		if err := fn(rootID, scopeKey); err != nil {
			return state.DirtyScopeWork{}, err
		}
	}
	return state.DirtyScopeWork{RootID: rootID, ScopeKey: scopeKey, WorkState: state.WorkPending}, nil
}

func (f *fakeStore) ListInflightRoots(_ context.Context, _ int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inflightErr != nil {
		return nil, f.inflightErr
	}
	if f.inflightIdx >= len(f.inflightSeq) {
		return nil, nil
	}
	out := f.inflightSeq[f.inflightIdx]
	f.inflightIdx++
	return out, nil
}

func (f *fakeStore) RecoverStaleInflight(_ context.Context, rootID string, _ time.Time) (int, error) {
	f.mu.Lock()
	f.recoverCalls = append(f.recoverCalls, rootID)
	total, err := f.recoverTotal, f.recoverErr
	f.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return total, nil
}

func (f *fakeStore) retryCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.retryCalls)
}

func (f *fakeStore) dueLimitValue() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dueLimit
}

type fakeCycle struct {
	mu sync.Mutex

	calls     int
	results   []incrementalorch.Result
	errs      []error
	fn        func(call int, ctx context.Context) (incrementalorch.Result, error)
	block     chan struct{}
	entered   chan struct{}
	active    int
	maxActive int
}

func (f *fakeCycle) RunCycle(ctx context.Context, _ incrementalorch.Config) (incrementalorch.Result, error) {
	f.mu.Lock()
	i := f.calls
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	fn, block, entered := f.fn, f.block, f.entered
	var res incrementalorch.Result
	var err error
	if i < len(f.results) {
		res = f.results[i]
	}
	if i < len(f.errs) {
		err = f.errs[i]
	}
	f.mu.Unlock()

	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			f.mu.Lock()
			f.active--
			f.mu.Unlock()
			return incrementalorch.Result{}, ctx.Err()
		}
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	if fn != nil {
		return fn(i, ctx)
	}
	return res, err
}

func (f *fakeCycle) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeCycle) maxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

func p10Cfg() incrementalruntime.Config {
	return incrementalruntime.Config{
		WakeInterval:       incrementalruntime.MinWakeInterval,
		MaxRetryPromotions: 5,
		Cycle:              incrementalruntime.DefaultCycleConfig(),
	}
}

func p10New(t *testing.T, store incrementalruntime.MaintenanceStore, cycle incrementalruntime.CycleRunner, cfg incrementalruntime.Config) *incrementalruntime.Runtime {
	t.Helper()
	rt, err := incrementalruntime.New(store, cycle, cfg, nil, nil)
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	return rt
}

func TestP10NewValidatesConfig(t *testing.T) {
	if _, err := incrementalruntime.New(nil, &fakeCycle{}, p10Cfg(), nil, nil); err == nil {
		t.Fatal("nil store must be rejected")
	}
	if _, err := incrementalruntime.New(&fakeStore{}, nil, p10Cfg(), nil, nil); err == nil {
		t.Fatal("nil cycle runner must be rejected")
	}
	bad := p10Cfg()
	bad.WakeInterval = 500 * time.Millisecond
	if _, err := incrementalruntime.New(&fakeStore{}, &fakeCycle{}, bad, nil, nil); err == nil {
		t.Fatal("wake interval below 1s must be rejected")
	}
	bad = p10Cfg()
	bad.WakeInterval = 61 * time.Second
	if _, err := incrementalruntime.New(&fakeStore{}, &fakeCycle{}, bad, nil, nil); err == nil {
		t.Fatal("wake interval above 60s must be rejected")
	}
	bad = p10Cfg()
	bad.MaxRetryPromotions = 0
	if _, err := incrementalruntime.New(&fakeStore{}, &fakeCycle{}, bad, nil, nil); err == nil {
		t.Fatal("zero promotions must be rejected")
	}
	bad = p10Cfg()
	bad.MaxRetryPromotions = 6
	if _, err := incrementalruntime.New(&fakeStore{}, &fakeCycle{}, bad, nil, nil); err == nil {
		t.Fatal("promotions above cap must be rejected")
	}
	bad = p10Cfg()
	bad.Cycle.MaxExecuteItems = 6
	if _, err := incrementalruntime.New(&fakeStore{}, &fakeCycle{}, bad, nil, nil); err == nil {
		t.Fatal("invalid nested P6 config must be rejected")
	}
}

func TestP10StartupRecoveryDrainsInflight(t *testing.T) {
	store := &fakeStore{inflightSeq: [][]string{{"r1", "r2"}, {}}, recoverTotal: 1}
	rt := p10New(t, store, &fakeCycle{}, p10Cfg())
	total, err := rt.RecoverStartupInflight(context.Background())
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if total != 2 {
		t.Fatalf("recovered = %d, want 2", total)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.recoverCalls) != 2 || store.recoverCalls[0] != "r1" || store.recoverCalls[1] != "r2" {
		t.Fatalf("recover calls = %v", store.recoverCalls)
	}
}

func TestP10StartupRecoveryErrorIsReturned(t *testing.T) {
	store := &fakeStore{inflightSeq: [][]string{{"r1"}}, recoverErr: errors.New("db down")}
	rt := p10New(t, store, &fakeCycle{}, p10Cfg())
	if _, err := rt.RecoverStartupInflight(context.Background()); err == nil {
		t.Fatal("startup recovery error must be returned")
	}
}

func TestP10RetryPromotionIsBounded(t *testing.T) {
	store := &fakeStore{due: []state.DirtyScopeWork{
		{RootID: "r1", ScopeKey: "/a", Version: 1},
		{RootID: "r2", ScopeKey: "/b", Version: 1},
		{RootID: "r3", ScopeKey: "/c", Version: 1},
		{RootID: "r4", ScopeKey: "/d", Version: 1},
		{RootID: "r5", ScopeKey: "/e", Version: 1},
		{RootID: "r6", ScopeKey: "/f", Version: 1},
	}}
	cycle := &fakeCycle{fn: func(int, context.Context) (incrementalorch.Result, error) {
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	}}
	rt := p10New(t, store, cycle, p10Cfg())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for store.retryCallCount() < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Give the runtime a moment to (not) attempt a sixth promotion.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	if got := store.retryCallCount(); got != 5 {
		t.Fatalf("retry promotions = %d, want exactly 5", got)
	}
	if store.dueLimitValue() != 6 {
		t.Fatalf("due query limit = %d, want promotions+1 = 6", store.dueLimitValue())
	}
}

func TestP10RetryPromotionSkipsStaleCAS(t *testing.T) {
	store := &fakeStore{due: []state.DirtyScopeWork{
		{RootID: "r1", ScopeKey: "/a", Version: 1},
		{RootID: "r2", ScopeKey: "/b", Version: 1},
		{RootID: "r3", ScopeKey: "/c", Version: 1},
	}}
	store.retryErrFn = func(rootID, _ string) error {
		if rootID == "r2" {
			return postgres.ErrStateCASConflict
		}
		return nil
	}
	cycle := &fakeCycle{fn: func(int, context.Context) (incrementalorch.Result, error) {
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	}}
	rt := p10New(t, store, cycle, p10Cfg())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for store.retryCallCount() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.retryCalls) != 3 {
		t.Fatalf("retry calls = %v, want all three candidates attempted once", store.retryCalls)
	}
	if store.retryCalls[1] != "r2|/b" {
		t.Fatalf("stale candidate must not block later candidates, got %v", store.retryCalls)
	}
}

func TestP10RetryMaintenanceStoreErrorIsFatal(t *testing.T) {
	store := &fakeStore{dueErr: errors.New("selector failure")}
	cycle := &fakeCycle{}
	rt := p10New(t, store, cycle, p10Cfg())
	err := rt.Run(context.Background())
	if err == nil {
		t.Fatal("retry-maintenance Store error must be fatal")
	}
	if cycle.callCount() != 0 {
		t.Fatal("no P6 cycle must run after a fatal maintenance error")
	}
}

func TestP10SystemicCycleErrorIsFatal(t *testing.T) {
	store := &fakeStore{}
	cycle := &fakeCycle{errs: []error{errors.New("MATERIALIZATION_ERROR")}}
	rt := p10New(t, store, cycle, p10Cfg())
	if err := rt.Run(context.Background()); err == nil {
		t.Fatal("systemic P6 error must be fatal")
	}
	if cycle.callCount() != 1 {
		t.Fatalf("cycle calls = %d, want 1", cycle.callCount())
	}
}

func TestP10InterruptedClaimRecoversProvenRootOnce(t *testing.T) {
	store := &fakeStore{}
	cycle := &fakeCycle{results: []incrementalorch.Result{{
		StopReason:  incrementalorch.StopMaxWallTime,
		ExecutorRan: true,
		Executor: incrementalexec.CycleResult{
			StopReason:          incrementalexec.StopMaxWallTime,
			InterruptedInFlight: true,
			Last: incrementalexec.Result{
				Selected: true, RootID: "r-interrupted", ClaimedSignalSeq: 7,
			},
		},
	}}}
	rt := p10New(t, store, cycle, p10Cfg())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		store.mu.Lock()
		n := len(store.recoverCalls)
		store.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.recoverCalls) != 1 || store.recoverCalls[0] != "r-interrupted" {
		t.Fatalf("interrupted recovery calls = %v, want exactly [r-interrupted]", store.recoverCalls)
	}
}

func TestP10InterruptedRecoveryErrorIsFatal(t *testing.T) {
	store := &fakeStore{recoverErr: errors.New("recover failure")}
	cycle := &fakeCycle{results: []incrementalorch.Result{{
		StopReason: incrementalorch.StopMaxWallTime,
		Executor: incrementalexec.CycleResult{
			InterruptedInFlight: true,
			Last:                incrementalexec.Result{Selected: true, RootID: "r1", ClaimedSignalSeq: 3},
		},
	}}}
	rt := p10New(t, store, cycle, p10Cfg())
	if err := rt.Run(context.Background()); err == nil {
		t.Fatal("interrupted-root recovery error must be fatal")
	}
}

func TestP10NeverOverlapsCycles(t *testing.T) {
	store := &fakeStore{}
	block := make(chan struct{})
	cycle := &fakeCycle{
		block:   block,
		entered: make(chan struct{}, 1),
		fn: func(int, context.Context) (incrementalorch.Result, error) {
			return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
		},
	}
	rt := p10New(t, store, cycle, p10Cfg())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	for i := 0; i < 50; i++ {
		rt.Wake()
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	// Unblock any cycle that was waiting.
	close(block)

	if got := cycle.maxConcurrent(); got > 1 {
		t.Fatalf("max concurrent P6 cycles = %d, want <= 1", got)
	}
}

// TestP10StaleSelectionIsNonFatal proves the hybrid Hint/P4 optimistic-CAS race
// never terminates the runtime/serve.
func TestP10StaleSelectionIsNonFatal(t *testing.T) {
	store := &fakeStore{}
	cycle := &fakeCycle{
		results: []incrementalorch.Result{{
			StopReason: incrementalorch.StopExecutorError,
			Executor:   incrementalexec.CycleResult{StopReason: incrementalexec.StopStaleSelection},
		}},
		errs: []error{fmt.Errorf("executor cycle: %w", incrementalexec.ErrStaleSelection)},
	}
	rt := p10New(t, store, cycle, p10Cfg())

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	if err := rt.Run(ctx); err != nil {
		t.Fatalf("STALE_SELECTION must be bounded non-fatal contention, got %v", err)
	}
	if cycle.callCount() == 0 {
		t.Fatal("cycle must have run")
	}
}

// TestP10SystemicErrorStillFatal proves the stale-selection carve-out does not
// loosen INTERNAL/systemic handling.
func TestP10SystemicErrorStillFatal(t *testing.T) {
	store := &fakeStore{}
	cycle := &fakeCycle{
		results: []incrementalorch.Result{{
			StopReason: incrementalorch.StopExecutorError,
			Executor:   incrementalexec.CycleResult{StopReason: incrementalexec.StopInternalItemFailure},
		}},
		errs: []error{fmt.Errorf("executor cycle: %w", incrementalexec.ErrScannerFailed)},
	}
	rt := p10New(t, store, cycle, p10Cfg())
	if err := rt.Run(context.Background()); err == nil {
		t.Fatal("INTERNAL/systemic P6 error must remain fatal")
	}
}

// TestP10BurstCapAppliesToExternalWakes proves a continuous external wake stream
// (Hint/timer) cannot start more than MaxConsecutiveCyclesPerBurst immediately
// contiguous cycles, even though every cycle reports backlog=false.
func TestP10BurstCapAppliesToExternalWakes(t *testing.T) {
	store := &fakeStore{}
	var calls int32
	var rt *incrementalruntime.Runtime
	cycle := &fakeCycle{fn: func(int, context.Context) (incrementalorch.Result, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// Outlive the 1s timer so a timer tick becomes ready during the cycle.
			time.Sleep(1100 * time.Millisecond)
		}
		// Simulate a Hint arriving while the cycle runs.
		rt.Wake()
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	}}
	rt = p10New(t, store, cycle, p10Cfg())

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_ = rt.Run(ctx)

	if got := int(atomic.LoadInt32(&calls)); got > incrementalruntime.MaxConsecutiveCyclesPerBurst {
		t.Fatalf("external wakes bypassed the burst cap: %d contiguous cycles before cooldown", got)
	}
}

// TestP10BurstCooldownThenContinue proves cycles resume after the burst cooldown.
func TestP10BurstCooldownThenContinue(t *testing.T) {
	store := &fakeStore{}
	var calls int32
	var rt *incrementalruntime.Runtime
	cycle := &fakeCycle{fn: func(int, context.Context) (incrementalorch.Result, error) {
		atomic.AddInt32(&calls, 1)
		rt.Wake()
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	}}
	rt = p10New(t, store, cycle, p10Cfg())

	ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer cancel()
	_ = rt.Run(ctx)

	if got := int(atomic.LoadInt32(&calls)); got <= incrementalruntime.MaxConsecutiveCyclesPerBurst {
		t.Fatalf("cycles must resume after the burst cooldown, got %d", got)
	}
}

func TestP10BurstCooldownCapsConsecutiveCycles(t *testing.T) {
	store := &fakeStore{}
	cycle := &fakeCycle{results: []incrementalorch.Result{
		{StopReason: incrementalorch.StopCompleted, MoreDueWatches: true},
		{StopReason: incrementalorch.StopCompleted, MoreDueWatches: true},
		{StopReason: incrementalorch.StopCompleted, MoreDueWatches: true},
		{StopReason: incrementalorch.StopCompleted, MoreDueWatches: true},
		{StopReason: incrementalorch.StopCompleted, MoreDueWatches: true},
		{StopReason: incrementalorch.StopCompleted, MoreDueWatches: true},
	}}
	rt := p10New(t, store, cycle, p10Cfg())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	// A burst of four consecutive cycles must be followed by a >=1s cooldown, so
	// before ~1s at most four cycles can have run.
	time.Sleep(500 * time.Millisecond)
	n := cycle.callCount()
	if n > incrementalruntime.MaxConsecutiveCyclesPerBurst {
		t.Fatalf("cycles before cooldown = %d, want <= %d", n, incrementalruntime.MaxConsecutiveCyclesPerBurst)
	}
	cancel()
	<-done
}

// --- P11 hardening ---

func TestP11StartupRecoveryZeroProgressFailsClosed(t *testing.T) {
	// ListInflightRoots reports a root, but RecoverStaleInflight recovers nothing:
	// the durable state cannot advance, so recovery must fail closed.
	store := &fakeStore{inflightSeq: [][]string{{"r1"}}, recoverTotal: 0}
	rt := p10New(t, store, &fakeCycle{}, p10Cfg())
	total, err := rt.RecoverStartupInflight(context.Background())
	if err == nil {
		t.Fatal("zero-progress startup recovery must fail closed")
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
	if !strings.Contains(err.Error(), "no progress") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestP11StartupRecoveryLogHasStartupPhase(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	store := &fakeStore{inflightSeq: [][]string{{"r1"}, {}}, recoverTotal: 1}
	rt, err := incrementalruntime.New(store, &fakeCycle{}, p10Cfg(), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RecoverStartupInflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "incremental_inflight_recovered") || !strings.Contains(out, `"phase":"startup"`) {
		t.Fatalf("startup recovery log = %s", out)
	}
}

func TestP11RuntimeStructuredLogFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	store := &fakeStore{due: []state.DirtyScopeWork{{RootID: "r1", ScopeKey: "/a", Version: 1}}}
	cycle := &fakeCycle{fn: func(int, context.Context) (incrementalorch.Result, error) {
		return incrementalorch.Result{
			StopReason:     incrementalorch.StopCompleted,
			DueCandidates:  2,
			DueAttempted:   2,
			DueEmitted:     1,
			MoreDueWatches: true,
			ExecutorRan:    true,
			Executor:       incrementalexec.CycleResult{SelectedItems: 1, Succeeded: 1, Failed: 0},
		}, nil
	}}
	rt, err := incrementalruntime.New(store, cycle, p10Cfg(), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1300*time.Millisecond)
	defer cancel()
	_ = rt.Run(ctx)

	out := buf.String()
	for _, want := range []string{
		"incremental_runtime_wake", "wake_reason", "burst_count",
		"incremental_retry_promotion", "candidates", "attempted", "promoted", "stale", "more",
		"incremental_cycle_finished", "stop_reason", "duration_ms",
		"due_candidates", "due_attempted", "due_emitted", "more_due_watches",
		"selected_items", "succeeded", "failed", "interrupted_in_flight", "backlog",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("structured log missing %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{"supersecret", "INDEXCORE_HINT_TOKEN", "postgres://"} {
		if strings.Contains(out, secret) {
			t.Fatalf("structured log leaked %q:\n%s", secret, out)
		}
	}
}

func TestP11RuntimeFatalLogHasPhase(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	cycle := &fakeCycle{errs: []error{errors.New("MATERIALIZATION_ERROR")}}
	rt, err := incrementalruntime.New(&fakeStore{}, cycle, p10Cfg(), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Run(context.Background()); err == nil {
		t.Fatal("systemic cycle error must be fatal")
	}
	out := buf.String()
	if !strings.Contains(out, "incremental_runtime_fatal") || !strings.Contains(out, `"phase"`) {
		t.Fatalf("fatal log missing phase/error_class:\n%s", out)
	}
}

// --- P11 Round 1 rework ---

// TestP11TimerExpiryIsOneCycle proves one timer expiry starts exactly one cycle
// (no phantom immediate cycle from the wake-reason plumbing).
func TestP11TimerExpiryIsOneCycle(t *testing.T) {
	store := &fakeStore{}
	cycle := &fakeCycle{fn: func(int, context.Context) (incrementalorch.Result, error) {
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	}}
	rt := p10New(t, store, cycle, p10Cfg()) // wake interval = 1s

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_ = rt.Run(ctx)

	if got := cycle.callCount(); got != 2 {
		t.Fatalf("startup + one timer expiry must run exactly 2 cycles, got %d", got)
	}
}

// TestP11RepeatedTimersRemainOneCycleEach proves each timer expiry maps to one
// cycle, with the wake reason coalesced to timer.
func TestP11RepeatedTimersRemainOneCycleEach(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	store := &fakeStore{}
	cycle := &fakeCycle{fn: func(int, context.Context) (incrementalorch.Result, error) {
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	}}
	rt, err := incrementalruntime.New(store, cycle, p10Cfg(), logger, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	_ = rt.Run(ctx)

	if got := cycle.callCount(); got != 3 {
		t.Fatalf("startup + two timer expiries must run exactly 3 cycles, got %d", got)
	}
	if !strings.Contains(buf.String(), `"wake_reason":"timer"`) {
		t.Fatalf("timer wake reason must be recorded:\n%s", buf.String())
	}
}

func TestP11StartupRecoveryNonProgressFatalLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	store := &fakeStore{inflightSeq: [][]string{{"r1"}}, recoverTotal: 0}
	rt, err := incrementalruntime.New(store, &fakeCycle{}, p10Cfg(), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RecoverStartupInflight(context.Background()); err == nil {
		t.Fatal("zero-progress recovery must fail")
	}
	out := buf.String()
	if !strings.Contains(out, "incremental_runtime_fatal") ||
		!strings.Contains(out, `"phase":"startup_recovery"`) ||
		!strings.Contains(out, `"error_class":"no_progress"`) {
		t.Fatalf("non-progress fatal log missing structured phase/class:\n%s", out)
	}
}

func TestP11StartupRecoveryStoreErrorFatalLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	store := &fakeStore{inflightErr: errors.New("postgres://user:supersecret@db/x")}
	rt, err := incrementalruntime.New(store, &fakeCycle{}, p10Cfg(), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RecoverStartupInflight(context.Background()); err == nil {
		t.Fatal("store error must fail recovery")
	}
	out := buf.String()
	if !strings.Contains(out, `"phase":"startup_recovery"`) || !strings.Contains(out, `"error_class":"store"`) {
		t.Fatalf("startup store fatal log missing phase/class:\n%s", out)
	}
	if strings.Contains(out, "supersecret") || strings.Contains(out, "postgres://") {
		t.Fatalf("fatal log must not leak raw DB material:\n%s", out)
	}
}

func TestP11SystemicCycleFatalLogPhase(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	cycle := &fakeCycle{errs: []error{errors.New("MATERIALIZATION_ERROR postgres://user:supersecret@db/x")}}
	rt, err := incrementalruntime.New(&fakeStore{}, cycle, p10Cfg(), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Run(context.Background()); err == nil {
		t.Fatal("systemic cycle error must be fatal")
	}
	out := buf.String()
	if !strings.Contains(out, `"phase":"cycle"`) || !strings.Contains(out, `"error_class":"executor"`) {
		t.Fatalf("cycle fatal log missing phase/class:\n%s", out)
	}
	if strings.Contains(out, "supersecret") || strings.Contains(out, "postgres://") {
		t.Fatalf("fatal log must not leak raw error material:\n%s", out)
	}
}

func TestP11RetryMaintenanceFatalLogPhase(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	store := &fakeStore{dueErr: errors.New("selector failure supersecret")}
	rt, err := incrementalruntime.New(store, &fakeCycle{}, p10Cfg(), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Run(context.Background()); err == nil {
		t.Fatal("retry maintenance error must be fatal")
	}
	out := buf.String()
	if !strings.Contains(out, `"phase":"retry_maintenance"`) || !strings.Contains(out, `"error_class":"store"`) {
		t.Fatalf("retry maintenance fatal log missing phase/class:\n%s", out)
	}
	if strings.Contains(out, "supersecret") {
		t.Fatalf("fatal log must not leak raw error material:\n%s", out)
	}
}

func TestP11InterruptedRecoveryFatalLogPhase(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	store := &fakeStore{recoverErr: errors.New("recover failure supersecret")}
	cycle := &fakeCycle{results: []incrementalorch.Result{{
		StopReason: incrementalorch.StopMaxWallTime,
		Executor: incrementalexec.CycleResult{
			InterruptedInFlight: true,
			Last:                incrementalexec.Result{Selected: true, RootID: "r1", ClaimedSignalSeq: 3},
		},
	}}}
	rt, err := incrementalruntime.New(store, cycle, p10Cfg(), logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Run(context.Background()); err == nil {
		t.Fatal("interrupted-root recovery error must be fatal")
	}
	out := buf.String()
	if !strings.Contains(out, `"phase":"interrupted_recovery"`) || !strings.Contains(out, `"error_class":"store"`) {
		t.Fatalf("interrupted recovery fatal log missing phase/class:\n%s", out)
	}
	if strings.Contains(out, "supersecret") {
		t.Fatalf("fatal log must not leak raw error material:\n%s", out)
	}
}
