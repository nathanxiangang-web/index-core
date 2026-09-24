package incrementalexec

import "errors"

var (
	// ErrNoEligibleWork means no eligible PENDING item existed. It is not an
	// operational failure: no state mutated and no provider call happened.
	ErrNoEligibleWork = errors.New("no eligible dirty work")

	// ErrStaleSelection means the selected work changed before ClaimWork. No scan
	// happens and no other item is selected in the same invocation.
	ErrStaleSelection = errors.New("stale work selection")

	// ErrScannerFailed wraps a classified scoped-refresh failure whose
	// CompleteFailure transition committed.
	ErrScannerFailed = errors.New("scoped refresh failed")

	// ErrCompletionFailed means the durable completion transition could not
	// commit. The executor never re-runs provider I/O to repair it.
	ErrCompletionFailed = errors.New("work completion failed")
)
