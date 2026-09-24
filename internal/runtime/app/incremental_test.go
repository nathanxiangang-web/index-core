package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func p7Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func p7BaseConfig() config.Config {
	c := config.Defaults()
	c.DatabaseURL = testutil.TestDSN()
	return c
}

func p7PrepareSchema(t *testing.T) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func p7CaptureStdout(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := incrementalStdout
	incrementalStdout = &buf
	t.Cleanup(func() { incrementalStdout = prev })
	return &buf
}

type p7CycleFn func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error)

func p7StubCycle(t *testing.T, fn p7CycleFn) *int32 {
	t.Helper()
	prev := runIncrementalCycle
	var calls int32
	runIncrementalCycle = func(ctx context.Context, st *postgres.Store, cfg config.Config, logger *slog.Logger, p4 incrementalexec.Config, p6 incrementalorch.Config) (incrementalorch.Result, error) {
		atomic.AddInt32(&calls, 1)
		return fn(ctx, st, cfg, logger, p4, p6)
	}
	t.Cleanup(func() { runIncrementalCycle = prev })
	return &calls
}

func p7ParseOneJSON(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("exactly one JSON line required, got %d: %q", len(lines), buf.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("stdout must be one valid JSON object: %v (%q)", err, lines[0])
	}
	return got
}

func p7AssertLockFree(t *testing.T, ctx context.Context) {
	t.Helper()
	pool := testutil.Pool(t)
	lock, err := postgres.New(pool).AcquireWriterLock(ctx)
	if err != nil {
		t.Fatalf("writer lock was not released: %v", err)
	}
	lock.Release(ctx)
}

func p7NewAListServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var refresh int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path    string `json:"path"`
			Refresh bool   `json:"refresh"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Refresh {
			atomic.AddInt32(&refresh, 1)
		}
		content := []map[string]any{}
		if req.Path == "/" {
			content = []map[string]any{{
				"name": "a.txt", "size": 5, "is_dir": false,
				"modified": "2026-01-02T03:04:05Z", "hash_info": map[string]string{"sha1": "aaa"},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"content": content, "total": len(content)},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &refresh
}

func p7SeedRoot(t *testing.T, st *postgres.Store, ctx context.Context, rootID, baseURL string) {
	t.Helper()
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	acfg, _ := json.Marshal(map[string]string{"base_url": baseURL, "path": "/"})
	if err := st.UpsertAdapterConfig(ctx, rootID,
		postgres.AdapterConfig{CollectorKind: "alist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}
}

func p7SeedWatch(t *testing.T, st *postgres.Store, ctx context.Context, rootID, scopeKey string, dueAt time.Time) {
	t.Helper()
	interval := int64(120)
	d := dueAt
	if _, err := st.CreateWatch(ctx, state.ScopeWatchState{
		RootID: rootID, ScopeKey: scopeKey, WatchState: state.WatchHot,
		CadenceClass: "HOT_120", EffectiveIntervalSeconds: &interval,
		SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy},
		Priority:  state.PriorityNormal, NextDueAt: &d,
	}); err != nil {
		t.Fatalf("create watch: %v", err)
	}
}

func TestP7IncrementalDispatch(t *testing.T) {
	base := p7BaseConfig()
	if err := Incremental(context.Background(), base, p7Logger(), nil); err == nil {
		t.Fatal("missing subcommand must fail")
	} else if !strings.Contains(err.Error(), "subcommand") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := Incremental(context.Background(), base, p7Logger(), []string{"watch"}); err == nil {
		t.Fatal("reserved/unknown subcommand must fail")
	} else if !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestP7IncrementalInvalidConfigPerformsNoWork(t *testing.T) {
	base := config.Defaults()
	base.DatabaseURL = "postgres://127.0.0.1:1/never"
	calls := p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		t.Error("orchestration must not run for invalid config")
		return incrementalorch.Result{}, nil
	})
	cases := [][]string{
		{"--max-due-watch-attempts", "0"},
		{"--max-due-watch-attempts", "6"},
		{"--max-execute-items", "0"},
		{"--max-execute-items", "6"},
		{"--max-wall-time", "0s"},
		{"--max-wall-time", "61s"},
		{"--max-entries-per-scope", "-1"},
		{"--max-entries-per-scope", "10001"},
		{"--retry-transient-provider", "0s"},
		{"--retry-throttled", "-1s"},
		{"--retry-internal", "0s"},
	}
	for _, args := range cases {
		err := RunIncremental(context.Background(), base, p7Logger(), args)
		if err == nil {
			t.Fatalf("%v must be rejected", args)
		}
		if strings.Contains(err.Error(), "open database") || strings.Contains(err.Error(), "ping database") {
			t.Fatalf("%v must fail before DB work, got %v", args, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("invalid config must make zero orchestrator calls, got %d", *calls)
	}
}

func TestP7IncrementalDefaults(t *testing.T) {
	p7PrepareSchema(t)
	p7CaptureStdout(t)
	var gotP4 incrementalexec.Config
	var gotP6 incrementalorch.Config
	calls := p7StubCycle(t, func(_ context.Context, _ *postgres.Store, _ config.Config, _ *slog.Logger, p4 incrementalexec.Config, p6 incrementalorch.Config) (incrementalorch.Result, error) {
		gotP4, gotP6 = p4, p6
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	})
	if err := RunIncremental(context.Background(), p7BaseConfig(), p7Logger(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("exactly one cycle required, got %d", *calls)
	}
	if gotP6.MaxDueWatchAttempts != 5 || gotP6.MaxExecuteItems != 5 || gotP6.MaxWallTime != 60*time.Second {
		t.Fatalf("P6 defaults = %+v, want 5/5/60s", gotP6)
	}
	if gotP4.MaxEntriesPerScope != 1000 {
		t.Fatalf("max-entries-per-scope default = %d, want 1000", gotP4.MaxEntriesPerScope)
	}
	if gotP4.Retry.TransientProvider != 30*time.Second ||
		gotP4.Retry.Throttled != 45*time.Second ||
		gotP4.Retry.Internal != 60*time.Second {
		t.Fatalf("retry defaults = %+v, want 30s/45s/60s", gotP4.Retry)
	}
}

func TestP7IncrementalJSONContractAndResultErrorOrdering(t *testing.T) {
	dsn := testutil.TestDSN()
	p7PrepareSchema(t)
	buf := p7CaptureStdout(t)
	sentinel := errors.New("cycle runtime failure")
	started := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	finished := started.Add(time.Second)
	class := state.ErrorAuthOrPermission
	p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		return incrementalorch.Result{
			StartedAt: started, FinishedAt: finished, ObservedAt: started,
			StopReason:     incrementalorch.StopCompleted,
			DueCandidates:  2,
			DueAttempted:   2,
			DueEmitted:     1,
			DueStale:       1,
			MoreDueWatches: true,
			ExecutorRan:    true,
			Executor: incrementalexec.CycleResult{
				StopReason:  incrementalexec.StopNoEligibleWork,
				Invocations: 2, SelectedItems: 1, Succeeded: 1, Failed: 0,
				Last: incrementalexec.Result{
					Selected: true, RootID: "root-1", ScopeKey: "/", SnapshotID: "snap-1",
					FinalWorkState: state.WorkVerified, FailureClass: &class,
				},
			},
		}, sentinel
	})
	err := RunIncremental(context.Background(), p7BaseConfig(), p7Logger(), nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("original error must be returned, got %v", err)
	}
	got := p7ParseOneJSON(t, buf)
	if got["command"] != "incremental run" || got["stop_reason"] != "COMPLETED" {
		t.Fatalf("unexpected command/stop_reason: %+v", got)
	}
	if got["started_at"] != started.Format(time.RFC3339Nano) || got["observed_at"] != started.Format(time.RFC3339Nano) {
		t.Fatalf("timestamps must be RFC3339Nano: %+v", got)
	}
	due, _ := got["due"].(map[string]any)
	if due["candidates"].(float64) != 2 || due["emitted"].(float64) != 1 || due["stale"].(float64) != 1 || due["more_due_watches"] != true {
		t.Fatalf("due shape = %+v", due)
	}
	exec, _ := got["executor"].(map[string]any)
	if exec["ran"] != true || exec["stop_reason"] != "NO_ELIGIBLE_WORK" || exec["invocations"].(float64) != 2 ||
		exec["selected_items"].(float64) != 1 || exec["succeeded"].(float64) != 1 {
		t.Fatalf("executor shape = %+v", exec)
	}
	last, _ := exec["last"].(map[string]any)
	if last["selected"] != true || last["final_work_state"] != "VERIFIED" ||
		last["failure_class"] != "AUTH_OR_PERMISSION" || last["snapshot_id"] != "snap-1" {
		t.Fatalf("last shape = %+v", last)
	}
	if strings.Contains(buf.String(), dsn) || strings.Contains(buf.String(), "indexcore:indexcore") {
		t.Fatalf("JSON must not contain the database DSN/credentials: %s", buf.String())
	}
}

func TestP7IncrementalNormalTimeoutIsZeroExit(t *testing.T) {
	p7PrepareSchema(t)
	buf := p7CaptureStdout(t)
	p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		return incrementalorch.Result{
			StopReason:  incrementalorch.StopMaxWallTime,
			ExecutorRan: true,
			Executor: incrementalexec.CycleResult{
				StopReason: incrementalexec.StopMaxWallTime, InterruptedInFlight: true,
			},
		}, nil
	})
	if err := RunIncremental(context.Background(), p7BaseConfig(), p7Logger(), nil); err != nil {
		t.Fatalf("normal bounded MAX_WALL_TIME must return nil (exit 0), got %v", err)
	}
	got := p7ParseOneJSON(t, buf)
	if got["stop_reason"] != "MAX_WALL_TIME" {
		t.Fatalf("stop_reason = %v", got["stop_reason"])
	}
	exec, _ := got["executor"].(map[string]any)
	if exec["interrupted_in_flight"] != true {
		t.Fatalf("interrupted_in_flight must be exposed: %+v", exec)
	}
}

func TestP7IncrementalErrorMapping(t *testing.T) {
	p7PrepareSchema(t)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"parent cancellation", context.Canceled},
		{"materialization error", errors.New("MATERIALIZATION_ERROR")},
		{"executor error", errors.New("EXECUTOR_ERROR")},
	} {
		p7CaptureStdout(t)
		sentinel := tc.err
		p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
			return incrementalorch.Result{}, sentinel
		})
		if err := RunIncremental(context.Background(), p7BaseConfig(), p7Logger(), nil); err == nil {
			t.Fatalf("%s must map to a non-nil error", tc.name)
		}
	}
}

func TestP7IncrementalWriterLockExclusion(t *testing.T) {
	p7PrepareSchema(t)
	ctx := context.Background()
	pool := testutil.Pool(t)
	lock, err := postgres.New(pool).AcquireWriterLock(ctx)
	if err != nil {
		t.Fatalf("acquire writer lock: %v", err)
	}
	calls := p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	})
	p7CaptureStdout(t)

	err = RunIncremental(ctx, p7BaseConfig(), p7Logger(), nil)
	if !errors.Is(err, postgres.ErrWriterLockHeld) {
		t.Fatalf("want ErrWriterLockHeld, got %v", err)
	}
	if *calls != 0 {
		t.Fatalf("writer-lock conflict must make zero orchestration calls, got %d", *calls)
	}

	lock.Release(ctx)
	if err := RunIncremental(ctx, p7BaseConfig(), p7Logger(), nil); err != nil {
		t.Fatalf("after release the command must proceed: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("exactly one cycle after release, got %d", *calls)
	}
}

func TestP7IncrementalSchemaFailClosed(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool) // migrated schema absent
	calls := p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		t.Error("orchestration must not run on an incompatible schema")
		return incrementalorch.Result{}, nil
	})
	p7CaptureStdout(t)

	err := RunIncremental(context.Background(), p7BaseConfig(), p7Logger(), nil)
	if err == nil {
		t.Fatal("missing schema must fail closed")
	}
	if *calls != 0 {
		t.Fatalf("schema failure must make zero orchestration calls, got %d", *calls)
	}
	if errors.Is(err, postgres.ErrWriterLockHeld) {
		t.Fatalf("schema check must precede writer acquisition: %v", err)
	}
}

func TestP7IncrementalReleasesWriterLock(t *testing.T) {
	p7PrepareSchema(t)
	ctx := context.Background()

	p7CaptureStdout(t)
	p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	})
	if err := RunIncremental(ctx, p7BaseConfig(), p7Logger(), nil); err != nil {
		t.Fatalf("success run: %v", err)
	}
	p7AssertLockFree(t, ctx)

	p7CaptureStdout(t)
	p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		return incrementalorch.Result{}, errors.New("boom")
	})
	if err := RunIncremental(ctx, p7BaseConfig(), p7Logger(), nil); err == nil {
		t.Fatal("runtime error must surface")
	}
	p7AssertLockFree(t, ctx)

	cctx, cancel := context.WithCancel(ctx)
	p7CaptureStdout(t)
	p7StubCycle(t, func(c context.Context, _ *postgres.Store, _ config.Config, _ *slog.Logger, _ incrementalexec.Config, _ incrementalorch.Config) (incrementalorch.Result, error) {
		cancel()
		return incrementalorch.Result{}, c.Err()
	})
	if err := RunIncremental(cctx, p7BaseConfig(), p7Logger(), nil); err == nil {
		t.Fatal("cancellation must surface")
	}
	p7AssertLockFree(t, ctx)
}

func TestP7RealPGEndToEnd(t *testing.T) {
	p7PrepareSchema(t)
	ctx := context.Background()
	pool := testutil.Pool(t)
	st := postgres.New(pool)
	srv, refresh := p7NewAListServer(t)
	rootID := "a7000000-0000-0000-0000-0000000000a1"
	p7SeedRoot(t, st, ctx, rootID, srv.URL)
	p7SeedWatch(t, st, ctx, rootID, "/", time.Now().UTC().Add(-time.Hour))

	buf := p7CaptureStdout(t)
	if err := RunIncremental(ctx, p7BaseConfig(), p7Logger(), []string{"--max-wall-time", "30s"}); err != nil {
		t.Fatalf("incremental run: %v", err)
	}
	got := p7ParseOneJSON(t, buf)
	if got["stop_reason"] != "COMPLETED" {
		t.Fatalf("stop_reason = %v", got["stop_reason"])
	}
	due, _ := got["due"].(map[string]any)
	if due["emitted"].(float64) != 1 || due["stale"].(float64) != 0 {
		t.Fatalf("due counts = %+v", due)
	}
	exec, _ := got["executor"].(map[string]any)
	if exec["ran"] != true || exec["succeeded"].(float64) != 1 {
		t.Fatalf("executor = %+v", exec)
	}

	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND canonical_path='/a.txt' AND resource_presence='PRESENT'`,
		rootID).Scan(&n); err != nil {
		t.Fatalf("canonical count: %v", err)
	}
	if n != 1 {
		t.Fatalf("canonical /a.txt PRESENT count = %d, want 1", n)
	}
	wk, err := st.GetWork(ctx, rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if wk.WorkState != state.WorkVerified {
		t.Fatalf("work state = %s, want VERIFIED", wk.WorkState)
	}
	if got := atomic.LoadInt32(refresh); got != 1 {
		t.Fatalf("exactly one refresh=true request required, got %d", got)
	}
	p7AssertLockFree(t, ctx)
}

func TestP7IncrementalNoDueDrainsPending(t *testing.T) {
	p7PrepareSchema(t)
	ctx := context.Background()
	pool := testutil.Pool(t)
	st := postgres.New(pool)
	srv, _ := p7NewAListServer(t)
	rootID := "a7000000-0000-0000-0000-0000000000a2"
	p7SeedRoot(t, st, ctx, rootID, srv.URL)
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	buf := p7CaptureStdout(t)
	if err := RunIncremental(ctx, p7BaseConfig(), p7Logger(), []string{"--max-wall-time", "30s"}); err != nil {
		t.Fatalf("incremental run: %v", err)
	}
	got := p7ParseOneJSON(t, buf)
	due, _ := got["due"].(map[string]any)
	if due["emitted"].(float64) != 0 {
		t.Fatalf("no due watches expected, due = %+v", due)
	}
	exec, _ := got["executor"].(map[string]any)
	if exec["ran"] != true || exec["succeeded"].(float64) != 1 {
		t.Fatalf("preexisting pending work must drain, executor = %+v", exec)
	}
	wk, err := st.GetWork(ctx, rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if wk.WorkState != state.WorkVerified {
		t.Fatalf("work state = %s, want VERIFIED", wk.WorkState)
	}
}

type p7FailingWriter struct{ err error }

func (w p7FailingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestP7IncrementalRejectsPositionalArgs(t *testing.T) {
	base := config.Defaults()
	base.DatabaseURL = "postgres://127.0.0.1:1/never" // unreachable: proves no DB work
	calls := p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		t.Error("orchestration must not run for positional args")
		return incrementalorch.Result{}, nil
	})
	for _, args := range [][]string{{"typo"}, {"typo", "extra"}} {
		err := RunIncremental(context.Background(), base, p7Logger(), args)
		if err == nil {
			t.Fatalf("%v must be rejected", args)
		}
		if !strings.Contains(err.Error(), "positional") {
			t.Fatalf("%v: unexpected error %v", args, err)
		}
		if strings.Contains(err.Error(), "open database") || strings.Contains(err.Error(), "ping database") {
			t.Fatalf("%v must fail before DB work: %v", args, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("positional args must make zero orchestration calls, got %d", *calls)
	}
}

// TestP7IncrementalPositionalStopsFlagParsingFailClosed covers the dangerous
// case where a positional token would stop flag parsing and leave an invalid
// budget unparsed, letting the command run with defaults.
func TestP7IncrementalPositionalStopsFlagParsingFailClosed(t *testing.T) {
	base := config.Defaults()
	base.DatabaseURL = "postgres://127.0.0.1:1/never"
	calls := p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		t.Error("orchestration must not run when a positional arg stops parsing")
		return incrementalorch.Result{}, nil
	})
	err := RunIncremental(context.Background(), base, p7Logger(), []string{"typo", "--max-wall-time", "0s"})
	if err == nil {
		t.Fatal("positional arg followed by an invalid flag must fail closed")
	}
	if !strings.Contains(err.Error(), "positional") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "open database") || strings.Contains(err.Error(), "ping database") {
		t.Fatalf("must fail before DB work: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("must make zero orchestration calls, got %d", *calls)
	}
}

func TestP7IncrementalValidFlagsOnlyUnchanged(t *testing.T) {
	p7PrepareSchema(t)
	p7CaptureStdout(t)
	calls := p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	})
	if err := RunIncremental(context.Background(), p7BaseConfig(), p7Logger(),
		[]string{"--max-wall-time", "30s", "--max-execute-items", "3"}); err != nil {
		t.Fatalf("flags-only invocation must still work: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("exactly one orchestration call, got %d", *calls)
	}
}

func TestP7IncrementalStdoutWriteFailureReturnsError(t *testing.T) {
	p7PrepareSchema(t)
	prev := incrementalStdout
	incrementalStdout = p7FailingWriter{err: errors.New("stdout broken pipe")}
	t.Cleanup(func() { incrementalStdout = prev })

	p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		return incrementalorch.Result{StopReason: incrementalorch.StopCompleted}, nil
	})
	err := RunIncremental(context.Background(), p7BaseConfig(), p7Logger(), nil)
	if err == nil {
		t.Fatal("a stdout write failure after a successful P6 must not exit 0")
	}
	if !strings.Contains(err.Error(), "write incremental result") {
		t.Fatalf("expected an output-delivery error, got %v", err)
	}
	// The deferred writer-lock release must still run when JSON delivery fails:
	// success -> P6 -> stdout failure -> command error -> Release -> re-acquire.
	p7AssertLockFree(t, context.Background())
}

func TestP7IncrementalStdoutWriteFailurePreservesCycleError(t *testing.T) {
	p7PrepareSchema(t)
	prev := incrementalStdout
	incrementalStdout = p7FailingWriter{err: errors.New("stdout broken pipe")}
	t.Cleanup(func() { incrementalStdout = prev })

	sentinel := errors.New("cycle runtime failure")
	p7StubCycle(t, func(context.Context, *postgres.Store, config.Config, *slog.Logger, incrementalexec.Config, incrementalorch.Config) (incrementalorch.Result, error) {
		return incrementalorch.Result{}, sentinel
	})
	err := RunIncremental(context.Background(), p7BaseConfig(), p7Logger(), nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("the P6 runtime error must be preserved, got %v", err)
	}
	if !strings.Contains(err.Error(), "write incremental result") {
		t.Fatalf("the output failure must be preserved too, got %v", err)
	}
}

func TestP7IncrementalMarshalErrorIsReturned(t *testing.T) {
	prev := incrementalStdout
	var buf bytes.Buffer
	incrementalStdout = &buf
	t.Cleanup(func() { incrementalStdout = prev })

	// A channel cannot be marshaled; the serialization error must be reported.
	if err := writeIncrementalJSON(make(chan int)); err == nil {
		t.Fatal("a serialization failure must be returned, not discarded")
	}
}
