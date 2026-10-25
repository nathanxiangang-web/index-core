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
	ReadWorkers          int
	MirrorRevisitEnabled bool
	MaxDueWatchAttempts  int
	MaxExecuteItems      int
	MaxWallTime          time.Duration
}

// Validate enforces the prototype hard caps. An invalid config fails before any
// Store due-watch query, EmitDuePoll, or P5 execution.
func (c Config) Validate() error {
	if c.MirrorRevisitEnabled && c.MaxDueWatchAttempts < 2 {
		return fmt.Errorf("mirror revisit fairness requires at least two due-watch attempts")
	}
	if c.MaxDueWatchAttempts < 1 || c.MaxDueWatchAttempts > MaxDueWatchAttemptsCap {
		return fmt.Errorf("orchestration: max_due_watch_attempts must be within [1, %d], got %d",
			MaxDueWatchAttemptsCap, c.MaxDueWatchAttempts)
	}
	cap := MaxExecuteItemsCap
	if c.ReadWorkers != 0 && c.ReadWorkers != 1 {
		if c.ReadWorkers != 2 && c.ReadWorkers != 4 && c.ReadWorkers != 8 {
			return fmt.Errorf("invalid pool reader count")
		}
		cap = 8
	}
	if c.MaxExecuteItems < 1 || c.MaxExecuteItems > cap {
		return fmt.Errorf("orchestration: max_execute_items must be within [1, %d], got %d",
			cap, c.MaxExecuteItems)
	}
	if c.MaxWallTime <= 0 || c.MaxWallTime > MaxWallTimeCap {
		return fmt.Errorf("orchestration: max_wall_time must be within (0, %s], got %s",
			MaxWallTimeCap, c.MaxWallTime)
	}
	return nil
}
