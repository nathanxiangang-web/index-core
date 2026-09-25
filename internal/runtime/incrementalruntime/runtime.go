package incrementalruntime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// CycleRunner is the accepted P6 orchestration seam. *incrementalorch.Runner
// satisfies it.
type CycleRunner interface {
	RunCycle(ctx context.Context, cfg incrementalorch.Config) (incrementalorch.Result, error)
}

// MaintenanceStore is the narrow slice of accepted P3 primitives the runtime
// needs. *postgres.Store satisfies it. The runtime receives no Query handler,
// HTTP response writer, provider credential, or second writable connection.
type MaintenanceStore interface {
	ListDueRetryWork(ctx context.Context, now time.Time, limit int) ([]state.DirtyScopeWork, error)
	RetryReady(ctx context.Context, rootID, scopeKey string, expectedVersion int64, now time.Time) (state.DirtyScopeWork, error)
	ListInflightRoots(ctx context.Context, limit int) ([]string, error)
	RecoverStaleInflight(ctx context.Context, rootID string, now time.Time) (int, error)
}

// Runtime owns exactly one serialized event loop.
type Runtime struct {
	store  MaintenanceStore
	cycle  CycleRunner
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
	wake   chan struct{}
}

// New builds the runtime. It fails closed on missing dependencies or an invalid
// config, before any goroutine, timer, or Store call starts.
func New(store MaintenanceStore, cycle CycleRunner, cfg Config, logger *slog.Logger, now func() time.Time) (*Runtime, error) {
	if store == nil {
		return nil, errors.New("incrementalruntime: store is required")
	}
	if cycle == nil {
		return nil, errors.New("incrementalruntime: cycle runner is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Runtime{
		store:  store,
		cycle:  cycle,
		cfg:    cfg,
		logger: logger,
		now:    now,
		wake:   make(chan struct{}, 1),
	}, nil
}

// Wake requests one coalesced runtime cycle. It never blocks: a duplicate wake is
// dropped while a wake is already pending. Dropping is safe because the durable
// P8 signal / due watch already committed and the periodic timer is the fallback.
func (r *Runtime) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// RecoverStartupInflight drains stale IN_FLIGHT roots in deterministic bounded
// batches. It must run only after exclusive writer-lock ownership and before the
// event loop and any listener are exposed. No provider I/O occurs.
func (r *Runtime) RecoverStartupInflight(ctx context.Context) (int, error) {
	total := 0
	for {
		roots, err := r.store.ListInflightRoots(ctx, InflightRecoveryBatch)
		if err != nil {
			return total, err
		}
		if len(roots) == 0 {
			return total, nil
		}
		for _, rootID := range roots {
			n, err := r.store.RecoverStaleInflight(ctx, rootID, r.now())
			if err != nil {
				return total, err
			}
			total += n
		}
	}
}

// Run owns the single event loop until ctx is cancelled (returns nil) or a fatal
// runtime failure occurs (returns a non-nil error). At most one P6 RunCycle is
// active at any time.
func (r *Runtime) Run(ctx context.Context) error {
	r.logger.Info("incremental_runtime_started", "wake_interval", r.cfg.WakeInterval.String())
	defer r.logger.Info("incremental_runtime_stopped")

	timer := time.NewTimer(r.cfg.WakeInterval)
	defer timer.Stop()

	// Startup wake: inspect durable state once after startup recovery.
	r.Wake()

	consecutive := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
			r.logger.Info("incremental_runtime_wake", "reason", "wake")
		case <-timer.C:
			r.logger.Info("incremental_runtime_wake", "reason", "timer")
		}
		resetTimer(timer, r.cfg.WakeInterval)

		if consecutive >= MaxConsecutiveCyclesPerBurst {
			r.logger.Info("incremental_runtime_wake", "reason", "burst_cooldown")
			if !sleepCtx(ctx, BurstCooldown) {
				return nil
			}
			consecutive = 0
		}

		backlog, err := r.runOnce(ctx)
		if err != nil {
			r.logger.Error("incremental_runtime_fatal", "error_class", "runtime")
			return err
		}
		// Burst accounting applies to EVERY immediately-contiguous cycle regardless
		// of wake source (timer / Hint / self-backlog). It is reset only by the
		// mandatory cooldown above, so no wake source can start the fifth
		// consecutive cycle before the cooldown.
		consecutive++
		if backlog {
			r.logger.Info("incremental_backlog_continue")
			r.Wake()
		}
	}
}

// runOnce performs one bounded pass: bounded retry promotion, exactly one P6
// cycle, optional exact-root interrupted-claim recovery, then backlog detection.
// It reports whether known durable backlog remains.
func (r *Runtime) runOnce(ctx context.Context) (bool, error) {
	now := r.now()
	promoted, moreRetries, err := r.promoteDueRetries(ctx, now)
	if err != nil {
		return false, err
	}
	if promoted > 0 {
		r.logger.Info("incremental_retry_promotion", "promoted", promoted, "more", moreRetries)
	}

	res, err := r.cycle.RunCycle(ctx, r.cfg.Cycle)
	if err != nil {
		if ctx.Err() != nil {
			return false, nil // parent shutdown, not a runtime failure
		}
		if isBoundedContention(res, err) {
			// Normal optimistic-CAS contention: a concurrent Hint or due-watch
			// mutation changed the selected Work version between P4 selection and
			// claim. This is expected under the hybrid runtime, not a system
			// failure. Never reselect inside the same P6 cycle; schedule a later
			// wake (still bounded by the burst cap) instead of terminating serve.
			r.logger.Info("incremental_cycle_finished",
				"stop_reason", string(res.Executor.StopReason), "contention", true)
			return true, nil
		}
		return false, err // systemic/fatal: the runtime must fail closed
	}
	r.logger.Info("incremental_cycle_finished",
		"stop_reason", string(res.StopReason),
		"selected_items", res.Executor.SelectedItems,
		"due_candidates", res.DueCandidates)

	if res.Executor.InterruptedInFlight &&
		res.Executor.Last.ClaimedSignalSeq > 0 &&
		res.Executor.Last.RootID != "" {
		n, rerr := r.store.RecoverStaleInflight(ctx, res.Executor.Last.RootID, r.now())
		if rerr != nil {
			return false, rerr
		}
		if n > 0 {
			r.logger.Info("incremental_inflight_recovered", "recovered", n, "root_id", res.Executor.Last.RootID)
		}
	}

	backlog := moreRetries || res.MoreDueWatches || res.Executor.StopReason == incrementalexec.StopMaxItems
	return backlog, nil
}

// promoteDueRetries promotes at most MaxRetryPromotions due RETRY_WAIT rows whose
// error class is one of the P10-approved transient provider classes. A stale CAS
// candidate is counted as stale and skipped, never retried inline. Any other Store
// error is fatal.
func (r *Runtime) promoteDueRetries(ctx context.Context, now time.Time) (int, bool, error) {
	candidates, err := r.store.ListDueRetryWork(ctx, now, r.cfg.MaxRetryPromotions+1)
	if err != nil {
		return 0, false, err
	}
	more := len(candidates) > r.cfg.MaxRetryPromotions
	if more {
		candidates = candidates[:r.cfg.MaxRetryPromotions]
	}
	promoted := 0
	for _, c := range candidates {
		if _, err := r.store.RetryReady(ctx, c.RootID, c.ScopeKey, c.Version, now); err != nil {
			if errors.Is(err, postgres.ErrStateCASConflict) {
				continue // stale operational state; not retried in this pass
			}
			return promoted, false, err
		}
		promoted++
	}
	return promoted, more, nil
}

// isBoundedContention reports whether a returned P6 error is the expected
// optimistic-CAS contention of hybrid Hint/P4 racing (STALE_SELECTION), which is
// non-fatal and handled by a later bounded wake. INTERNAL_ITEM_FAILURE,
// SYSTEMIC_ERROR, MATERIALIZATION_ERROR and unexpected executor/store failures
// are deliberately NOT treated as bounded contention here.
func isBoundedContention(res incrementalorch.Result, err error) bool {
	if res.Executor.StopReason == incrementalexec.StopStaleSelection {
		return true
	}
	return errors.Is(err, incrementalexec.ErrStaleSelection)
}

// resetTimer re-arms a single-source timer without letting ticks accumulate.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
