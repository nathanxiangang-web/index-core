package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Incremental dispatches the `indexcore incremental` command family. Only the
// one-shot `run` subcommand is implemented in P7; watch/daemon/recover/hint are
// intentionally reserved and fail as unknown, and no second binary exists.
func Incremental(ctx context.Context, base config.Config, logger *slog.Logger, args []string) error {
	if len(args) == 0 {
		return errors.New(`incremental: missing subcommand; expected "run"`)
	}
	switch args[0] {
	case "run":
		return RunIncremental(ctx, base, logger, args[1:])
	default:
		return fmt.Errorf("incremental: unknown subcommand %q (only \"run\" is implemented)", args[0])
	}
}

// RunIncremental implements `indexcore incremental run [flags]`: exactly one
// bounded, manual P6 orchestration cycle under the existing single-writer
// advisory lock, followed by exactly one structured JSON result on stdout.
func RunIncremental(ctx context.Context, base config.Config, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("incremental run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := base
	cfg.RegisterFlags(fs)
	maxDueWatchAttempts := fs.Int("max-due-watch-attempts", 5, "bounded due-watch materialization attempts (1..5)")
	maxExecuteItems := fs.Int("max-execute-items", 5, "bounded executor items per cycle (1..5)")
	maxWallTime := fs.Duration("max-wall-time", 60*time.Second, "overall cycle wall-time budget (0..60s]")
	maxEntriesPerScope := fs.Int("max-entries-per-scope", 1000, "scoped observation max_entries bound")
	retryTransientProvider := fs.Duration("retry-transient-provider", 30*time.Second, "RETRY_WAIT delay for transient provider failures")
	retryThrottled := fs.Duration("retry-throttled", 45*time.Second, "RETRY_WAIT delay for throttled failures")
	retryInternal := fs.Duration("retry-internal", 60*time.Second, "RETRY_WAIT delay for internal failures")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// flag.FlagSet stops parsing at the first positional token, so a stray
	// positional argument could leave later flags (e.g. an invalid budget)
	// unparsed and let the command run with defaults. Fail closed before any
	// database, writer-lock, or provider work.
	if fs.NArg() != 0 {
		return fmt.Errorf("incremental run: unexpected positional arguments %v (this command accepts flags only)", fs.Args())
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	p4cfg := incrementalexec.Config{
		MaxEntriesPerScope: *maxEntriesPerScope,
		Retry: incrementalexec.RetryPolicy{
			TransientProvider: *retryTransientProvider,
			Throttled:         *retryThrottled,
			Internal:          *retryInternal,
		},
	}
	p6cfg := incrementalorch.Config{
		MaxDueWatchAttempts: *maxDueWatchAttempts,
		MaxExecuteItems:     *maxExecuteItems,
		MaxWallTime:         *maxWallTime,
	}
	// Fail before opening the database, acquiring writer ownership, constructing
	// the chain, or touching the provider.
	if err := p4cfg.Validate(); err != nil {
		return err
	}
	if err := p6cfg.Validate(); err != nil {
		return err
	}

	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	// Schema preflight: never auto-migrate. A missing/future/incompatible schema
	// fails closed before any incremental mutation.
	state, err := postgres.SchemaStatus(ctx, pool)
	if err != nil {
		return fmt.Errorf("schema status: %w", err)
	}
	if !state.Compatible() {
		return postgres.SchemaError(state)
	}

	st := postgres.New(pool)

	// Single-writer ownership: an active `indexcore serve` writer excludes this
	// command. Do not wait/spin/retry; fail closed before any provider work.
	lock, err := st.AcquireWriterLock(ctx)
	if err != nil {
		return fmt.Errorf("single-writer ownership: %w", err)
	}
	defer func() {
		// Release on a fresh bounded background context so a cancelled command
		// context cannot strand the dedicated advisory-lock connection.
		releaseCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		lock.Release(releaseCtx)
	}()

	res, cycleErr := runIncrementalCycle(ctx, st, cfg, logger, p4cfg, p6cfg)

	// Exactly one structured JSON object is emitted after P6 ran, even when P6
	// also returned a runtime error. JSON delivery is part of command success: a
	// serialization or stdout failure is returned as an error (exit 1), and when
	// P6 also failed both errors are preserved in the returned chain.
	writeErr := writeIncrementalJSON(newIncrementalRunDTO(res))
	return errors.Join(cycleErr, writeErr)
}

// runIncrementalCycle composes the accepted Scan -> P4 -> P5 -> P6 chain and
// invokes P6 exactly once. It is a package-level seam so tests can prove the
// JSON/error ordering without a real provider; production always builds the
// real chain here.
var runIncrementalCycle = func(ctx context.Context, st *postgres.Store, cfg config.Config, logger *slog.Logger, p4cfg incrementalexec.Config, p6cfg incrementalorch.Config) (incrementalorch.Result, error) {
	scanner := scan.New(st, cfg.RclonePath, cfg.RcloneConfig, cfg.ScanTimeout, logger)
	one, err := incrementalexec.New(st, scanner, p4cfg, nil)
	if err != nil {
		return incrementalorch.Result{}, fmt.Errorf("construct executor: %w", err)
	}
	cycle, err := incrementalexec.NewCycleRunner(one)
	if err != nil {
		return incrementalorch.Result{}, fmt.Errorf("construct cycle runner: %w", err)
	}
	orch, err := incrementalorch.NewRunner(st, cycle, nil)
	if err != nil {
		return incrementalorch.Result{}, fmt.Errorf("construct orchestrator: %w", err)
	}
	return orch.RunCycle(ctx, p6cfg)
}

// incrementalStdout is a test seam; production writes one JSON line to stdout.
var incrementalStdout io.Writer = os.Stdout

// writeIncrementalJSON renders one JSON line to stdout and reports both
// serialization and write failures: delivering the operator JSON is part of
// command success, so a stdout/broken-pipe failure must not exit 0.
func writeIncrementalJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode incremental result: %w", err)
	}
	if _, err := fmt.Fprintln(incrementalStdout, string(b)); err != nil {
		return fmt.Errorf("write incremental result: %w", err)
	}
	return nil
}

// incrementalRunDTO is the stable, snake_case operator contract for one manual
// cycle. It carries no database DSN, provider credentials, adapter JSON, or
// environment values.
type incrementalRunDTO struct {
	Command    string                 `json:"command"`
	StopReason string                 `json:"stop_reason"`
	StartedAt  string                 `json:"started_at"`
	FinishedAt string                 `json:"finished_at"`
	ObservedAt string                 `json:"observed_at"`
	Due        incrementalDueDTO      `json:"due"`
	Executor   incrementalExecutorDTO `json:"executor"`
}

type incrementalDueDTO struct {
	Candidates                 int  `json:"candidates"`
	Attempted                  int  `json:"attempted"`
	Emitted                    int  `json:"emitted"`
	Stale                      int  `json:"stale"`
	MoreDueWatches             bool `json:"more_due_watches"`
	MaterializationInterrupted bool `json:"materialization_interrupted"`
}

type incrementalExecutorDTO struct {
	Ran                 bool                `json:"ran"`
	StopReason          string              `json:"stop_reason"`
	Invocations         int                 `json:"invocations"`
	SelectedItems       int                 `json:"selected_items"`
	Succeeded           int                 `json:"succeeded"`
	Failed              int                 `json:"failed"`
	InterruptedInFlight bool                `json:"interrupted_in_flight"`
	Last                *incrementalLastDTO `json:"last"`
}

type incrementalLastDTO struct {
	Selected       bool    `json:"selected"`
	RootID         string  `json:"root_id"`
	ScopeKey       string  `json:"scope_key"`
	SnapshotID     string  `json:"snapshot_id"`
	FinalWorkState string  `json:"final_work_state"`
	FailureClass   *string `json:"failure_class"`
}

func newIncrementalRunDTO(res incrementalorch.Result) incrementalRunDTO {
	dto := incrementalRunDTO{
		Command:    "incremental run",
		StopReason: string(res.StopReason),
		StartedAt:  formatIncrementalTime(res.StartedAt),
		FinishedAt: formatIncrementalTime(res.FinishedAt),
		ObservedAt: formatIncrementalTime(res.ObservedAt),
		Due: incrementalDueDTO{
			Candidates:                 res.DueCandidates,
			Attempted:                  res.DueAttempted,
			Emitted:                    res.DueEmitted,
			Stale:                      res.DueStale,
			MoreDueWatches:             res.MoreDueWatches,
			MaterializationInterrupted: res.MaterializationInterrupted,
		},
		Executor: incrementalExecutorDTO{
			Ran:                 res.ExecutorRan,
			StopReason:          string(res.Executor.StopReason),
			Invocations:         res.Executor.Invocations,
			SelectedItems:       res.Executor.SelectedItems,
			Succeeded:           res.Executor.Succeeded,
			Failed:              res.Executor.Failed,
			InterruptedInFlight: res.Executor.InterruptedInFlight,
		},
	}
	if res.Executor.Last.Selected {
		last := &incrementalLastDTO{
			Selected:       true,
			RootID:         res.Executor.Last.RootID,
			ScopeKey:       res.Executor.Last.ScopeKey,
			SnapshotID:     res.Executor.Last.SnapshotID,
			FinalWorkState: string(res.Executor.Last.FinalWorkState),
		}
		if res.Executor.Last.FailureClass != nil {
			class := string(*res.Executor.Last.FailureClass)
			last.FailureClass = &class
		}
		dto.Executor.Last = last
	}
	return dto
}

func formatIncrementalTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
