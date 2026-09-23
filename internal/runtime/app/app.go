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
	applied, required, _, err := postgres.SchemaStatus(ctx, pool)
	if err != nil {
		return fmt.Errorf("schema status: %w", err)
	}
	logger.Info("migration complete", "applied", applied, "required", required)
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
	applied, required, missing, err := postgres.SchemaStatus(ctx, pool)
	if err != nil {
		return fmt.Errorf("schema status: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("schema incompatible: applied %d/%d, missing %v; run `indexcore migrate`", applied, required, missing)
	}
	rcloneOK := "configured"
	logger.Info("doctor ok",
		"schema_applied", applied, "schema_required", required,
		"http_addr", cfg.HTTPAddr, "loopback_only", cfg.LoopbackOnly(),
		"rclone_path", cfg.RclonePath, "rclone", rcloneOK)
	return nil
}

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
	applied, required, missing, err := postgres.SchemaStatus(ctx, pool)
	if err != nil {
		return fmt.Errorf("schema status: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("schema incompatible: applied %d/%d, missing %v; run `indexcore migrate`", applied, required, missing)
	}

	if !cfg.LoopbackOnly() {
		logger.Warn("HTTP is bound to a non-loopback address; Gate 3 has no auth layer", "addr", cfg.HTTPAddr)
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Single write-orchestration daemon: bounded per-root concurrency, drains the
	// durable PENDING heads, and resumes them across restart.
	st := postgres.New(pool)
	var ready atomic.Bool
	wk := worker.New(st, cfg.MaxConcurrentRoots, logger)
	go func() {
		ready.Store(true)
		if err := wk.Run(ctx); err != nil {
			logger.Warn("worker stopped", "error_class", "worker")
		}
	}()

	srv := httpapi.New(httpapi.Deps{
		Pool:    pool,
		Query:   postgres.NewQueryReader(pool),
		Logger:  logger,
		Version: version.String(),
		Ready:   ready.Load,
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
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutting down", "timeout", cfg.ShutdownTimeout.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
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
	res, err := scan.New(postgres.New(pool), cfg.RclonePath, cfg.ScanTimeout, logger).Scan(ctx, *rootID)
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
