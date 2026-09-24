// Package incrementalorch is the P6 bounded scheduler-orchestration prototype.
// One manually-invoked RunCycle materializes a bounded snapshot of persisted due
// watches through the accepted P3 EmitDuePoll transaction and then runs exactly
// one accepted P5 bounded executor cycle. It contains no ticker, cadence, sleep,
// daemon, background scheduler, API, or CLI.
package incrementalorch

import (
	"fmt"
	"time"
)

// Prototype hard safety caps. These are structural bounds, not production SLA or
// polling cadence.
const (
	MaxDueWatchAttemptsCap = 5
	MaxExecuteItemsCap     = 5
	MaxWallTimeCap         = 60 * time.Second
)

// Config is the finite budget for one manual orchestration cycle.
type Config struct {
	MaxDueWatchAttempts int
	MaxExecuteItems     int
	MaxWallTime         time.Duration
}

// Validate enforces the prototype hard caps. An invalid config fails before any
// Store due-watch query, EmitDuePoll, or P5 execution.
func (c Config) Validate() error {
	if c.MaxDueWatchAttempts < 1 || c.MaxDueWatchAttempts > MaxDueWatchAttemptsCap {
		return fmt.Errorf("orchestration: max_due_watch_attempts must be within [1, %d], got %d",
			MaxDueWatchAttemptsCap, c.MaxDueWatchAttempts)
	}
	if c.MaxExecuteItems < 1 || c.MaxExecuteItems > MaxExecuteItemsCap {
		return fmt.Errorf("orchestration: max_execute_items must be within [1, %d], got %d",
			MaxExecuteItemsCap, c.MaxExecuteItems)
	}
	if c.MaxWallTime <= 0 || c.MaxWallTime > MaxWallTimeCap {
		return fmt.Errorf("orchestration: max_wall_time must be within (0, %s], got %s",
			MaxWallTimeCap, c.MaxWallTime)
	}
	return nil
}
