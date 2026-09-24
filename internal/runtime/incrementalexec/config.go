// Package incrementalexec is the P4 one-shot durable dirty-work executor
// prototype. One ExecuteOne invocation processes zero or one eligible PENDING
// DirtyScopeWork item and calls the existing P0 scan.Service.ScanScope at most
// once. It contains no scheduler, loop, sleep, daemon, API, or CLI.
package incrementalexec

import (
	"fmt"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
)

// RetryPolicy holds the strictly positive eligibility delays for the retryable
// failure classes. P4 only computes a future timestamp; it never sleeps and
// never retries a provider call inline.
type RetryPolicy struct {
	TransientProvider time.Duration
	Throttled         time.Duration
	Internal          time.Duration
}

// DelayFor returns the configured delay for a retryable class.
func (p RetryPolicy) DelayFor(class state.ErrorClass) (time.Duration, bool) {
	switch class {
	case state.ErrorTransientProvider:
		return p.TransientProvider, true
	case state.ErrorThrottled:
		return p.Throttled, true
	case state.ErrorInternal:
		return p.Internal, true
	default:
		return 0, false
	}
}

// Validate rejects non-positive retry delays.
func (p RetryPolicy) Validate() error {
	switch {
	case p.TransientProvider <= 0:
		return fmt.Errorf("retry policy: transient_provider delay must be > 0, got %s", p.TransientProvider)
	case p.Throttled <= 0:
		return fmt.Errorf("retry policy: throttled delay must be > 0, got %s", p.Throttled)
	case p.Internal <= 0:
		return fmt.Errorf("retry policy: internal delay must be > 0, got %s", p.Internal)
	}
	return nil
}

// Config is the executor's explicit configuration: one max_entries bound and
// three retry delays. There is no hidden scheduler cadence.
type Config struct {
	MaxEntriesPerScope int
	Retry              RetryPolicy
}

// Validate fails before any selection or claim when the configuration is
// invalid: max_entries must be inside the accepted P0 hard cap and every retry
// delay strictly positive.
func (c Config) Validate() error {
	if c.MaxEntriesPerScope < 0 || c.MaxEntriesPerScope > scan.MaxScopedEntries {
		return fmt.Errorf("max_entries_per_scope must be within [0, %d], got %d",
			scan.MaxScopedEntries, c.MaxEntriesPerScope)
	}
	return c.Retry.Validate()
}
