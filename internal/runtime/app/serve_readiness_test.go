package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/hintapi"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func p11GetReadyz(addr string) int {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/readyz", nil)
	if err != nil {
		return 0
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func p11WaitReadyz(t *testing.T, addr string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := 0
	for time.Now().Before(deadline) {
		last = p11GetReadyz(addr)
		if last == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("readyz did not reach %d, last=%d", want, last)
}

// TestP11ReadyzSteadyState proves /readyz is 200 only after full startup.
func TestP11ReadyzSteadyState(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	cfg := p10Cfg("", true)
	cfg.HTTPAddr = p9FreeAddr(t)
	p10StubCycle(t)

	gctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()

	p11WaitReadyz(t, cfg.HTTPAddr, http.StatusOK, 5*time.Second)

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve shutdown: %v", err)
	}
}

// TestP11ReadyzFlipsBeforeLongShutdownDrain proves readiness drops to 503 before
// the long actor join, while Query stays reachable and the writer lock is held.
func TestP11ReadyzFlipsBeforeLongShutdownDrain(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	cfg := p10Cfg("", true)
	cfg.HTTPAddr = p9FreeAddr(t)

	cycle := &fakeCycle{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	prev := buildIncrementalCycle
	buildIncrementalCycle = func(*postgres.Store, config.Config, *slog.Logger) (incrementalruntime.CycleRunner, error) {
		return cycle, nil
	}
	t.Cleanup(func() { buildIncrementalCycle = prev })

	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()
	p11WaitReadyz(t, cfg.HTTPAddr, http.StatusOK, 5*time.Second)

	select {
	case <-cycle.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("P10 cycle never started")
	}

	cancel()
	// Query may still answer during the drain window, but readiness must be 503.
	p11WaitReadyz(t, cfg.HTTPAddr, http.StatusServiceUnavailable, 3*time.Second)

	if l, err := postgres.New(testutil.Pool(t)).AcquireWriterLock(ctx); err == nil {
		l.Release(ctx)
		t.Fatal("writer lock must stay held while P10 is still draining")
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

	l, err := postgres.New(testutil.Pool(t)).AcquireWriterLock(ctx)
	if err != nil {
		t.Fatalf("writer lock must be released after the drain: %v", err)
	}
	l.Release(ctx)
}

// TestP11ReadyzFalseOnFatalBeforeCleanup proves a fatal runtime path flips
// readiness false before the long cleanup/join completes.
func TestP11ReadyzFalseOnFatalBeforeCleanup(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()
	p9SeedRoot(t, st, ctx)

	cfg := p10Cfg(p9FreeAddr(t), true) // Hint enabled
	cfg.HTTPAddr = p9FreeAddr(t)
	p10StubCycle(t)

	rt := &fakeRuntime{block: make(chan struct{}), runErr: errors.New("MATERIALIZATION_ERROR")}
	p10StubRuntime(t, rt)

	ing := &p9BlockingIngester{entered: make(chan struct{}), release: make(chan struct{})}
	prevIng := newHintIngester
	newHintIngester = func(*postgres.Store) (hintapi.Ingester, error) { return ing, nil }
	t.Cleanup(func() { newHintIngester = prevIng })

	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, cfg, p9Logger(), pool, st, p9BlockingWorker) }()
	p11WaitReadyz(t, cfg.HTTPAddr, http.StatusOK, 5*time.Second)

	// Occupy a Hint handler so the fatal cleanup must block on it.
	go func() {
		p9PostHint(cfg.HintAddr, p9Token,
			[]byte(`{"root_id":"`+p9RootID+`","scope_key":"/","reason":"POSSIBLE_CHANGE"}`))
	}()
	select {
	case <-ing.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("hint handler never started")
	}

	// Make the runtime fatal; readiness must flip before the blocked cleanup ends.
	close(rt.block)
	p11WaitReadyz(t, cfg.HTTPAddr, http.StatusServiceUnavailable, 3*time.Second)

	close(ing.release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("fatal runtime path must fail serve")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not finish shutdown after the handler released")
	}
}
