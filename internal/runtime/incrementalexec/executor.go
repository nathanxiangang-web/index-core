package incrementalexec

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
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

type ClaimedScopedScanner interface {
	ScanClaimedScope(context.Context, state.DirtyScopeWork, int) (scan.Result, error)
}

func (e *Executor) scanClaimed(ctx context.Context, w state.DirtyScopeWork) (scan.Result, error) {
	if s, ok := e.scanner.(ClaimedScopedScanner); ok {
		return s.ScanClaimedScope(ctx, w, e.cfg.MaxEntriesPerScope)
	}
	return e.scanner.ScanScope(ctx, w.RootID, w.ScopeKey, e.cfg.MaxEntriesPerScope)
}

// DiscoveryStore commits follow-up scopes and the parent's verified prefix in
// one transaction. A crash cannot strand a verified parent without its children.
type DiscoveryStore interface {
	CompleteSuccessWithPrioritizedChildren(context.Context, string, string, int64, []string, string, time.Time) (state.DirtyScopeWork, error)
}

// MirrorDiscoveryStore adds periodic-watch proof and duplicate coalescing to the
// SAME child-publication/parent-completion transaction. Legacy callers unchanged.
type MirrorDiscoveryStore interface {
	CompleteSuccessWithMirrorChildren(context.Context, string, string, int64, []string, string, time.Time, time.Duration) (state.DirtyScopeWork, state.MirrorDiscoveryStats, error)
}

// Result exposes the durable transitions of one ExecuteOne invocation.
type Result struct {
	Discovery        state.MirrorDiscoveryStats
	SelectionClass   string
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
	store          WorkStore
	scanner        ScopedScanner
	cfg            Config
	now            func() time.Time
	retryFloor     func(string) time.Time
	available      func(context.Context, string) (time.Time, error)
	cooldownCursor string
}

// WithRetryFloor clones configuration before actor startup; never mutates a live executor.
func (e *Executor) WithRetryFloor(f func(string) time.Time) *Executor {
	c := *e
	c.retryFloor = f
	return &c
}
func (e *Executor) WithSourceAvailability(f func(context.Context, string) (time.Time, error)) *Executor {
	c := *e
	c.available = f
	return &c
}

func (e *Executor) readySelection(ctx context.Context, now time.Time, w state.DirtyScopeWork, found bool, class string) (state.DirtyScopeWork, bool, string, error) {
	excluded := []string{}
	for found && e.available != nil {
		at, err := e.available(ctx, w.RootID)
		if err != nil {
			return w, false, class, err
		}
		if !at.After(now) {
			return w, true, class, nil
		}
		excluded = append(excluded, w.RootID)
		if e.cooldownCursor == "" {
			e.cooldownCursor = w.RootID
		}
		if len(excluded) == 8 {
			return state.DirtyScopeWork{}, false, class, nil
		}
		fair, ok := e.store.(PoolFairStore)
		if !ok {
			return w, false, class, errors.New("cooldown admission requires fair store")
		}
		w, found, err = fair.NextPoolFairWork(ctx, now, e.cooldownCursor, excluded...)
		if err != nil {
			return w, false, class, err
		}
		if found {
			e.cooldownCursor = w.RootID
		}
		class = "SOURCE_COOLDOWN_SKIP"
	}
	return w, found, class, nil
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
	if cfg.DiscoveryScope != "" {
		if _, ok := store.(DiscoveryStore); !ok {
			return nil, errors.New("incrementalexec: discovery requires atomic completion store")
		}
	}
	if cfg.FirstDiscoveryEnabled {
		if _, ok := store.(FirstDiscoveryStore); !ok {
			return nil, errors.New("first discovery requires planning store")
		}
	}
	if cfg.MirrorRevisitInterval > 0 {
		if _, ok := store.(MirrorDiscoveryStore); !ok {
			return nil, errors.New("mirror revisit requires atomic mirror completion store")
		}
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Executor{store: store, scanner: scanner, cfg: cfg, now: now}, nil
}

// ExecuteOne processes zero or one eligible PENDING item and calls ScanScope at
// most once. Opt-in cycle planning may read up to three bounded candidate
// cohorts, but there is only one claim and no re-selection after a stale claim,
// loop, sleep or provider retry.
func (e *Executor) ExecuteOne(ctx context.Context) (Result, error) {
	if err := e.cfg.Validate(); err != nil {
		return Result{}, err
	}

	now := e.now()
	work, found, class, err := e.selectWork(ctx, now)
	if err == nil {
		work, found, class, err = e.readySelection(ctx, now, work, found, class)
	}
	if err != nil {
		return Result{}, fmt.Errorf("select eligible work: %w", err)
	}
	if !found {
		return Result{}, ErrNoEligibleWork
	}
	res, claimed, err := e.claimSelected(ctx, work, class, now)
	if err != nil {
		return res, err
	}

	// Exactly one scoped refresh per invocation.
	scanRes, scanErr := e.scanClaimed(ctx, claimed)
	return e.completeScan(ctx, res, claimed, scanRes, scanErr)
}

func (e *Executor) claimSelected(ctx context.Context, work state.DirtyScopeWork, class string, now time.Time) (Result, state.DirtyScopeWork, error) {
	res := Result{SelectionClass: class, Selected: true, RootID: work.RootID, ScopeKey: work.ScopeKey, SelectedVersion: work.Version}
	claimed, err := e.store.ClaimWork(ctx, work.RootID, work.ScopeKey, work.Version, now)
	if err != nil {
		if errors.Is(err, postgres.ErrStateCASConflict) {
			return res, claimed, fmt.Errorf("%w: %v", ErrStaleSelection, err)
		}
		return res, claimed, fmt.Errorf("claim work: %w", err)
	}
	if claimed.ClaimedSignalSeq == nil {
		return res, claimed, errors.New("claim work: missing claimed signal sequence")
	}
	res.ClaimedSignalSeq = *claimed.ClaimedSignalSeq
	e.recordSelection(ctx, claimed.RootID, class)
	return res, claimed, nil
}

// One durable completion implementation for serial and pool callers.
func (e *Executor) completeScan(ctx context.Context, res Result, claimed state.DirtyScopeWork, scanRes scan.Result, scanErr error) (Result, error) {

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
	prefix := e.cfg.DiscoveryScope
	if scanRes.MirrorPresence {
		prefix = "/"
	}
	children, childErr := discoveryChildren(prefix, claimed.ScopeKey, scanRes.ChildScopes)
	if childErr != nil {
		return e.completeFailure(ctx, res, claimed, state.ErrorInternal, childErr)
	}
	var done state.DirtyScopeWork
	var err error
	if len(children) > 0 && scanRes.MirrorPresence && e.cfg.MirrorRevisitInterval > 0 {
		done, res.Discovery, err = e.store.(MirrorDiscoveryStore).CompleteSuccessWithMirrorChildren(ctx, claimed.RootID, claimed.ScopeKey, *claimed.ClaimedSignalSeq, children, e.cfg.DiscoveryScope, completionNow, e.cfg.MirrorRevisitInterval)
	} else if len(children) > 0 {
		discoveryStore, ok := e.store.(DiscoveryStore)
		if !ok {
			return res, errors.New("source mirror requires atomic discovery store")
		}
		done, err = discoveryStore.CompleteSuccessWithPrioritizedChildren(ctx, claimed.RootID, claimed.ScopeKey, *claimed.ClaimedSignalSeq, children, e.cfg.DiscoveryScope, completionNow)
	} else {
		done, err = e.store.CompleteSuccess(ctx, claimed.RootID, claimed.ScopeKey, *claimed.ClaimedSignalSeq, completionNow)
	}
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

// Discovery is bounded to eight levels below an explicit subtree, and at most
// the existing 1000-entry scope cap per completion. No provider recursion or
// recurring watch is added. Re-observing a directory deliberately re-verifies
// its children, including files that arrived after an earlier empty observation.
func discoveryChildren(prefix, parent string, observed []string) ([]string, error) {
	if prefix == "" || (parent != prefix && !strings.HasPrefix(parent, strings.TrimSuffix(prefix, "/")+"/")) {
		return nil, nil
	}
	if len(observed) > scan.MaxScopedEntries {
		return nil, errors.New("discovery: child count exceeds scope cap")
	}
	children := []string{}
	seen := map[string]bool{}
	for _, child := range observed {
		if state.ValidateScopeKey(child) != nil || path.Dir(child) != parent {
			return nil, errors.New("discovery: invalid direct child")
		}
		rel := strings.TrimPrefix(child, strings.TrimSuffix(prefix, "/")+"/")
		if len(strings.Split(rel, "/")) <= 8 && !seen[child] {
			children = append(children, child)
			seen[child] = true
		}
	}
	return children, nil
}

// completeFailure records a classified failure through P3 and returns the
// deterministic result of THIS invocation. The committed completion row is used
// directly, so a concurrent later transition can never rewrite FinalWorkState.
func (e *Executor) completeFailure(ctx context.Context, res Result, claimed state.DirtyScopeWork, class state.ErrorClass, cause error) (Result, error) {
	completionNow := e.now()
	next := e.retryEligibility(class, completionNow)
	if next != nil && e.retryFloor != nil {
		floor := e.retryFloor(claimed.RootID)
		if floor.After(*next) {
			next = &floor
		}
	}
	done, err := e.store.CompleteFailure(ctx, claimed.RootID, claimed.ScopeKey,
		*claimed.ClaimedSignalSeq, class, next, completionNow)
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
