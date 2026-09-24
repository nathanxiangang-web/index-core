package alist

import "fmt"

// ScopedErrorKind is the provider-level classification of a failed P0 scoped
// refresh. It exists so the scan layer never has to parse error strings or
// extract HTTP codes: it can map a typed kind instead.
type ScopedErrorKind string

const (
	ScopedAuthOrPermission  ScopedErrorKind = "AUTH_OR_PERMISSION"
	ScopedThrottled         ScopedErrorKind = "THROTTLED"
	ScopedTransientProvider ScopedErrorKind = "TRANSIENT_PROVIDER"
	ScopedTooLarge          ScopedErrorKind = "SCOPE_TOO_LARGE"
	ScopedInvalidScope      ScopedErrorKind = "INVALID_SCOPE"
	ScopedConfigInvalid     ScopedErrorKind = "CONFIG_INVALID"
)

// ScopedError carries a typed scoped-refresh failure.
type ScopedError struct {
	Kind ScopedErrorKind
	Err  error
}

func (e *ScopedError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return "alist scoped refresh: " + string(e.Kind)
	}
	return fmt.Sprintf("alist scoped refresh: %s: %v", e.Kind, e.Err)
}

func (e *ScopedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func scopedErrorf(kind ScopedErrorKind, format string, args ...any) *ScopedError {
	return &ScopedError{Kind: kind, Err: fmt.Errorf(format, args...)}
}

func scopedWrap(kind ScopedErrorKind, err error) *ScopedError {
	return &ScopedError{Kind: kind, Err: err}
}

// scopedKindFromAPICode maps an AList provider response code onto the typed
// scoped classification.
func scopedKindFromAPICode(code int) ScopedErrorKind {
	switch {
	case code == 401 || code == 403:
		return ScopedAuthOrPermission
	case code == 429:
		return ScopedThrottled
	default:
		// 5xx, malformed, truncation and any other provider-side failure are
		// conservatively retryable.
		return ScopedTransientProvider
	}
}
