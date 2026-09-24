package incrementalexec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// WorkStore is the narrow slice of P3 operational-state primitives the executor
// needs. *postgres.Store satisfies it. Keeping it an interface (rather than the
// concrete Store) allows test-only completion-failure injection without adding
// any production failpoint.
type WorkStore interface {
	NextEligiblePendingWork(ctx context.Context, now time.Time) (state.DirtyScopeWork, bool, error)
	ClaimWork(ctx context.Context, rootID, scopeKey string, expectedWorkVersion int64, now time.Time) (state.DirtyScopeWork, error)
	CompleteSuccess(ctx context.Context, rootID, scopeKey string, claimedSignalSeq int64, now time.Time) (state.DirtyScopeWork, error)
	CompleteFailure(ctx context.Context, rootID, scopeKey string, claimedSignalSeq int64, class state.ErrorClass, retryNotBefore *time.Time, now time.Time) (state.DirtyScopeWork, error)
	GetWork(ctx context.Context, rootID, scopeKey string) (state.DirtyScopeWork, error)
}

// ScopedScanner is the P0 scoped-refresh seam. *scan.Service implements it.
type ScopedScanner interface {
	ScanScope(ctx context.Context, rootID, scope string, maxEntries int) (scan.Result, error)
}

// Result exposes the durable transitions of one ExecuteOne invocation.
type Result struct {
	Selected         bool
	RootID           string
	ScopeKey         string
	SelectedVersion  int64
	ClaimedSignalSeq int64
	SnapshotID       string
	FinalWorkState   state.WorkState
	FailureClass     *state.ErrorClass
}

// Executor runs at most one durable dirty-work item per ExecuteOne call.
type Executor struct {
	store   WorkStore
	scanner ScopedScanner
	cfg     Config
	now     func() time.Time
}

// New builds a one-shot executor. It fails closed on invalid configuration or
// missing dependencies, before any selection or claim.
func New(store WorkStore, scanner ScopedScanner, cfg Config, now func() time.Time) (*Executor, error) {
	if store == nil {
		return nil, errors.New("incrementalexec: store is required")
	}
	if scanner == nil {
		return nil, errors.New("incrementalexec: scanner is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Executor{store: store, scanner: scanner, cfg: cfg, now: now}, nil
}

// ExecuteOne processes zero or one eligible PENDING item and calls ScanScope at
// most once. There is no loop, sleep, second selection, or provider retry.
func (e *Executor) ExecuteOne(ctx context.Context) (Result, error) {
	if err := e.cfg.Validate(); err != nil {
		return Result{}, err
	}

	now := e.now()
	work, found, err := e.store.NextEligiblePendingWork(ctx, now)
	if err != nil {
		return Result{}, fmt.Errorf("select eligible work: %w", err)
	}
	if !found {
		return Result{}, ErrNoEligibleWork
	}
	res := Result{
		Selected:        true,
		RootID:          work.RootID,
		ScopeKey:        work.ScopeKey,
		SelectedVersion: work.Version,
	}

	claimed, err := e.store.ClaimWork(ctx, work.RootID, work.ScopeKey, work.Version, now)
	if err != nil {
		if errors.Is(err, postgres.ErrStateCASConflict) {
			// The selection went stale: no scan, no reselect.
			return res, fmt.Errorf("%w: %v", ErrStaleSelection, err)
		}
		return res, fmt.Errorf("claim work: %w", err)
	}
	if claimed.ClaimedSignalSeq == nil {
		return res, fmt.Errorf("claim work: missing claimed signal sequence")
	}
	res.ClaimedSignalSeq = *claimed.ClaimedSignalSeq

	// Exactly one scoped refresh per invocation.
	scanRes, scanErr := e.scanner.ScanScope(ctx, claimed.RootID, claimed.ScopeKey, e.cfg.MaxEntriesPerScope)
	if scanErr != nil {
		// Caller shutdown is not a provider failure: do not write a failure merely
		// because the caller is stopping. Leave IN_FLIGHT for explicit P3 recovery.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return res, ctxErr
		}

		class := MapScopeFailure(scanErr)
		completionNow := e.now()
		retryNotBefore := e.retryEligibility(class, completionNow)
		if _, cerr := e.store.CompleteFailure(ctx, claimed.RootID, claimed.ScopeKey,
			*claimed.ClaimedSignalSeq, class, retryNotBefore, completionNow); cerr != nil {
			return res, fmt.Errorf("%w: %v", ErrCompletionFailed, cerr)
		}
		res.FailureClass = &class
		if w, gerr := e.store.GetWork(ctx, claimed.RootID, claimed.ScopeKey); gerr == nil {
			res.FinalWorkState = w.WorkState
		}
		return res, fmt.Errorf("%w: class=%s: %v", ErrScannerFailed, class, scanErr)
	}
	res.SnapshotID = scanRes.SnapshotID

	completionNow := e.now()
	done, err := e.store.CompleteSuccess(ctx, claimed.RootID, claimed.ScopeKey, *claimed.ClaimedSignalSeq, completionNow)
	if err != nil {
		// Never re-scan and never tight-loop completion: durable recovery owns the
		// repair.
		return res, fmt.Errorf("%w: %v", ErrCompletionFailed, err)
	}
	res.FinalWorkState = done.WorkState
	return res, nil
}

// retryEligibility computes the future retry timestamp for retryable classes and
// nothing for the terminal ones. It never sleeps.
func (e *Executor) retryEligibility(class state.ErrorClass, completionNow time.Time) *time.Time {
	d, ok := e.cfg.Retry.DelayFor(class)
	if !ok {
		return nil
	}
	t := completionNow.Add(d)
	return &t
}

// MapScopeFailure exhaustively maps a typed scoped-refresh failure to a P3
// ErrorClass. Unknown or untyped failures fail closed as INTERNAL.
func MapScopeFailure(err error) state.ErrorClass {
	var se *scan.ScopeError
	if errors.As(err, &se) {
		switch se.Kind {
		case scan.ScopeFailureTransientProvider:
			return state.ErrorTransientProvider
		case scan.ScopeFailureThrottled:
			return state.ErrorThrottled
		case scan.ScopeFailureAuthOrPermission:
			return state.ErrorAuthOrPermission
		case scan.ScopeFailureTooLarge:
			return state.ErrorScopeTooLarge
		case scan.ScopeFailureInvalidScope:
			return state.ErrorInvalidScope
		case scan.ScopeFailureRootInactive:
			return state.ErrorRootInactive
		case scan.ScopeFailureConfigInvalid:
			return state.ErrorConfigInvalid
		case scan.ScopeFailureInternal:
			return state.ErrorInternal
		}
	}
	return state.ErrorInternal
}
