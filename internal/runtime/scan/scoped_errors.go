package scan

import (
	"fmt"

	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
)

// ScopeFailureKind is the provider-neutral classification of a failed scoped
// refresh. It is the only thing the P4 executor inspects: no substring matching
// or HTTP-code extraction from formatted messages is permitted.
type ScopeFailureKind string

const (
	ScopeFailureTransientProvider ScopeFailureKind = "TRANSIENT_PROVIDER"
	ScopeFailureThrottled         ScopeFailureKind = "THROTTLED"
	ScopeFailureAuthOrPermission  ScopeFailureKind = "AUTH_OR_PERMISSION"
	ScopeFailureTooLarge          ScopeFailureKind = "SCOPE_TOO_LARGE"
	ScopeFailureInvalidScope      ScopeFailureKind = "INVALID_SCOPE"
	ScopeFailureRootInactive      ScopeFailureKind = "ROOT_INACTIVE"
	ScopeFailureConfigInvalid     ScopeFailureKind = "CONFIG_INVALID"
	ScopeFailureInternal          ScopeFailureKind = "INTERNAL"
)

// MaxScopedEntries re-exports the P0 hard cap so runtime callers can enforce the
// accepted bound without importing a provider adapter package.
const MaxScopedEntries = alist.MaxScopedEntries

// ScopeError is a typed scoped-refresh failure. Its Kind is exhaustive and lets
// a caller map it to durable state without string parsing.
type ScopeError struct {
	Kind ScopeFailureKind
	Err  error
}

// NewScopeError builds a typed scoped error. It is exported so deterministic
// test scanners can reproduce any classification without a real provider.
func NewScopeError(kind ScopeFailureKind, err error) *ScopeError {
	return &ScopeError{Kind: kind, Err: err}
}

func (e *ScopeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return "scoped refresh: " + string(e.Kind)
	}
	return fmt.Sprintf("scoped refresh: %s: %v", e.Kind, e.Err)
}

func (e *ScopeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func scopeErrorf(kind ScopeFailureKind, format string, args ...any) *ScopeError {
	return &ScopeError{Kind: kind, Err: fmt.Errorf(format, args...)}
}

func scopeWrap(kind ScopeFailureKind, err error) *ScopeError {
	return &ScopeError{Kind: kind, Err: err}
}
