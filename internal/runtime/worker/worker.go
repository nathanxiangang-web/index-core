// Package worker is the Gate 3 single-daemon write-orchestration loop. It
// processes each root's absolute FIFO PENDING head through the safe Coordinator,
// with bounded concurrency across DIFFERENT roots (an in-flight root set prevents
// the same root from occupying multiple slots) and no busy-spin. It also recovers
// SUBMITTED-but-unadmitted Snapshots. Multi-daemon HA is explicitly deferred.
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
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		inFlight = map[string]bool{}
	)
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-ticker.C:
		}

		// Recover SUBMITTED-but-unadmitted Snapshots (crash window, G3-R3).
		w.recoverUnadmitted(ctx)

		roots, err := w.store.PendingRoots(ctx)
		if err != nil {
			w.logger.Warn("worker: list pending roots failed", "error_class", "db")
			continue
		}
		for _, rootID := range roots {
			// At most one active goroutine per root; skip roots already active so
			// a slow root cannot occupy multiple slots and starve other roots (G3-R4).
			mu.Lock()
			active := inFlight[rootID]
			mu.Unlock()
			if active {
				continue
			}
			select {
			case sem <- struct{}{}:
			default:
				continue // capacity reached; keep considering other roots, never break
			}
			mu.Lock()
			inFlight[rootID] = true
			mu.Unlock()
			wg.Add(1)
			go func(root string) {
				defer wg.Done()
				defer func() {
					<-sem
					mu.Lock()
					delete(inFlight, root)
					mu.Unlock()
				}()
				w.processRoot(ctx, root)
			}(rootID)
		}
	}
}

// recoverUnadmitted admits any durable SUBMITTED Snapshot that has no admission
// row, so a crash between Snapshot commit and Stage-1 admission is recoverable.
func (w *Worker) recoverUnadmitted(ctx context.Context) {
	items, err := w.store.SubmittedSnapshotsWithoutAdmission(ctx, 100)
	if err != nil {
		w.logger.Warn("worker: recovery sweep failed", "error_class", "db")
		return
	}
	for _, it := range items {
		if _, resumed, err := w.store.AdmitOrResumeSnapshot(ctx, it.RootID, it.SnapshotID); err != nil {
			w.logger.Warn("worker: recovery admit failed", "root_id", it.RootID, "snapshot_id", it.SnapshotID, "error_class", "admission")
			continue
		} else {
			w.logger.Info("worker: recovered unadmitted snapshot", "root_id", it.RootID, "snapshot_id", it.SnapshotID, "resumed", resumed)
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
