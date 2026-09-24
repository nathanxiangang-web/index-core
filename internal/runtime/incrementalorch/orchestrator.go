package incrementalorch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// DueWatchStore is the narrow slice of accepted P3 operational primitives the
// orchestrator needs. *postgres.Store satisfies it. P6 never reimplements the
// due-watch SQL or the atomic EmitDuePoll transaction.
type DueWatchStore interface {
	ListDueWatches(ctx context.Context, now time.Time, limit int) ([]state.ScopeWatchState, error)
	EmitDuePoll(ctx context.Context, rootID, scopeKey string, expectedWatchVersion int64, now time.Time) (state.DirtyScopeWork, error)
}

// ExecutorCycle is the accepted P5 bounded executor cycle.
// *incrementalexec.CycleRunner satisfies it.
type ExecutorCycle interface {
	Run(ctx context.Context, cfg incrementalexec.CycleConfig) (incrementalexec.CycleResult, error)
}

// StopReason is the stable, finite reason one orchestration cycle stopped.
type StopReason string

const (
	StopCompleted            StopReason = "COMPLETED"
	StopMaxWallTime          StopReason = "MAX_WALL_TIME"
	StopContextCancelled     StopReason = "CONTEXT_CANCELLED"
	StopMaterializationError StopReason = "MATERIALIZATION_ERROR"
	StopExecutorError        StopReason = "EXECUTOR_ERROR"
)

// Result reports the deterministic outcome of one manual orchestration cycle.
type Result struct {
	StartedAt  time.Time
	FinishedAt time.Time
	ObservedAt time.Time

	StopReason StopReason

	DueCandidates  int
	DueAttempted   int
	DueEmitted     int
	DueStale       int
	MoreDueWatches bool

	// MaterializationInterrupted is true when the P6-owned wall deadline
	// interrupted an EmitDuePoll, so the caller must not infer that final watch's
	// commit outcome from in-memory counters alone.
	MaterializationInterrupted bool

	ExecutorRan bool
	Executor    incrementalexec.CycleResult
}

// Runner composes the accepted P3 due-poll materialization with the accepted P5
// bounded executor cycle. RunCycle is strictly serial.
type Runner struct {
	store DueWatchStore
	exec  ExecutorCycle
	now   func() time.Time
}

// NewRunner builds a bounded orchestration runner.
func NewRunner(store DueWatchStore, exec ExecutorCycle, now func() time.Time) (*Runner, error) {
	if store == nil {
		return nil, errors.New("incrementalorch: due-watch store is required")
	}
	if exec == nil {
		return nil, errors.New("incrementalorch: executor cycle is required")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Runner{store: store, exec: exec, now: now}, nil
}

// RunCycle performs one finite orchestration cycle: capture one observed_at,
// materialize at most MaxDueWatchAttempts due watches, then run P5 exactly once.
func (r *Runner) RunCycle(ctx context.Context, cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}

	started := r.now()
	res := Result{StartedAt: started, ObservedAt: started}

	orchCtx, cancel := context.WithTimeout(ctx, cfg.MaxWallTime)
	defer cancel()

	// Boundary precedence before every new phase/operation:
	// parent cancellation > P6 wall deadline > operation.
	if perr := ctx.Err(); perr != nil {
		return r.stop(res, StopContextCancelled), perr
	}
	if orchCtx.Err() != nil {
		return r.stop(res, StopMaxWallTime), nil
	}

	limit := cfg.MaxDueWatchAttempts + 1
	watches, err := r.store.ListDueWatches(orchCtx, res.ObservedAt, limit)
	if err != nil {
		if perr := ctx.Err(); perr != nil {
			return r.stop(res, StopContextCancelled), perr
		}
		if isContextError(err) && orchCtx.Err() != nil {
			// Read-only due-list cancellation caused by the P6 deadline.
			return r.stop(res, StopMaxWallTime), nil
		}
		return r.stop(res, StopMaterializationError), fmt.Errorf("list due watches: %w", err)
	}
	res.DueCandidates = len(watches)
	if len(watches) > cfg.MaxDueWatchAttempts {
		res.MoreDueWatches = true
		watches = watches[:cfg.MaxDueWatchAttempts]
	}

	for _, w := range watches {
		if perr := ctx.Err(); perr != nil {
			return r.stop(res, StopContextCancelled), perr
		}
		if orchCtx.Err() != nil {
			return r.stop(res, StopMaxWallTime), nil
		}

		res.DueAttempted++
		if _, err := r.store.EmitDuePoll(orchCtx, w.RootID, w.ScopeKey, w.Version, res.ObservedAt); err != nil {
			if errors.Is(err, postgres.ErrStateCASConflict) {
				// The watch changed under us: one attempt consumed, no retry, no
				// re-read, no re-list; continue with the next snapshot row.
				res.DueStale++
				continue
			}
			if perr := ctx.Err(); perr != nil {
				return r.stop(res, StopContextCancelled), perr
			}
			if isContextError(err) && orchCtx.Err() != nil {
				res.MaterializationInterrupted = true
				return r.stop(res, StopMaxWallTime), nil
			}
			// Any other materialization error is fail-closed: already-committed
			// emissions stay committed and no compensation is attempted.
			return r.stop(res, StopMaterializationError),
				fmt.Errorf("emit due poll %s/%s: %w", w.RootID, w.ScopeKey, err)
		}
		res.DueEmitted++
	}

	// Execution phase: P5 runs exactly once even when DueEmitted == 0, so
	// pre-existing eligible PENDING work can drain.
	if perr := ctx.Err(); perr != nil {
		return r.stop(res, StopContextCancelled), perr
	}
	if orchCtx.Err() != nil {
		return r.stop(res, StopMaxWallTime), nil
	}

	res.ExecutorRan = true
	cycleRes, cerr := r.exec.Run(orchCtx, incrementalexec.CycleConfig{
		MaxItems:    cfg.MaxExecuteItems,
		MaxWallTime: cfg.MaxWallTime,
	})
	res.Executor = cycleRes

	if cerr == nil {
		// Parent cancellation outranks the P6-owned wall deadline: orchCtx is a
		// child of the parent context, so without this check a caller
		// cancellation racing a nil P5 return would be misreported as budget
		// exhaustion.
		if perr := ctx.Err(); perr != nil {
			return r.stop(res, StopContextCancelled), perr
		}
		if orchCtx.Err() != nil {
			return r.stop(res, StopMaxWallTime), nil
		}
		return r.stop(res, StopCompleted), nil
	}
	if perr := ctx.Err(); perr != nil {
		return r.stop(res, StopContextCancelled), perr
	}
	if isContextError(cerr) && orchCtx.Err() != nil {
		// The P6-owned deadline cancelled the nested P5 cycle; an unrelated
		// systemic P5 error is not hidden because it would not carry the context
		// cause.
		return r.stop(res, StopMaxWallTime), nil
	}
	return r.stop(res, StopExecutorError), fmt.Errorf("executor cycle: %w", cerr)
}

func (r *Runner) stop(res Result, reason StopReason) Result {
	res.StopReason = reason
	res.FinishedAt = r.now()
	return res
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
