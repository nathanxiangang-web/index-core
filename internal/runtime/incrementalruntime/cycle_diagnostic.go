package incrementalruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
)

// Finite classifications and one-way correlation only. No err.Error(), SQL,
// provider response, URL, raw RootID, scope path or source name is logged.
func cycleFailureFields(res incrementalorch.Result, err error) []any {
	stop := "UNKNOWN"
	switch res.Executor.StopReason {
	case incrementalexec.StopInternalItemFailure, incrementalexec.StopSystemicError,
		incrementalexec.StopContextCancelled, incrementalexec.StopStaleSelection,
		incrementalexec.StopMaxWallTime, incrementalexec.StopMaxItems, incrementalexec.StopNoEligibleWork:
		stop = string(res.Executor.StopReason)
	}
	class := "NONE"
	if c := res.Executor.Last.FailureClass; c != nil {
		class = "UNKNOWN"
		switch *c {
		case state.ErrorInternal, state.ErrorTransientProvider, state.ErrorThrottled,
			state.ErrorAuthOrPermission, state.ErrorScopeTooLarge, state.ErrorInvalidScope,
			state.ErrorRootInactive, state.ErrorConfigInvalid:
			class = string(*c)
		}
	}
	family := "UNCLASSIFIED"
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		family = "CONTEXT"
	case errors.Is(err, incrementalexec.ErrCompletionFailed):
		family = "COMPLETION_FAILURE"
	case errors.Is(err, incrementalexec.ErrScannerFailed):
		family = "SCANNER_FAILURE"
	case errors.Is(err, incrementalexec.ErrStaleSelection):
		family = "STALE_SELECTION"
	}
	sqlstate := "UNKNOWN"
	var sqlerr interface{ SQLState() string }
	if errors.As(err, &sqlerr) {
		code := sqlerr.SQLState()
		valid := len(code) == 5
		for _, c := range code {
			valid = valid && (c >= '0' && c <= '9' || c >= 'A' && c <= 'Z')
		}
		if valid {
			sqlstate = code
		}
	}
	token := func(value string) string {
		if value == "" {
			return ""
		}
		return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))[:20]
	}
	return []any{"p6_stop_reason", stop, "failure_class", class, "error_family", family,
		"sql_state", sqlstate, "root_fingerprint", token(res.Executor.Last.RootID),
		"scope_fingerprint", token(res.Executor.Last.ScopeKey),
		"snapshot_fingerprint", token(res.Executor.Last.SnapshotID),
		"claimed_signal_seq", res.Executor.Last.ClaimedSignalSeq}
}
