package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalhint"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/hintapi"
)

// buildIncrementalCycle composes the accepted P4 -> P5 -> P6 chain used by the P10
// hybrid runtime. It is a package-level seam so tests can inject a controlled
// cycle runner. The P4/P6 prototype caps stay at their accepted defaults.
var buildIncrementalCycle = func(st *postgres.Store, cfg config.Config, logger *slog.Logger) (incrementalruntime.CycleRunner, error) {
	scanner := scan.New(st, cfg.RclonePath, cfg.RcloneConfig, cfg.ScanTimeout, logger)
	one, err := incrementalexec.New(st, scanner, incrementalexec.Config{
		MaxEntriesPerScope: 1000,
		Retry: incrementalexec.RetryPolicy{
			TransientProvider: 30 * time.Second,
			Throttled:         45 * time.Second,
			Internal:          60 * time.Second,
		},
	}, nil)
	if err != nil {
		return nil, err
	}
	p5, err := incrementalexec.NewCycleRunner(one)
	if err != nil {
		return nil, err
	}
	return incrementalorch.NewRunner(st, p5, nil)
}

// incrementalRuntime is the app-layer P10 runtime seam.
// *incrementalruntime.Runtime satisfies it; tests may inject a controlled runtime.
type incrementalRuntime interface {
	RecoverStartupInflight(ctx context.Context) (int, error)
	Run(ctx context.Context) error
	Wake()
}

// newIncrementalRuntime is the P10 runtime seam.
var newIncrementalRuntime = func(store incrementalruntime.MaintenanceStore, cycle incrementalruntime.CycleRunner, cfg incrementalruntime.Config, logger *slog.Logger) (incrementalRuntime, error) {
	return incrementalruntime.New(store, cycle, cfg, logger, nil)
}

// runtimeConfig builds the P10 runtime config from the global config.
func runtimeConfig(cfg config.Config) incrementalruntime.Config {
	return incrementalruntime.Config{
		WakeInterval:       cfg.IncrementalWakeInterval,
		MaxRetryPromotions: incrementalruntime.MaxRetryPromotionsCap,
		Cycle:              incrementalruntime.DefaultCycleConfig(),
	}
}

// notifyingIngester wakes the P10 runtime after a successful durable P8 merge. It
// never changes the HTTP 202 semantics (P8 success = durable merge only) and never
// blocks on P6/provider execution. hintapi.Deps gains no executor/runtime
// dependency; the decorator lives in the app layer.
type notifyingIngester struct {
	inner hintapi.Ingester
	wake  func()
}

func (n notifyingIngester) IngestOne(ctx context.Context, req incrementalhint.Request) (state.DirtyScopeWork, error) {
	wk, err := n.inner.IngestOne(ctx, req)
	if err == nil && n.wake != nil {
		n.wake()
	}
	return wk, err
}

// joinIncremental waits for the P10 runtime to stop. If the graceful timeout
// expires it keeps waiting, so the writer lock is never released while incremental
// execution may still be active (G3-R2.4 / P10 §16).
func joinIncremental(done <-chan error, timeout time.Duration, logger *slog.Logger) {
	if done == nil {
		return
	}
	select {
	case <-done:
		return
	case <-time.After(timeout):
		logger.Error("shutdown timeout: incremental runtime still running; holding the writer lock until it stops",
			"shutdown_timeout", timeout.String())
	}
	<-done
}
