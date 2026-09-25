package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalhint"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// fakeRuntime is a controllable P10 runtime for lifecycle tests.
type fakeRuntime struct {
	recovered  int
	recoverErr error

	runErr    error
	block     chan struct{}
	entered   chan struct{}
	exitDelay time.Duration

	wakes  int32
	wakeCh chan struct{}
}

func (f *fakeRuntime) RecoverStartupInflight(context.Context) (int, error) {
	if f.recoverErr != nil {
		return 0, f.recoverErr
	}
	return f.recovered, nil
}

func (f *fakeRuntime) Run(ctx context.Context) error {
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil
		}
	} else if f.runErr == nil {
		// No injected failure: run until cancellation.
		<-ctx.Done()
	}
	if f.runErr != nil {
		return f.runErr
	}
	if f.exitDelay > 0 {
		time.Sleep(f.exitDelay)
	}
	return nil
}

func (f *fakeRuntime) Wake() {
	atomic.AddInt32(&f.wakes, 1)
	if f.wakeCh != nil {
		select {
		case f.wakeCh <- struct{}{}:
		default:
		}
	}
}

func (f *fakeRuntime) wakeCount() int { return int(atomic.LoadInt32(&f.wakes)) }

// fakeCycle is a controllable P6 cycle runner.
type fakeCycle struct {
	mu      sync.Mutex
	calls   int
	block   chan struct{}
	entered chan struct{}
}

func (f *fakeCycle) RunCycle(ctx context.Context, _ incrementalorch.Config) (incrementalorch.Result, error) {
	f.mu.Lock()
	f.calls++
	e, b := f.entered, f.block
	f.mu.Unlock()
	if e != nil {
		select {
		case e <- struct{}{}:
		default:
		}
	}
	if b != nil {
		// Deliberately ignore ctx cancellation: this simulates an in-flight P10
		// execution that must actually finish before the writer lock is released.
		<-b
	}
	return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
}

func (f *fakeCycle) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func p10Cfg(addr string, enabled bool) config.Config {
	c := p9Cfg(addr)
	c.IncrementalRuntimeEnabled = enabled
	c.IncrementalWakeInterval = time.Second
	return c
}

func p10StubCycle(t *testing.T) *fakeCycle {
	t.Helper()
	cycle := &fakeCycle{}
	prev := buildIncrementalCycle
	buildIncrementalCycle = func(*postgres.Store, config.Config, *slog.Logger) (incrementalruntime.CycleRunner, error) {
		return cycle, nil
	}
	t.Cleanup(func() { buildIncrementalCycle = prev })
	return cycle
}

func p10StubRuntime(t *testing.T, rt incrementalRuntime) {
	t.Helper()
	prev := newIncrementalRuntime
	newIncrementalRuntime = func(incrementalruntime.MaintenanceStore, incrementalruntime.CycleRunner, incrementalruntime.Config, *slog.Logger) (incrementalRuntime, error) {
		return rt, nil
	}
	t.Cleanup(func() { newIncrementalRuntime = prev })
}

func p10AdapterConfig(baseURL string) []byte {
	b, _ := json.Marshal(map[string]string{"base_url": baseURL, "path": "/"})
	return b
}

func newP10CountingHandler(refresh *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Refresh bool `json:"refresh"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Refresh {
			atomic.AddInt32(refresh, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"content": []any{}, "total": 0},
		})
	}
}

func p10SeedWatch(t *testing.T, st *postgres.Store, ctx context.Context, rootID, scopeKey string, dueAt time.Time) {
	t.Helper()
	interval := int64(120)
	d := dueAt
	if _, err := st.CreateWatch(ctx, state.ScopeWatchState{
		RootID: rootID, ScopeKey: scopeKey, WatchState: state.WatchHot,
		CadenceClass: "HOT_120", EffectiveIntervalSeconds: &interval,
		SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy},
		Priority:  state.PriorityNormal, NextDueAt: &d,
	}); err != nil {
		t.Fatalf("create watch: %v", err)
	}
}

func TestP10DisabledRegression(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)

	builds := 0
	prev := buildIncrementalCycle
	buildIncrementalCycle = func(*postgres.Store, config.Config, *slog.Logger) (incrementalruntime.CycleRunner, error) {
		builds++
		return nil, errors.New("must not build a runtime when disabled")
	}
	t.Cleanup(func() { buildIncrementalCycle = prev })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, p10Cfg("", false), p9Logger(), pool, st, p9BlockingWorker) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("disabled P10 serve must behave like P9: %v", err)
	}
	if builds != 0 {
		t.Fatal("disabled P10 must not construct an incremental chain")
	}
}

func TestP10StartupRecoveryBeforeHintExposure(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()
	p9SeedRoot(t, st, ctx)

	// Persist one IN_FLIGHT row (crash residue).
	ing, err := incrementalhint.New(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ing.IngestOne(ctx, incrementalhint.Request{RootID: p9RootID, ScopeKey: "/a", Reason: state.ReasonPossibleChange}); err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWork(ctx, p9RootID, "/a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimWork(ctx, p9RootID, "/a", w.Version, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetWork(ctx, p9RootID, "/a"); got.WorkState != state.WorkInFlight {
		t.Fatalf("precondition: work state = %s, want IN_FLIGHT", got.WorkState)
	}

	p10StubCycle(t) // no provider execution during this test

	addr := p9FreeAddr(t)
	cfg := p10Cfg(addr, true)
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()
	p9WaitHint(t, addr) // Hint becomes reachable only after startup recovery

	recovered, err := st.GetWork(ctx, p9RootID, "/a")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.WorkState != state.WorkPending {
		t.Fatalf("startup recovery must requeue IN_FLIGHT before Hint exposure, got %s", recovered.WorkState)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve shutdown: %v", err)
	}
}

func TestP10StartupRecoveryErrorFailsClosed(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	rt := &fakeRuntime{recoverErr: errors.New("recovery failure")}
	p10StubRuntime(t, rt)

	addr := p9FreeAddr(t)
	err := runServe(context.Background(), p10Cfg(addr, true), p9Logger(), pool, st, p9BlockingWorker)
	if err == nil {
		t.Fatal("startup recovery error must fail serve closed")
	}
	if c, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond); derr == nil {
		_ = c.Close()
		t.Fatal("Hint listener must not be exposed when startup recovery fails")
	}
}

func TestP10RuntimeFatalIsFatalToServe(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	rt := &fakeRuntime{runErr: errors.New("MATERIALIZATION_ERROR")}
	p10StubRuntime(t, rt)

	err := runServe(context.Background(), p10Cfg(p9FreeAddr(t), true), p9Logger(), pool, st, p9BlockingWorker)
	if err == nil {
		t.Fatal("an enabled P10 runtime failure must be fatal to serve")
	}
}

func TestP10StartupFailureJoinsRuntimeAndWorker(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	qLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer qLn.Close()

	// Both write-capable actors take time to stop after cancellation.
	rt := &fakeRuntime{exitDelay: 400 * time.Millisecond}
	p10StubRuntime(t, rt)

	cfg := p10Cfg(p9FreeAddr(t), true)
	cfg.HTTPAddr = qLn.Addr().String() // Query bind fails

	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, cfg, p9Logger(), pool, st, p9DelayedExitWorker(400*time.Millisecond))
	}()

	time.Sleep(150 * time.Millisecond)
	if l, lerr := postgres.New(testutil.Pool(t)).AcquireWriterLock(ctx); lerr == nil {
		l.Release(ctx)
		t.Fatal("writer lock must remain held while a started write-capable actor is alive")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("query bind failure must fail serve")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not return after both actors stopped")
	}

	l, lerr := postgres.New(testutil.Pool(t)).AcquireWriterLock(ctx)
	if lerr != nil {
		t.Fatalf("writer lock must be acquirable after both actors stopped: %v", lerr)
	}
	l.Release(ctx)
}

func TestP10ShutdownHoldsLockWhileRuntimeActive(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	cycle := &fakeCycle{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	prev := buildIncrementalCycle
	buildIncrementalCycle = func(*postgres.Store, config.Config, *slog.Logger) (incrementalruntime.CycleRunner, error) {
		return cycle, nil
	}
	t.Cleanup(func() { buildIncrementalCycle = prev })

	cfg := p10Cfg("", true) // runtime enabled, Hint disabled
	gctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()

	select {
	case <-cycle.entered:
	case <-time.After(3 * time.Second):
		select {
		case err := <-done:
			t.Fatalf("runServe exited before the first cycle: %v", err)
		default:
			t.Fatal("P10 cycle never started")
		}
	}

	cancel()
	time.Sleep(150 * time.Millisecond)
	if l, lerr := postgres.New(testutil.Pool(t)).AcquireWriterLock(ctx); lerr == nil {
		l.Release(ctx)
		t.Fatal("writer lock must be held while P10 execution is active")
	}

	close(cycle.block)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not finish shutdown after the cycle released")
	}

	l, lerr := postgres.New(testutil.Pool(t)).AcquireWriterLock(ctx)
	if lerr != nil {
		t.Fatalf("writer lock must be released after P10 stopped: %v", lerr)
	}
	l.Release(ctx)
}

func TestP10HintWakeExecutesBeforeLongTimer(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()
	p9SeedRoot(t, st, ctx)

	// Long timer so only the Hint wake can drive the cycle.
	cycle := p10StubCycle(t)
	cfg := p10Cfg(p9FreeAddr(t), true)
	cfg.IncrementalWakeInterval = 60 * time.Second
	cfg.HintToken = p9Token

	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()
	p9WaitHint(t, cfg.HintAddr)

	// Give the runtime a chance to run its startup cycle first.
	time.Sleep(200 * time.Millisecond)
	before := cycle.callCount()

	code, body := p9PostHint(cfg.HintAddr, p9Token, p9ValidHintBody())
	if code != 202 {
		t.Fatalf("hint POST status = %d (%s)", code, body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for cycle.callCount() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if cycle.callCount() == before {
		t.Fatal("hint wake must drive a P6 cycle before the 60s timer")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve shutdown: %v", err)
	}
}

func TestP10TimerWakeZeroProviderWhenNotDue(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	// Provider-facing mock that must never be hit.
	var refresh int32
	srv := httptest.NewServer(newP10CountingHandler(&refresh))
	defer srv.Close()

	p9SeedRoot(t, st, ctx)
	if err := st.UpsertAdapterConfig(ctx, p9RootID, postgres.AdapterConfig{
		CollectorKind: "alist",
		Config:        p10AdapterConfig(srv.URL),
	}); err != nil {
		t.Fatal(err)
	}
	// Watch is not due for another hour.
	p10SeedWatch(t, st, ctx, p9RootID, "/", time.Now().UTC().Add(time.Hour))

	cfg := p10Cfg("", true)
	cfg.IncrementalWakeInterval = time.Second

	gctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()
	time.Sleep(2500 * time.Millisecond) // multiple timer wakes
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve shutdown: %v", err)
	}
	if got := atomic.LoadInt32(&refresh); got != 0 {
		t.Fatalf("timer wakes must not touch the provider for a not-yet-due watch, got %d refreshes", got)
	}
}

func TestP10P7ExcludedByWriterLock(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	p10StubCycle(t)
	cfg := p10Cfg("", true)

	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()
	time.Sleep(200 * time.Millisecond)

	// While the P10-enabled serve owns the writer lock, a manual P7 run must fail
	// with the existing writer-lock error.
	err := RunIncremental(ctx, p7BaseConfig(), p9Logger(), nil)
	if !errors.Is(err, postgres.ErrWriterLockHeld) {
		t.Fatalf("manual incremental run must fail with ErrWriterLockHeld, got %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve shutdown: %v", err)
	}
}
