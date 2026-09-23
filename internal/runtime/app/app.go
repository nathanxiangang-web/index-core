// Package app implements the indexcore runtime commands. It wires configuration
// into the Store/Kernel/Query layers but does not redefine Domain or bypass the
// Coordinator.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/runtime/version"
	"github.com/nathanxiangang-web/index-core/internal/runtime/worker"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/httpapi"
)

// Migrate applies SQL-first migrations explicitly (no auto-migrate in serve).
func Migrate(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	state, err := postgres.SchemaStatus(ctx, pool)
	if err != nil {
		return fmt.Errorf("schema status: %w", err)
	}
	logger.Info("migration complete", "applied", state.Applied, "required", state.Required)
	return nil
}

// Doctor validates connectivity and schema compatibility without mutating.
func Doctor(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	state, err := postgres.SchemaStatus(ctx, pool)
	if err != nil {
		return fmt.Errorf("schema status: %w", err)
	}
	if !state.Compatible() {
		return postgres.SchemaError(state)
	}
	rcloneOK := "configured"
	logger.Info("doctor ok",
		"schema_applied", state.Applied, "schema_required", state.Required,
		"http_addr", cfg.HTTPAddr, "loopback_only", cfg.LoopbackOnly(),
		"rclone_path", cfg.RclonePath, "rclone", rcloneOK)
	return nil
}

// workerRunner runs the write-orchestration worker until ctx is cancelled.
type workerRunner func(ctx context.Context) error

// Serve starts the read-only HTTP transport and blocks until shutdown.
func Serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	// serve verifies schema compatibility but never auto-migrates (Gate 3 P2).
	// It rejects both missing and unexpected/future migrations (G3-R7).
	state, err := postgres.SchemaStatus(ctx, pool)
	if err != nil {
		return fmt.Errorf("schema status: %w", err)
	}
	if !state.Compatible() {
		return postgres.SchemaError(state)
	}

	if !cfg.LoopbackOnly() {
		logger.Warn("HTTP is bound to a non-loopback address; Gate 3 has no auth layer", "addr", cfg.HTTPAddr)
	}

	st := postgres.New(pool)
	return runServe(ctx, cfg, logger, pool, st, func(wctx context.Context) error {
		return worker.New(st, cfg.MaxConcurrentRoots, logger).Run(wctx)
	})
}

// runServe owns the single-writer lock, the worker, and the HTTP transport. On
// shutdown it joins the worker goroutine BEFORE the writer lock is released, so
// the lock is never released while worker orchestration is still running
// (G3-R2.4).
func runServe(ctx context.Context, cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool, st *postgres.Store, runWorker workerRunner) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Enforce the Architect-locked single active write daemon per database with a
	// PostgreSQL advisory lock held for the process lifetime (G3-R5). A second
	// daemon fails closed instead of silently starting another writer loop.
	lock, err := st.AcquireWriterLock(ctx)
	if err != nil {
		return fmt.Errorf("single-writer ownership: %w", err)
	}
	defer func() {
		rlCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		lock.Release(rlCtx)
	}()
	logger.Info("acquired single-writer ownership")

	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()

	var ready atomic.Bool
	workerDone := make(chan error, 1)
	go func() {
		ready.Store(true)
		workerDone <- runWorker(workerCtx)
	}()

	srv := httpapi.New(httpapi.Deps{
		Query:     postgres.NewQueryReader(pool),
		Readiness: postgres.NewReadiness(pool),
		Logger:    logger,
		Version:   version.String(),
		Ready:     ready.Load,
	})
	srv.Addr = cfg.HTTPAddr

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http transport listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		cancelWorker()
		joinWorker(workerDone, cfg.ShutdownTimeout, logger)
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down", "timeout", cfg.ShutdownTimeout.String())
		cancelWorker()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		herr := srv.Shutdown(shutdownCtx)
		joinWorker(workerDone, cfg.ShutdownTimeout, logger)
		return herr
	}
}

// joinWorker waits for the worker goroutine to stop. The caller releases the
// writer lock only after this returns, so the lock is held until worker
// orchestration has actually stopped. If the worker is still running after the
// graceful timeout it does NOT release early: it keeps holding the writer lock
// and keeps waiting (the worker was already asked to cancel), so another daemon
// can never acquire single-writer ownership while a worker is alive (G3-R2.4).
func joinWorker(done <-chan error, timeout time.Duration, logger *slog.Logger) {
	select {
	case err := <-done:
		if err != nil {
			logger.Warn("worker exited with error", "error_class", "worker")
		}
		return
	case <-time.After(timeout):
		logger.Error("shutdown timeout: worker still running; holding the writer lock until it stops",
			"shutdown_timeout", timeout.String())
	}
	err := <-done
	if err != nil {
		logger.Warn("worker exited with error", "error_class", "worker")
	}
}

// Scan implements `indexcore scan --root <root_id>` (Gate 3 P5): run the collector,
// persist DRAFT->SUBMITTED, then let the Kernel Coordinator reconcile.
func Scan(ctx context.Context, base config.Config, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := base
	cfg.RegisterFlags(fs)
	rootID := fs.String("root", "", "root UUID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if *rootID == "" {
		return errors.New("scan: --root is required")
	}
	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	res, err := scan.New(postgres.New(pool), cfg.RclonePath, cfg.RcloneConfig, cfg.ScanTimeout, logger).Scan(ctx, *rootID)
	if err != nil {
		return err
	}
	writeJSON(map[string]any{
		"root_id": *rootID, "snapshot_id": res.SnapshotID,
		"status": string(res.Outcome.Status), "snapshot_lifecycle": string(res.Outcome.SnapshotLifecycle),
		"generation": res.Outcome.Generation, "applied_generation": res.Outcome.AppliedGeneration,
	})
	return nil
}
