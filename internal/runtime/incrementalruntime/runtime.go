package incrementalruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
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

// wakeReason is a bounded bitmask of coalesced wake sources. It never grows into
// an unbounded payload queue; the capacity-1 wake channel remains authoritative.
type wakeReason uint32

const (
	wakeStartup wakeReason = 1 << iota
	wakeContention
	wakeHint
	wakeBacklog
	wakeTimer
)

// Runtime owns exactly one serialized event loop.
type Runtime struct {
	store  MaintenanceStore
	cycle  CycleRunner
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) bool

	wake     chan struct{}
	wakeBits atomic.Uint32

	// Burst state is touched only by the single event loop goroutine.
	burstCount          int
	lastCycleFinishedAt time.Time
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
		sleep:  sleepCtx,
		wake:   make(chan struct{}, 1),
	}, nil
}

// Wake requests one coalesced runtime cycle from an external (Hint) source. It
// never blocks: a duplicate wake is dropped while a wake is already pending.
// Dropping is safe because the durable P8 signal / due watch already committed
// and the periodic timer is the fallback.
func (r *Runtime) Wake() { r.wakeWith(wakeHint) }

func (r *Runtime) wakeWith(reason wakeReason) {
	r.wakeBits.Or(uint32(reason))
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// consumeWakeReason returns the coalesced wake sources seen since the last call
// as a bounded, comma-separated token list.
func (r *Runtime) consumeWakeReason() string {
	bits := r.wakeBits.Swap(0)
	if bits == 0 {
		return "wake"
	}
	names := make([]string, 0, 5)
	for _, c := range [...]struct {
		bit  wakeReason
		name string
	}{
		{wakeStartup, "startup"},
		{wakeContention, "contention"},
		{wakeHint, "hint"},
		{wakeBacklog, "backlog"},
		{wakeTimer, "timer"},
	} {
		if bits&uint32(c.bit) != 0 {
			names = append(names, c.name)
		}
	}
	return strings.Join(names, ",")
}

// RecoverStartupInflight drains stale IN_FLIGHT roots in deterministic bounded
// batches. It must run only after exclusive writer-lock ownership and before the
// event loop and any listener are exposed. No provider I/O occurs.
//
// H2 progress guard: if a returned root contributes zero recovered rows in a
// whole batch, the durable state did not advance, so the loop fails closed
// instead of spinning forever.
func (r *Runtime) RecoverStartupInflight(ctx context.Context) (int, error) {
	total := 0
	for {
		roots, err := r.store.ListInflightRoots(ctx, InflightRecoveryBatch)
		if err != nil {
			return total, err
		}
		if len(roots) == 0 {
			if total > 0 {
				r.logger.Info("incremental_inflight_recovered", "phase", "startup", "recovered", total)
			}
			return total, nil
		}
		batchRecovered := 0
		progressed := false
		for _, rootID := range roots {
			n, err := r.store.RecoverStaleInflight(ctx, rootID, r.now())
			if err != nil {
				return total, err
			}
			if n > 0 {
				progressed = true
			}
			batchRecovered += n
		}
		total += batchRecovered
		if !progressed {
			return total, fmt.Errorf(
				"incrementalruntime: startup recovery made no progress for %d inflight root(s)", len(roots))
		}
		r.logger.Info("incremental_inflight_recovered",
			"phase", "startup", "recovered", batchRecovered, "roots", len(roots))
	}
}

// Run owns the single event loop until ctx is cancelled (returns nil) or a fatal
// runtime failure occurs (returns a non-nil error). At most one P6 RunCycle is
// active at any time.
func (r *Runtime) Run(ctx context.Context) error {
	r.logger.Info("incremental_runtime_started",
		"wake_interval", r.cfg.WakeInterval.String(),
		"burst_cap", MaxConsecutiveCyclesPerBurst,
		"burst_cooldown", BurstCooldown.String())
	defer r.logger.Info("incremental_runtime_stopped")

	timer := time.NewTimer(r.cfg.WakeInterval)
	defer timer.Stop()

	// Startup wake: inspect durable state once after startup recovery.
	r.wakeWith(wakeStartup)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
		case <-timer.C:
			r.wakeWith(wakeTimer)
		}
		resetTimer(timer, r.cfg.WakeInterval)

		// H1 exact burst gate: post-cycle idle time counts toward the cooldown, so
		// a genuine >= BurstCooldown idle starts immediately while a fifth
		// immediately-contiguous cycle waits only the remaining cooldown.
		if wait := r.burstWaitBeforeStart(r.now()); wait > 0 {
			r.logger.Info("incremental_runtime_wake",
				"reason", "burst_cooldown",
				"wait_ms", wait.Milliseconds(),
				"burst_count", r.burstCount)
			if !r.sleep(ctx, wait) {
				return nil
			}
			r.burstCount = 0 // waited the remainder: next cycle starts a new burst
		}

		r.logger.Info("incremental_runtime_wake",
			"wake_reason", r.consumeWakeReason(),
			"burst_count", r.burstCount)

		backlog, contention, err := r.runOnce(ctx)
		if err != nil {
			r.logger.Error("incremental_runtime_fatal", "error_class", "runtime", "phase", "cycle")
			return err
		}
		r.markCycleFinished(r.now())
		if backlog {
			if contention {
				r.wakeWith(wakeContention)
			} else {
				r.wakeWith(wakeBacklog)
			}
		}
	}
}

// burstWaitBeforeStart returns the remaining cooldown a cycle must wait for, or 0
// when it may start immediately.
func (r *Runtime) burstWaitBeforeStart(now time.Time) time.Duration {
	if r.lastCycleFinishedAt.IsZero() {
		return 0 // no previous cycle: the first cycle always starts a new burst
	}
	idle := now.Sub(r.lastCycleFinishedAt)
	if idle >= BurstCooldown {
		// A real >= BurstCooldown post-cycle idle already satisfied the cooldown.
		r.burstCount = 0
		return 0
	}
	if r.burstCount < MaxConsecutiveCyclesPerBurst {
		return 0
	}
	return BurstCooldown - idle
}

// markCycleFinished stamps the post-cycle idle baseline and advances the burst
// count. Time spent inside a cycle deliberately does not count as idle.
func (r *Runtime) markCycleFinished(now time.Time) {
	r.lastCycleFinishedAt = now
	r.burstCount++
}

// runOnce performs one bounded pass: bounded retry promotion, exactly one P6
// cycle, optional exact-root interrupted-claim recovery, then backlog detection.
// It reports whether known durable backlog remains and whether the stop was
// bounded optimistic-CAS contention.
func (r *Runtime) runOnce(ctx context.Context) (backlog bool, contention bool, err error) {
	now := r.now()
	promo, err := r.promoteDueRetries(ctx, now)
	if err != nil {
		return false, false, err
	}
	if promo.attempted > 0 || promo.more {
		r.logger.Info("incremental_retry_promotion",
			"candidates", promo.candidates,
			"attempted", promo.attempted,
			"promoted", promo.promoted,
			"stale", promo.stale,
			"more", promo.more)
	}

	cycleStart := r.now()
	res, cerr := r.cycle.RunCycle(ctx, r.cfg.Cycle)
	duration := r.now().Sub(cycleStart)

	if cerr != nil {
		if ctx.Err() != nil {
			return false, false, nil // parent shutdown, not a runtime failure
		}
		if isBoundedContention(res, cerr) {
			// Normal optimistic-CAS contention: a concurrent Hint or due-watch
			// mutation changed the selected Work version between P4 selection and
			// claim. This is expected under the hybrid runtime, not a system
			// failure. Never reselect inside the same P6 cycle; schedule a later
			// wake (still bounded by the burst cap) instead of terminating serve.
			r.logger.Info("incremental_cycle_finished",
				"stop_reason", string(res.Executor.StopReason),
				"duration_ms", duration.Milliseconds(),
				"contention", true,
				"selected_items", res.Executor.SelectedItems)
			return true, true, nil
		}
		return false, false, cerr // systemic/fatal: the runtime must fail closed
	}

	if res.Executor.InterruptedInFlight &&
		res.Executor.Last.ClaimedSignalSeq > 0 &&
		res.Executor.Last.RootID != "" {
		n, rerr := r.store.RecoverStaleInflight(ctx, res.Executor.Last.RootID, r.now())
		if rerr != nil {
			return false, false, rerr
		}
		if n > 0 {
			r.logger.Info("incremental_inflight_recovered",
				"phase", "interrupted", "root_id", res.Executor.Last.RootID, "recovered", n)
		}
	}

	backlog = promo.more || res.MoreDueWatches || res.Executor.StopReason == incrementalexec.StopMaxItems
	r.logger.Info("incremental_cycle_finished",
		"stop_reason", string(res.StopReason),
		"duration_ms", duration.Milliseconds(),
		"due_candidates", res.DueCandidates,
		"due_attempted", res.DueAttempted,
		"due_emitted", res.DueEmitted,
		"more_due_watches", res.MoreDueWatches,
		"selected_items", res.Executor.SelectedItems,
		"succeeded", res.Executor.Succeeded,
		"failed", res.Executor.Failed,
		"interrupted_in_flight", res.Executor.InterruptedInFlight,
		"backlog", backlog)
	return backlog, false, nil
}

// retryPromotionResult is the bounded, observable outcome of one maintenance pass.
type retryPromotionResult struct {
	candidates int
	attempted  int
	promoted   int
	stale      int
	more       bool
}

// promoteDueRetries promotes at most MaxRetryPromotions due RETRY_WAIT rows whose
// error class is one of the accepted transient provider classes. A stale CAS
// candidate is counted as stale and skipped, never retried inline. Any other Store
// error is fatal.
func (r *Runtime) promoteDueRetries(ctx context.Context, now time.Time) (retryPromotionResult, error) {
	var out retryPromotionResult
	candidates, err := r.store.ListDueRetryWork(ctx, now, r.cfg.MaxRetryPromotions+1)
	if err != nil {
		return out, err
	}
	out.candidates = len(candidates)
	out.more = len(candidates) > r.cfg.MaxRetryPromotions
	if out.more {
		candidates = candidates[:r.cfg.MaxRetryPromotions]
	}
	for _, c := range candidates {
		out.attempted++
		if _, err := r.store.RetryReady(ctx, c.RootID, c.ScopeKey, c.Version, now); err != nil {
			if errors.Is(err, postgres.ErrStateCASConflict) {
				out.stale++
				continue // stale operational state; not retried in this pass
			}
			return out, err
		}
		out.promoted++
	}
	return out, nil
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
