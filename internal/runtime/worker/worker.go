// Package worker is the Gate 3 single-daemon write-orchestration loop. It
// processes each root's absolute FIFO PENDING head through the safe Coordinator,
// with bounded concurrency across roots and no busy-spin. Multi-daemon HA is
// explicitly deferred.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Worker processes pending admissions for roots.
type Worker struct {
	store         *postgres.Store
	maxConcurrent int
	poll          time.Duration
	backoff       time.Duration
	logger        *slog.Logger
}

// New builds a worker with bounded per-root concurrency and default intervals.
func New(store *postgres.Store, maxConcurrent int, logger *slog.Logger) *Worker {
	return NewWithIntervals(store, maxConcurrent, 2*time.Second, 2*time.Second, logger)
}

// NewWithIntervals builds a worker with explicit poll/backoff intervals.
func NewWithIntervals(store *postgres.Store, maxConcurrent int, poll, backoff time.Duration, logger *slog.Logger) *Worker {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if backoff <= 0 {
		backoff = 2 * time.Second
	}
	return &Worker{store: store, maxConcurrent: maxConcurrent, poll: poll, backoff: backoff, logger: logger}
}

// Run polls for pending roots until the context is cancelled, then waits for
// in-flight work to finish (graceful shutdown).
func (w *Worker) Run(ctx context.Context) error {
	sem := make(chan struct{}, w.maxConcurrent)
	var wg sync.WaitGroup
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-ticker.C:
		}

		roots, err := w.store.PendingRoots(ctx)
		if err != nil {
			w.logger.Warn("worker: list pending roots failed", "error_class", "db")
			continue
		}
	rootsLoop:
		for _, rootID := range roots {
			select {
			case sem <- struct{}{}:
			default:
				break rootsLoop // bounded concurrency reached; remaining roots next tick
			}
			wg.Add(1)
			go func(root string) {
				defer wg.Done()
				defer func() { <-sem }()
				w.processRoot(ctx, root)
			}(rootID)
		}
	}
}

// processRoot drains the per-root FIFO head one admission at a time. A transient
// failure backs off and leaves the durable PENDING head for the next attempt; it
// never allocates a new sequence.
func (w *Worker) processRoot(ctx context.Context, rootID string) {
	cfg, err := w.store.RootReconcileConfig(ctx, rootID)
	if err != nil {
		w.logger.Warn("worker: root policy unavailable; skipping", "root_id", rootID, "error_class", "config")
		return
	}
	c := postgres.NewCoordinator(w.store, cfg)

	for {
		if ctx.Err() != nil {
			return
		}
		out, done, err := c.ProcessHead(ctx, rootID)
		if err != nil {
			if errors.Is(err, postgres.ErrNotHead) {
				return
			}
			w.logger.Warn("worker: transient failure; will retry the same durable head",
				"root_id", rootID, "error_class", "reconcile")
			select {
			case <-ctx.Done():
			case <-time.After(w.backoff):
			}
			return
		}
		if !done {
			return
		}
		w.logger.Info("worker: admission processed",
			"root_id", rootID,
			"status", string(out.Status),
			"generation", out.Generation,
			"applied_generation", out.AppliedGeneration,
			"snapshot_lifecycle", string(out.SnapshotLifecycle))
	}
}
