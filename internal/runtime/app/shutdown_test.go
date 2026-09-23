package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// G3-R2.4: the writer lock must be held until the worker goroutine has actually
// stopped; a second acquire must fail while the worker is in-flight, and succeed
// only after shutdown completes.
func TestServeJoinsWorkerBeforeReleasingWriterLock(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	cfg := config.Config{DatabaseURL: testutil.TestDSN(), HTTPAddr: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	runCtx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		runErr <- runServe(runCtx, cfg, logger, pool, st, func(wctx context.Context) error {
			close(started)
			<-wctx.Done()
			return nil
		})
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := st.AcquireWriterLock(ctx); err == nil {
		t.Fatal("writer lock must NOT be released while the worker is in-flight (G3-R2.4)")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not return after shutdown")
	}

	l, err := st.AcquireWriterLock(ctx)
	if err != nil {
		t.Fatalf("writer lock must be released after full shutdown: %v", err)
	}
	l.Release(ctx)
}

// G3-R2.4 (timeout path): if the worker is still running after the graceful
// shutdown timeout, the writer lock MUST NOT be released early. runServe keeps
// holding the lock (and keeps waiting) until the worker actually stops.
func TestServeHoldsWriterLockWhenWorkerExceedsShutdownTimeout(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	cfg := config.Config{DatabaseURL: testutil.TestDSN(), HTTPAddr: "127.0.0.1:0", ShutdownTimeout: 50 * time.Millisecond}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	runCtx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopping := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		runErr <- runServe(runCtx, cfg, logger, pool, st, func(wctx context.Context) error {
			close(started)
			<-wctx.Done()
			close(stopping)
			// Keep running past the graceful timeout before actually stopping.
			time.Sleep(400 * time.Millisecond)
			return nil
		})
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case <-stopping:
	case <-time.After(5 * time.Second):
		t.Fatal("worker was not asked to stop")
	}
	// The shutdown timeout has elapsed while the worker is still alive: the lock
	// must still be held, so another daemon cannot acquire single-writer ownership.
	time.Sleep(150 * time.Millisecond)
	if _, err := st.AcquireWriterLock(ctx); err == nil {
		t.Fatal("writer lock must NOT be released while the worker outlives the shutdown timeout (G3-R2.4)")
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not return after the worker stopped")
	}
	l, err := st.AcquireWriterLock(ctx)
	if err != nil {
		t.Fatalf("writer lock must be released after the worker truly stops: %v", err)
	}
	l.Release(ctx)
}
