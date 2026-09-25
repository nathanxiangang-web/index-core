package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalhint"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/hintapi"
	"github.com/nathanxiangang-web/index-core/testutil"
)

const (
	p9Token  = "0123456789abcdef0123456789abcdef"
	p9RootID = "22222222-2222-4222-8222-222222222222"
)

func p9Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func p9Schema(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

func p9Cfg(addr string) config.Config {
	c := config.Defaults()
	c.DatabaseURL = testutil.TestDSN()
	c.HTTPAddr = "127.0.0.1:0"
	c.HintAddr = addr
	c.HintToken = p9Token
	c.ShutdownTimeout = 500 * time.Millisecond
	return c
}

func p9BlockingWorker(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func p9FreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func p9WaitHint(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hint listener %s never became available", addr)
}

func p9PostHint(addr, token string, body []byte) (int, string) {
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+hintapi.HintPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func p9ValidHintBody() []byte {
	return []byte(`{"root_id":"` + p9RootID + `","scope_key":"/a","reason":"POSSIBLE_CHANGE"}`)
}

func p9SeedRoot(t *testing.T, st *postgres.Store, ctx context.Context) {
	t.Helper()
	if err := st.CreateRoot(ctx, st.Pool(), p9RootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
}

type p9FakeIngester struct{}

func (p9FakeIngester) IngestOne(context.Context, incrementalhint.Request) (state.DirtyScopeWork, error) {
	return state.DirtyScopeWork{}, nil
}

type p9BlockingIngester struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *p9BlockingIngester) IngestOne(ctx context.Context, req incrementalhint.Request) (state.DirtyScopeWork, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return state.DirtyScopeWork{RootID: req.RootID, ScopeKey: req.ScopeKey, WorkState: state.WorkPending, SignalSeq: 1}, nil
	case <-ctx.Done():
		return state.DirtyScopeWork{}, ctx.Err()
	}
}

type p9FailingTransport struct{}

func (p9FailingTransport) Serve(net.Listener) error       { return errors.New("injected hint failure") }
func (p9FailingTransport) Shutdown(context.Context) error { return nil }
func (p9FailingTransport) WaitHandlers()                  {}

func TestP9DisabledTransportRegression(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, p9Cfg(""), p9Logger(), pool, st, p9BlockingWorker) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("disabled hint transport must keep serve unchanged: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not shut down")
	}
}

func TestP9HintServeRequiresWriterLockBeforeBind(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	holder := postgres.New(testutil.Pool(t))
	lock, err := holder.AcquireWriterLock(ctx)
	if err != nil {
		t.Fatalf("acquire holder lock: %v", err)
	}
	defer lock.Release(ctx)

	addr := p9FreeAddr(t)
	err = runServe(ctx, p9Cfg(addr), p9Logger(), pool, st, p9BlockingWorker)
	if !errors.Is(err, postgres.ErrWriterLockHeld) {
		t.Fatalf("want ErrWriterLockHeld, got %v", err)
	}
	if c, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond); derr == nil {
		_ = c.Close()
		t.Fatal("hint address must not be bound without writer ownership")
	}
}

func TestP9HintBindFailureFailsClosed(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	addr := p9FreeAddr(t)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	err = runServe(context.Background(), p9Cfg(addr), p9Logger(), pool, st, p9BlockingWorker)
	if err == nil {
		t.Fatal("an occupied hint port must fail serve")
	}
	if !strings.Contains(err.Error(), "bind hint listener") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestP9UnexpectedHintFailureIsFatal(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)

	prevIng, prevTr := newHintIngester, newHintTransport
	newHintIngester = func(*postgres.Store) (hintapi.Ingester, error) { return p9FakeIngester{}, nil }
	newHintTransport = func(hintapi.Deps) (hintTransport, error) { return p9FailingTransport{}, nil }
	t.Cleanup(func() { newHintIngester, newHintTransport = prevIng, prevTr })

	err := runServe(context.Background(), p9Cfg(p9FreeAddr(t)), p9Logger(), pool, st, p9BlockingWorker)
	if err == nil {
		t.Fatal("an unexpected hint server failure must be fatal")
	}
	if !strings.Contains(err.Error(), "hint transport") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestP9HintIngestPersistsWithoutExecution(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()
	p9SeedRoot(t, st, ctx)

	addr := p9FreeAddr(t)
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, p9Cfg(addr), p9Logger(), pool, st, p9BlockingWorker) }()
	p9WaitHint(t, addr)

	body := p9ValidHintBody()
	for i := 0; i < 2; i++ {
		code, resp := p9PostHint(addr, p9Token, body)
		if code != http.StatusAccepted {
			t.Fatalf("request %d status = %d (%s)", i, code, resp)
		}
	}

	w, err := st.GetWork(ctx, p9RootID, "/a")
	if err != nil {
		t.Fatal(err)
	}
	if w.WorkState != state.WorkPending || w.SignalSeq != 2 {
		t.Fatalf("work = %+v, want PENDING with signal_seq 2", w)
	}
	var rows int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM index_dirty_scope_work WHERE root_id=$1::uuid`, p9RootID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("duplicate HTTP hints must coalesce into one row, got %d", rows)
	}
	var canon int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM index_canonical_resource WHERE root_id=$1::uuid`, p9RootID).Scan(&canon); err != nil {
		t.Fatal(err)
	}
	if canon != 0 {
		t.Fatalf("202 must not mutate Canonical, got %d resources", canon)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not shut down")
	}
}

func TestP9ShutdownHoldsWriterLockUntilHandlersFinish(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()
	p9SeedRoot(t, st, ctx)

	ing := &p9BlockingIngester{entered: make(chan struct{}), release: make(chan struct{})}
	prevIng := newHintIngester
	newHintIngester = func(*postgres.Store) (hintapi.Ingester, error) { return ing, nil }
	t.Cleanup(func() { newHintIngester = prevIng })

	addr := p9FreeAddr(t)
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(gctx, p9Cfg(addr), p9Logger(), pool, st, p9BlockingWorker) }()
	p9WaitHint(t, addr)

	reqDone := make(chan int, 1)
	go func() {
		code, _ := p9PostHint(addr, p9Token, p9ValidHintBody())
		reqDone <- code
	}()
	<-ing.entered

	// Trigger shutdown while the hint handler is still active.
	cancel()
	time.Sleep(100 * time.Millisecond)

	// The writer lock must still be held while a hint handler may call P8.
	if l, err := postgres.New(testutil.Pool(t)).AcquireWriterLock(ctx); err == nil {
		l.Release(ctx)
		t.Fatal("writer lock must be held while a hint handler is active")
	} else if !errors.Is(err, postgres.ErrWriterLockHeld) {
		t.Fatalf("unexpected lock error: %v", err)
	}

	close(ing.release)
	if code := <-reqDone; code != http.StatusAccepted {
		t.Fatalf("blocked hint request status = %d", code)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not finish shutdown after the handler returned")
	}

	l, err := postgres.New(testutil.Pool(t)).AcquireWriterLock(ctx)
	if err != nil {
		t.Fatalf("writer lock must be released after in-flight handlers finish: %v", err)
	}
	l.Release(ctx)
}

// TestP9QueryBindFailurePreventsHintExposure proves the startup order: the Query
// listener binds first, so a Query bind failure can never leave a reachable Hint
// listener.
func TestP9QueryBindFailurePreventsHintExposure(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)

	qLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer qLn.Close()
	hintAddr := p9FreeAddr(t)

	cfg := p9Cfg(hintAddr)
	cfg.HTTPAddr = qLn.Addr().String()

	err = runServe(context.Background(), cfg, p9Logger(), pool, st, p9BlockingWorker)
	if err == nil {
		t.Fatal("an occupied query port must fail serve")
	}
	if !strings.Contains(err.Error(), "bind http listener") {
		t.Fatalf("unexpected error: %v", err)
	}
	if c, derr := net.DialTimeout("tcp", hintAddr, 200*time.Millisecond); derr == nil {
		_ = c.Close()
		t.Fatal("hint listener must not be exposed when the query bind fails")
	}
}

// p9DelayedExitWorker honors cancellation but keeps running for d afterward,
// modelling a worker that takes time to stop.
func p9DelayedExitWorker(d time.Duration) workerRunner {
	return func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(d)
		return nil
	}
}

func p9AssertLockHeldThenReleased(t *testing.T, ctx context.Context, done <-chan error, wantErr string) {
	t.Helper()
	// Startup already failed, but the cancellation-resistant worker is still
	// running, so the writer lock must remain held.
	time.Sleep(150 * time.Millisecond)
	if l, lerr := postgres.New(testutil.Pool(t)).AcquireWriterLock(ctx); lerr == nil {
		l.Release(ctx)
		t.Fatal("writer lock must not be released while the started worker is still running")
	} else if !errors.Is(lerr, postgres.ErrWriterLockHeld) {
		t.Fatalf("unexpected lock error: %v", lerr)
	}

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("expected %q error, got %v", wantErr, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not return after the worker stopped")
	}

	l, lerr := postgres.New(testutil.Pool(t)).AcquireWriterLock(ctx)
	if lerr != nil {
		t.Fatalf("writer lock must be released after the worker stops: %v", lerr)
	}
	l.Release(ctx)
}

// TestP9QueryBindFailureWaitsForWorkerBeforeLockRelease proves a startup failure
// after the worker started never releases the writer lock until the worker
// actually stops (G3-R2.4).
func TestP9QueryBindFailureWaitsForWorkerBeforeLockRelease(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	qLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer qLn.Close()

	cfg := p9Cfg(p9FreeAddr(t))
	cfg.HTTPAddr = qLn.Addr().String()

	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, cfg, p9Logger(), pool, st, p9DelayedExitWorker(400*time.Millisecond))
	}()
	p9AssertLockHeldThenReleased(t, ctx, done, "bind http listener")
}

// TestP9HintBindFailureWaitsForWorkerBeforeLockRelease proves the same invariant
// after the Query bind succeeds but the Hint bind fails.
func TestP9HintBindFailureWaitsForWorkerBeforeLockRelease(t *testing.T) {
	pool := p9Schema(t)
	st := postgres.New(pool)
	ctx := context.Background()

	hintLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hintLn.Close()

	cfg := p9Cfg(hintLn.Addr().String()) // occupied hint port; query binds fine

	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, cfg, p9Logger(), pool, st, p9DelayedExitWorker(400*time.Millisecond))
	}()
	p9AssertLockHeldThenReleased(t, ctx, done, "bind hint listener")
}
