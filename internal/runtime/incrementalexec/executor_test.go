package incrementalexec_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

var p4Now = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

type fakeStore struct {
	next    state.DirtyScopeWork
	found   bool
	nextErr error

	claimed  state.DirtyScopeWork
	claimErr error

	successResult state.DirtyScopeWork
	successErr    error

	failureResult state.DirtyScopeWork
	failureErr    error

	claimVersion int64
	successCalls int32
	failureCalls int32
	failClass    state.ErrorClass
	failRetry    *time.Time
}

func (f *fakeStore) NextEligiblePendingWork(context.Context, time.Time) (state.DirtyScopeWork, bool, error) {
	return f.next, f.found, f.nextErr
}

func (f *fakeStore) ClaimWork(_ context.Context, _, _ string, expectedWorkVersion int64, _ time.Time) (state.DirtyScopeWork, error) {
	f.claimVersion = expectedWorkVersion
	return f.claimed, f.claimErr
}

func (f *fakeStore) CompleteSuccess(context.Context, string, string, int64, time.Time) (state.DirtyScopeWork, error) {
	atomic.AddInt32(&f.successCalls, 1)
	return f.successResult, f.successErr
}

func (f *fakeStore) CompleteFailure(_ context.Context, _, _ string, _ int64, class state.ErrorClass, retryNotBefore *time.Time, _ time.Time) (state.DirtyScopeWork, error) {
	atomic.AddInt32(&f.failureCalls, 1)
	f.failClass = class
	f.failRetry = retryNotBefore
	return f.failureResult, f.failureErr
}

func p4AppliedResult() scan.Result {
	return scan.Result{
		SnapshotID: "snap-1",
		Outcome:    postgres.ReconcileOutcome{Status: domain.AdmissionApplied},
	}
}

type countingScanner struct {
	calls  int32
	result scan.Result
	err    error
	fn     func(context.Context, string, string, int) (scan.Result, error)
}

func (s *countingScanner) ScanScope(ctx context.Context, rootID, scope string, maxEntries int) (scan.Result, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.fn != nil {
		return s.fn(ctx, rootID, scope, maxEntries)
	}
	return s.result, s.err
}

func p4Config() incrementalexec.Config {
	return incrementalexec.Config{
		MaxEntriesPerScope: 1000,
		Retry: incrementalexec.RetryPolicy{
			TransientProvider: 30 * time.Second,
			Throttled:         45 * time.Second,
			Internal:          60 * time.Second,
		},
	}
}

func p4ClaimedWork() state.DirtyScopeWork {
	seq := int64(7)
	return state.DirtyScopeWork{
		RootID: p4RootID, ScopeKey: "/a", WorkState: state.WorkInFlight, SignalSeq: 7,
		ClaimedSignalSeq: &seq, Version: 3,
	}
}

const p4RootID = "00000000-0000-0000-0000-00000000b001"

func TestP4ConfigRejectsInvalid(t *testing.T) {
	base := p4Config()

	bad := base
	bad.MaxEntriesPerScope = -1
	if err := bad.Validate(); err == nil {
		t.Fatal("negative max_entries must be rejected")
	}
	bad = base
	bad.MaxEntriesPerScope = scan.MaxScopedEntries + 1
	if err := bad.Validate(); err == nil {
		t.Fatal("max_entries above the P0 hard cap must be rejected")
	}
	bad = base
	bad.Retry.TransientProvider = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("non-positive transient delay must be rejected")
	}
	bad = base
	bad.Retry.Throttled = -time.Second
	if err := bad.Validate(); err == nil {
		t.Fatal("negative throttled delay must be rejected")
	}
	bad = base
	bad.Retry.Internal = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("non-positive internal delay must be rejected")
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestP4NewRequiresDependencies(t *testing.T) {
	sc := &countingScanner{}
	if _, err := incrementalexec.New(nil, sc, p4Config(), nil); err == nil {
		t.Fatal("nil store must be rejected")
	}
	if _, err := incrementalexec.New(&fakeStore{}, nil, p4Config(), nil); err == nil {
		t.Fatal("nil scanner must be rejected")
	}
	bad := p4Config()
	bad.Retry.Internal = 0
	if _, err := incrementalexec.New(&fakeStore{}, sc, bad, nil); err == nil {
		t.Fatal("invalid config must fail before selection")
	}
}

func TestP4MapScopeFailureExhaustive(t *testing.T) {
	cases := []struct {
		kind scan.ScopeFailureKind
		want state.ErrorClass
	}{
		{scan.ScopeFailureTransientProvider, state.ErrorTransientProvider},
		{scan.ScopeFailureThrottled, state.ErrorThrottled},
		{scan.ScopeFailureAuthOrPermission, state.ErrorAuthOrPermission},
		{scan.ScopeFailureTooLarge, state.ErrorScopeTooLarge},
		{scan.ScopeFailureInvalidScope, state.ErrorInvalidScope},
		{scan.ScopeFailureRootInactive, state.ErrorRootInactive},
		{scan.ScopeFailureConfigInvalid, state.ErrorConfigInvalid},
		{scan.ScopeFailureInternal, state.ErrorInternal},
	}
	for _, tc := range cases {
		err := scan.NewScopeError(tc.kind, errors.New("boom"))
		if got := incrementalexec.MapScopeFailure(err); got != tc.want {
			t.Fatalf("kind %s mapped to %s, want %s", tc.kind, got, tc.want)
		}
	}
	// Unknown typed kind and untyped errors fail closed as INTERNAL.
	if got := incrementalexec.MapScopeFailure(scan.NewScopeError("SURPRISE", errors.New("x"))); got != state.ErrorInternal {
		t.Fatalf("unknown kind must fail closed as INTERNAL, got %s", got)
	}
	if got := incrementalexec.MapScopeFailure(errors.New("plain error")); got != state.ErrorInternal {
		t.Fatalf("untyped error must fail closed as INTERNAL, got %s", got)
	}
}

func TestP4NoEligibleWork(t *testing.T) {
	st := &fakeStore{found: false}
	sc := &countingScanner{}
	ex, err := incrementalexec.New(st, sc, p4Config(), func() time.Time { return p4Now })
	if err != nil {
		t.Fatal(err)
	}
	res, err := ex.ExecuteOne(context.Background())
	if !errors.Is(err, incrementalexec.ErrNoEligibleWork) {
		t.Fatalf("want ErrNoEligibleWork, got %v", err)
	}
	if res.Selected || res.RootID != "" {
		t.Fatalf("no-work result must be empty, got %+v", res)
	}
	if n := atomic.LoadInt32(&sc.calls); n != 0 {
		t.Fatalf("no provider call allowed, got %d", n)
	}
}

func TestP4OneShotSuccessViaFakeStore(t *testing.T) {
	st := &fakeStore{
		next:          state.DirtyScopeWork{RootID: p4RootID, ScopeKey: "/a", WorkState: state.WorkPending, Version: 3},
		found:         true,
		claimed:       p4ClaimedWork(),
		successResult: state.DirtyScopeWork{WorkState: state.WorkVerified, Version: 5},
	}
	sc := &countingScanner{result: p4AppliedResult()}
	ex, err := incrementalexec.New(st, sc, p4Config(), func() time.Time { return p4Now })
	if err != nil {
		t.Fatal(err)
	}
	res, err := ex.ExecuteOne(context.Background())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if st.claimVersion != 3 {
		t.Fatalf("claim must use the selected version 3, got %d", st.claimVersion)
	}
	if n := atomic.LoadInt32(&sc.calls); n != 1 {
		t.Fatalf("exactly one scan required, got %d", n)
	}
	if n := atomic.LoadInt32(&st.successCalls); n != 1 {
		t.Fatalf("exactly one CompleteSuccess required, got %d", n)
	}
	if res.ClaimedSignalSeq != 7 || res.SnapshotID != "snap-1" || res.FinalWorkState != state.WorkVerified {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestP4StaleSelectionDoesNotScan(t *testing.T) {
	st := &fakeStore{
		next:     state.DirtyScopeWork{RootID: p4RootID, ScopeKey: "/a", WorkState: state.WorkPending, Version: 3},
		found:    true,
		claimErr: postgres.ErrStateCASConflict,
	}
	sc := &countingScanner{}
	ex, err := incrementalexec.New(st, sc, p4Config(), func() time.Time { return p4Now })
	if err != nil {
		t.Fatal(err)
	}
	res, err := ex.ExecuteOne(context.Background())
	if !errors.Is(err, incrementalexec.ErrStaleSelection) {
		t.Fatalf("want ErrStaleSelection, got %v", err)
	}
	if !res.Selected || res.SelectedVersion != 3 {
		t.Fatalf("result must expose the stale selection, got %+v", res)
	}
	if n := atomic.LoadInt32(&sc.calls); n != 0 {
		t.Fatalf("stale selection must not scan, got %d calls", n)
	}
	if n := atomic.LoadInt32(&st.successCalls); n != 0 {
		t.Fatal("stale selection must not complete")
	}
}

func TestP4CompletionFailureReturnsPersistenceError(t *testing.T) {
	st := &fakeStore{
		next:          state.DirtyScopeWork{RootID: p4RootID, ScopeKey: "/a", WorkState: state.WorkPending, Version: 3},
		found:         true,
		claimed:       p4ClaimedWork(),
		successErr:    errors.New("injected completion failure"),
		successResult: state.DirtyScopeWork{},
	}
	sc := &countingScanner{result: p4AppliedResult()}
	ex, err := incrementalexec.New(st, sc, p4Config(), func() time.Time { return p4Now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ex.ExecuteOne(context.Background()); !errors.Is(err, incrementalexec.ErrCompletionFailed) {
		t.Fatalf("want ErrCompletionFailed, got %v", err)
	}
	if n := atomic.LoadInt32(&sc.calls); n != 1 {
		t.Fatalf("scanner must be called exactly once, got %d", n)
	}
	if n := atomic.LoadInt32(&st.successCalls); n != 1 {
		t.Fatalf("no hidden completion retry loop allowed, got %d", n)
	}
}

func TestP4CallerCancellationIsNotAProviderFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st := &fakeStore{
		next:    state.DirtyScopeWork{RootID: p4RootID, ScopeKey: "/a", WorkState: state.WorkPending, Version: 3},
		found:   true,
		claimed: p4ClaimedWork(),
	}
	sc := &countingScanner{fn: func(ctx context.Context, _ string, _ string, _ int) (scan.Result, error) {
		cancel()
		<-ctx.Done()
		return scan.Result{}, ctx.Err()
	}}
	ex, err := incrementalexec.New(st, sc, p4Config(), func() time.Time { return p4Now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ex.ExecuteOne(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if n := atomic.LoadInt32(&st.failureCalls); n != 0 {
		t.Fatalf("caller cancellation must not write a provider failure, got %d", n)
	}
}

func TestP4RetryEligibilityOnlyForRetryableClasses(t *testing.T) {
	for _, tc := range []struct {
		kind      scan.ScopeFailureKind
		wantState state.WorkState
		wantRetry bool
	}{
		{scan.ScopeFailureTransientProvider, state.WorkRetryWait, true},
		{scan.ScopeFailureThrottled, state.WorkRetryWait, true},
		{scan.ScopeFailureInternal, state.WorkRetryWait, true},
		{scan.ScopeFailureAuthOrPermission, state.WorkBlocked, false},
		{scan.ScopeFailureTooLarge, state.WorkBlocked, false},
		{scan.ScopeFailureInvalidScope, state.WorkBlocked, false},
		{scan.ScopeFailureConfigInvalid, state.WorkBlocked, false},
		{scan.ScopeFailureRootInactive, state.WorkSuspended, false},
	} {
		st := &fakeStore{
			next:          state.DirtyScopeWork{RootID: p4RootID, ScopeKey: "/a", WorkState: state.WorkPending, Version: 3},
			found:         true,
			claimed:       p4ClaimedWork(),
			failureResult: state.DirtyScopeWork{WorkState: tc.wantState},
		}
		sc := &countingScanner{err: scan.NewScopeError(tc.kind, errors.New("boom"))}
		ex, err := incrementalexec.New(st, sc, p4Config(), func() time.Time { return p4Now })
		if err != nil {
			t.Fatal(err)
		}
		res, err := ex.ExecuteOne(context.Background())
		if !errors.Is(err, incrementalexec.ErrScannerFailed) {
			t.Fatalf("%s: want ErrScannerFailed, got %v", tc.kind, err)
		}
		if st.failClass != state.ErrorClass(tc.kind) {
			t.Fatalf("%s: CompleteFailure class = %s", tc.kind, st.failClass)
		}
		if (st.failRetry != nil) != tc.wantRetry {
			t.Fatalf("%s: retryNotBefore=%v wantRetry=%v", tc.kind, st.failRetry, tc.wantRetry)
		}
		if tc.wantRetry && !st.failRetry.After(p4Now) {
			t.Fatalf("%s: retry eligibility must be strictly in the future", tc.kind)
		}
		if res.FinalWorkState != tc.wantState {
			t.Fatalf("%s: final work state = %s, want %s", tc.kind, res.FinalWorkState, tc.wantState)
		}
		if n := atomic.LoadInt32(&sc.calls); n != 1 {
			t.Fatalf("%s: exactly one scan allowed, got %d", tc.kind, n)
		}
	}
}
func TestP4NonVerifyingOutcomeDoesNotSucceed(t *testing.T) {
	for _, status := range []domain.AdmissionStatus{
		domain.AdmissionStaleInput, domain.AdmissionRejected, "",
	} {
		st := &fakeStore{
			next:          state.DirtyScopeWork{RootID: p4RootID, ScopeKey: "/a", WorkState: state.WorkPending, Version: 3},
			found:         true,
			claimed:       p4ClaimedWork(),
			failureResult: state.DirtyScopeWork{WorkState: state.WorkRetryWait},
		}
		sc := &countingScanner{result: scan.Result{
			SnapshotID: "snap-x",
			Outcome:    postgres.ReconcileOutcome{Status: status},
		}}
		ex, err := incrementalexec.New(st, sc, p4Config(), func() time.Time { return p4Now })
		if err != nil {
			t.Fatal(err)
		}
		res, err := ex.ExecuteOne(context.Background())
		if !errors.Is(err, incrementalexec.ErrScannerFailed) {
			t.Fatalf("status %q: want ErrScannerFailed, got %v", status, err)
		}
		if n := atomic.LoadInt32(&st.successCalls); n != 0 {
			t.Fatalf("status %q: must not CompleteSuccess", status)
		}
		if n := atomic.LoadInt32(&st.failureCalls); n != 1 {
			t.Fatalf("status %q: must CompleteFailure exactly once, got %d", status, n)
		}
		if st.failClass != state.ErrorInternal {
			t.Fatalf("status %q: class = %s, want INTERNAL", status, st.failClass)
		}
		if res.FinalWorkState != state.WorkRetryWait {
			t.Fatalf("status %q: final state = %s", status, res.FinalWorkState)
		}
	}
}

func TestP4AppliedAndNoopAreAccepted(t *testing.T) {
	for _, status := range []domain.AdmissionStatus{domain.AdmissionApplied, domain.AdmissionNoop} {
		st := &fakeStore{
			next:          state.DirtyScopeWork{RootID: p4RootID, ScopeKey: "/a", WorkState: state.WorkPending, Version: 3},
			found:         true,
			claimed:       p4ClaimedWork(),
			successResult: state.DirtyScopeWork{WorkState: state.WorkVerified},
		}
		sc := &countingScanner{result: scan.Result{
			SnapshotID: "snap-1",
			Outcome:    postgres.ReconcileOutcome{Status: status},
		}}
		ex, err := incrementalexec.New(st, sc, p4Config(), func() time.Time { return p4Now })
		if err != nil {
			t.Fatal(err)
		}
		res, err := ex.ExecuteOne(context.Background())
		if err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
		if n := atomic.LoadInt32(&st.successCalls); n != 1 {
			t.Fatalf("status %q: CompleteSuccess calls = %d", status, n)
		}
		if res.FinalWorkState != state.WorkVerified {
			t.Fatalf("status %q: final state = %s", status, res.FinalWorkState)
		}
	}
}

func TestP4PostScanCancellationLeavesInflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st := &fakeStore{
		next:    state.DirtyScopeWork{RootID: p4RootID, ScopeKey: "/a", WorkState: state.WorkPending, Version: 3},
		found:   true,
		claimed: p4ClaimedWork(),
	}
	// The scanner itself succeeds, but cancellation becomes observable before
	// the executor reaches completion.
	sc := &countingScanner{fn: func(context.Context, string, string, int) (scan.Result, error) {
		cancel()
		return p4AppliedResult(), nil
	}}
	ex, err := incrementalexec.New(st, sc, p4Config(), func() time.Time { return p4Now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ex.ExecuteOne(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if n := atomic.LoadInt32(&st.successCalls); n != 0 {
		t.Fatalf("must not CompleteSuccess after cancellation, got %d", n)
	}
	if n := atomic.LoadInt32(&st.failureCalls); n != 0 {
		t.Fatalf("must not CompleteFailure after cancellation, got %d", n)
	}
}

func TestP4FailureFinalStateFromCompletionRow(t *testing.T) {
	st := &fakeStore{
		next:          state.DirtyScopeWork{RootID: p4RootID, ScopeKey: "/a", WorkState: state.WorkPending, Version: 3},
		found:         true,
		claimed:       p4ClaimedWork(),
		failureResult: state.DirtyScopeWork{WorkState: state.WorkBlocked},
	}
	sc := &countingScanner{err: scan.NewScopeError(scan.ScopeFailureAuthOrPermission, errors.New("denied"))}
	ex, err := incrementalexec.New(st, sc, p4Config(), func() time.Time { return p4Now })
	if err != nil {
		t.Fatal(err)
	}
	res, err := ex.ExecuteOne(context.Background())
	if !errors.Is(err, incrementalexec.ErrScannerFailed) {
		t.Fatalf("want ErrScannerFailed, got %v", err)
	}
	if res.FinalWorkState != state.WorkBlocked {
		t.Fatalf("FinalWorkState must come from the committed completion row, got %s", res.FinalWorkState)
	}
}
