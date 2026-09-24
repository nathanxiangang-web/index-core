package incrementalexec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// Prototype hard safety caps for one bounded cycle. These are structural bounds,
// not production defaults, SLA, cadence, or provider limits.
const (
	MaxCycleItems    = 5
	MaxCycleWallTime = 60 * time.Second
)

// OneShotExecutor is the accepted P4 one-shot primitive. P5 only ever calls
// ExecuteOne; it never reimplements selection, claim, scan, typed mapping, retry
// eligibility, or completion.
type OneShotExecutor interface {
	ExecuteOne(ctx context.Context) (Result, error)
}

// CycleConfig is the finite budget for one bounded cycle.
type CycleConfig struct {
	MaxItems    int
	MaxWallTime time.Duration
}

// Validate enforces the prototype hard caps. An invalid config fails before the
// first ExecuteOne.
func (c CycleConfig) Validate() error {
	if c.MaxItems < 1 || c.MaxItems > MaxCycleItems {
		return fmt.Errorf("cycle: max_items must be within [1, %d], got %d", MaxCycleItems, c.MaxItems)
	}
	if c.MaxWallTime <= 0 || c.MaxWallTime > MaxCycleWallTime {
		return fmt.Errorf("cycle: max_wall_time must be within (0, %s], got %s", MaxCycleWallTime, c.MaxWallTime)
	}
	return nil
}

// StopReason is the stable, finite reason a cycle stopped.
type StopReason string

const (
	StopNoEligibleWork      StopReason = "NO_ELIGIBLE_WORK"
	StopMaxItems            StopReason = "MAX_ITEMS"
	StopMaxWallTime         StopReason = "MAX_WALL_TIME"
	StopStaleSelection      StopReason = "STALE_SELECTION"
	StopInternalItemFailure StopReason = "INTERNAL_ITEM_FAILURE"
	StopSystemicError       StopReason = "SYSTEMIC_ERROR"
	StopContextCancelled    StopReason = "CONTEXT_CANCELLED"
)

// CycleResult reports the deterministic outcome of one bounded cycle.
type CycleResult struct {
	StartedAt  time.Time
	FinishedAt time.Time

	StopReason StopReason

	Invocations   int
	SelectedItems int
	Succeeded     int
	Failed        int

	Last Result

	// InterruptedInFlight is true when cancellation — from EITHER the parent
	// context or the cycle-owned wall deadline — interrupted a P4 invocation
	// after a proven committed claim (ClaimedSignalSeq > 0), leaving the Work
	// potentially IN_FLIGHT. A selected-but-unclaimed item, or a budget that
	// expires between items, reports false.
	InterruptedInFlight bool
}

// CycleRunner drains a bounded number of already-eligible items by repeatedly
// invoking the accepted P4 one-shot primitive. It is a finite in-process draining
// primitive, not a scheduler: it never waits for future eligibility.
type CycleRunner struct {
	one OneShotExecutor
}

// NewCycleRunner builds a bounded cycle runner over a P4 one-shot executor.
func NewCycleRunner(one OneShotExecutor) (*CycleRunner, error) {
	if one == nil {
		return nil, errors.New("incrementalexec: one-shot executor is required")
	}
	return &CycleRunner{one: one}, nil
}

// Run executes at most cfg.MaxItems P4 ExecuteOne calls within cfg.MaxWallTime
// and returns a finite stop result. Execution is strictly serial: at most one
// ExecuteOne is in flight at any time.
func (r *CycleRunner) Run(ctx context.Context, cfg CycleConfig) (CycleResult, error) {
	if err := cfg.Validate(); err != nil {
		return CycleResult{}, err
	}

	res := CycleResult{StartedAt: time.Now()}
	cycleCtx, cancel := context.WithTimeout(ctx, cfg.MaxWallTime)
	defer cancel()

	for {
		// Deterministic boundary precedence before every new item:
		// parent cancellation > max-items > cycle wall budget.
		if perr := ctx.Err(); perr != nil {
			return res.stop(StopContextCancelled), perr
		}
		if res.SelectedItems >= cfg.MaxItems {
			return res.stop(StopMaxItems), nil
		}
		if cycleCtx.Err() != nil {
			return res.stop(StopMaxWallTime), nil
		}

		item, err := r.one.ExecuteOne(cycleCtx)
		res.Invocations++
		if item.Selected {
			res.SelectedItems++
			// Retain the last actually selected item; an empty no-work result
			// must not overwrite it.
			res.Last = item
		}

		if err == nil {
			res.Succeeded++
			continue
		}

		// Parent cancellation wins over any other classification. It still
		// reports a potentially interrupted in-flight claim when one committed.
		if perr := ctx.Err(); perr != nil {
			res.InterruptedInFlight = item.ClaimedSignalSeq > 0
			return res.stop(StopContextCancelled), perr
		}

		switch {
		case errors.Is(err, ErrNoEligibleWork):
			return res.stop(StopNoEligibleWork), nil

		case errors.Is(err, ErrStaleSelection):
			return res.stop(StopStaleSelection), err

		case errors.Is(err, ErrScannerFailed):
			class := item.FailureClass
			if class == nil {
				// A scanner failure whose durable class is unknown must never be
				// treated as an item-local continuation: fail closed.
				return res.stop(StopSystemicError), fmt.Errorf("cycle: scanner failure without a durable class: %w", err)
			}
			// Classify before counting: Failed counts durably completed classified
			// item failures only, so an unknown class must not increment it.
			switch *class {
			case state.ErrorInternal:
				// INTERNAL may signal a broken subsystem; do not hammer other
				// scopes through it.
				res.Failed++
				return res.stop(StopInternalItemFailure), err
			case state.ErrorTransientProvider, state.ErrorThrottled, state.ErrorAuthOrPermission,
				state.ErrorScopeTooLarge, state.ErrorInvalidScope, state.ErrorConfigInvalid, state.ErrorRootInactive:
				// The failed item moved to RETRY_WAIT / BLOCKED / SUSPENDED and is
				// no longer immediately eligible; continue draining ready work.
				res.Failed++
				continue
			default:
				// Unknown class is systemic uncertainty, not a durable failed item.
				return res.stop(StopSystemicError), fmt.Errorf("cycle: unknown failure class %q: %w", *class, err)
			}

		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			if cycleCtx.Err() == nil {
				// Not a cancellation owned by this cycle.
				return res.stop(StopSystemicError), err
			}
			res.InterruptedInFlight = item.ClaimedSignalSeq > 0
			return res.stop(StopMaxWallTime), nil

		default:
			// ErrCompletionFailed and any unexpected select/claim/store/runtime
			// error: stop immediately with no further item.
			return res.stop(StopSystemicError), err
		}
	}
}

// stop stamps FinishedAt and the stop reason once, returning the final result.
func (r CycleResult) stop(reason StopReason) CycleResult {
	r.StopReason = reason
	r.FinishedAt = time.Now()
	return r
}
