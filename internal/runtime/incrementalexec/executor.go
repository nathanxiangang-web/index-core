package incrementalexec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
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

	// The outer context must win over ANY completion. If the caller is shutting
	// down after the provider call, neither a provider failure nor a success may
	// be written; the Work stays IN_FLIGHT for explicit P3 recovery. This window
	// exists even when ScanScope itself returned a successful result.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, ctxErr
	}
	if scanErr != nil {
		return e.completeFailure(ctx, res, claimed, MapScopeFailure(scanErr), scanErr)
	}

	res.SnapshotID = scanRes.SnapshotID

	// A nil Go error is not proof of verification: only a fresh, applied (or
	// idempotent NOOP) observation verifies dirty work. STALE_INPUT, REJECTED,
	// zero and unknown outcomes must never clear the claimed epoch.
	if !verifiesCoverage(scanRes.Outcome.Status) {
		return e.completeFailure(ctx, res, claimed, state.ErrorInternal,
			fmt.Errorf("scan outcome %q does not verify scoped coverage", scanRes.Outcome.Status))
	}

	completionNow := e.now()
	done, err := e.store.CompleteSuccess(ctx, claimed.RootID, claimed.ScopeKey, *claimed.ClaimedSignalSeq, completionNow)
	if err != nil {
		// Never re-scan and never tight-loop completion: durable recovery owns the
		// repair.
		// Preserve both the durable completion sentinel and the underlying cause
		// (e.g. context.DeadlineExceeded) so a bounded caller can distinguish a
		// budget-driven interruption from an unrelated systemic failure.
		return res, fmt.Errorf("%w: %w", ErrCompletionFailed, err)
	}
	res.FinalWorkState = done.WorkState
	return res, nil
}

// completeFailure records a classified failure through P3 and returns the
// deterministic result of THIS invocation. The committed completion row is used
// directly, so a concurrent later transition can never rewrite FinalWorkState.
func (e *Executor) completeFailure(ctx context.Context, res Result, claimed state.DirtyScopeWork, class state.ErrorClass, cause error) (Result, error) {
	completionNow := e.now()
	done, err := e.store.CompleteFailure(ctx, claimed.RootID, claimed.ScopeKey,
		*claimed.ClaimedSignalSeq, class, e.retryEligibility(class, completionNow), completionNow)
	if err != nil {
		// Preserve both the durable completion sentinel and the underlying cause
		// (e.g. context.DeadlineExceeded) so a bounded caller can distinguish a
		// budget-driven interruption from an unrelated systemic failure.
		return res, fmt.Errorf("%w: %w", ErrCompletionFailed, err)
	}
	res.FailureClass = &class
	res.FinalWorkState = done.WorkState
	return res, fmt.Errorf("%w: class=%s: %v", ErrScannerFailed, class, cause)
}

// verifiesCoverage reports whether a ScanScope terminal outcome proves that
// fresh scoped coverage was admitted/applied. Everything else fails closed.
func verifiesCoverage(status domain.AdmissionStatus) bool {
	switch status {
	case domain.AdmissionApplied, domain.AdmissionNoop:
		return true
	default:
		return false
	}
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
