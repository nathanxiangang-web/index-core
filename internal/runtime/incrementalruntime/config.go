// Package incrementalruntime is the P10 bounded same-process hybrid incremental
// runtime. It owns exactly one serialized event loop that repeatedly invokes the
// accepted P6 RunCycle under the existing serve writer lock, performs bounded
// auto-promotion of approved transient retry rows, recovers stale IN_FLIGHT work,
// and never overlaps cycles. It contains no second writer, no public write API,
// no CLI, no native provider delta, and no migration.
package incrementalruntime

import (
	"fmt"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
)

// Prototype hard safety caps. These are structural bounds, not production
// throughput targets or provider polling cadence.
const (
	MaxRetryPromotionsCap        = 5
	MaxConsecutiveCyclesPerBurst = 4
	BurstCooldown                = time.Second
	InflightRecoveryBatch        = 100
	MinWakeInterval              = time.Second
	MaxWakeInterval              = 60 * time.Second
)

// Config is the finite runtime configuration.
type Config struct {
	WakeInterval       time.Duration
	MaxRetryPromotions int
	Cycle              incrementalorch.Config
}

// DefaultCycleConfig returns the accepted P7/P10 prototype composition defaults.
func DefaultCycleConfig() incrementalorch.Config {
	return incrementalorch.Config{
		MaxDueWatchAttempts: 5,
		MaxExecuteItems:     5,
		MaxWallTime:         60 * time.Second,
	}
}

// Validate enforces the prototype bounds. An invalid config fails before any
// runtime goroutine, timer, or Store call starts.
func (c Config) Validate() error {
	if c.WakeInterval < MinWakeInterval || c.WakeInterval > MaxWakeInterval {
		return fmt.Errorf("incrementalruntime: wake interval must be within [%s, %s], got %s",
			MinWakeInterval, MaxWakeInterval, c.WakeInterval)
	}
	if c.MaxRetryPromotions < 1 || c.MaxRetryPromotions > MaxRetryPromotionsCap {
		return fmt.Errorf("incrementalruntime: max retry promotions must be within [1, %d], got %d",
			MaxRetryPromotionsCap, c.MaxRetryPromotions)
	}
	return c.Cycle.Validate()
}
