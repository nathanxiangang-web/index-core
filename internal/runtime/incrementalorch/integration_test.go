package incrementalorch_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func p6NewStore(t *testing.T) (*postgres.Store, context.Context) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return postgres.New(pool), ctx
}

func p6AddRoot(t *testing.T, st *postgres.Store, ctx context.Context, rootID, baseURL string) {
	t.Helper()
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root %s: %v", rootID, err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy %s: %v", rootID, err)
	}
	acfg, _ := json.Marshal(map[string]string{"base_url": baseURL, "path": "/"})
	if err := st.UpsertAdapterConfig(ctx, rootID,
		postgres.AdapterConfig{CollectorKind: "alist", Config: acfg}); err != nil {
		t.Fatalf("adapter %s: %v", rootID, err)
	}
}

func p6CreateDueWatch(t *testing.T, st *postgres.Store, ctx context.Context, rootID, scopeKey string, dueAt time.Time) {
	t.Helper()
	interval := int64(120)
	d := dueAt
	_, err := st.CreateWatch(ctx, state.ScopeWatchState{
		RootID: rootID, ScopeKey: scopeKey, WatchState: state.WatchHot,
		CadenceClass: "HOT_120", EffectiveIntervalSeconds: &interval,
		SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy},
		Priority:  state.PriorityNormal, NextDueAt: &d,
	})
	if err != nil {
		t.Fatalf("create watch %s/%s: %v", rootID, scopeKey, err)
	}
}

func p6ExecConfig() incrementalexec.Config {
	return incrementalexec.Config{
		MaxEntriesPerScope: 1000,
		Retry: incrementalexec.RetryPolicy{
			TransientProvider: 30 * time.Second, Throttled: 45 * time.Second, Internal: 60 * time.Second,
		},
	}
}

func p6CycleRunner(t *testing.T, one incrementalexec.OneShotExecutor) *incrementalexec.CycleRunner {
	t.Helper()
	c, err := incrementalexec.NewCycleRunner(one)
	if err != nil {
		t.Fatalf("new cycle runner: %v", err)
	}
	return c
}

func p6RealExecutor(t *testing.T, st *postgres.Store, now time.Time) incrementalexec.OneShotExecutor {
	t.Helper()
	svc := scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	one, err := incrementalexec.New(st, svc, p6ExecConfig(), func() time.Time { return now })
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	return one
}

// p6NewAListServer serves a coherent single-page AList listing and counts forced
// refresh requests.
func p6NewAListServer(t *testing.T) (*httptest.Server, *int32) {
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
		total := 0
		if req.Path == "/" {
			content = []map[string]any{{
				"name": "a.txt", "size": 5, "is_dir": false,
				"modified": "2026-01-02T03:04:05Z", "hash_info": map[string]string{"sha1": "aaa"},
			}}
			total = 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"content": content, "total": total},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &refresh
}

type p6AppliedScanner struct {
	calls int32
}

func (s *p6AppliedScanner) ScanScope(context.Context, string, string, int) (scan.Result, error) {
	atomic.AddInt32(&s.calls, 1)
	return scan.Result{Outcome: postgres.ReconcileOutcome{Status: domain.AdmissionApplied}}, nil
}

type p6BlockingScanner struct {
	calls int32
}

func (b *p6BlockingScanner) ScanScope(ctx context.Context, _, _ string, _ int) (scan.Result, error) {
	atomic.AddInt32(&b.calls, 1)
	<-ctx.Done()
	return scan.Result{}, ctx.Err()
}

func p6AssertPresent(t *testing.T, st *postgres.Store, rootID, path string) {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND canonical_path=$2 AND resource_presence='PRESENT'`,
		rootID, path).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", path, err)
	}
	if n != 1 {
		t.Fatalf("expected one PRESENT resource at %s, got %d", path, n)
	}
}

func p6AssertNoRemovalEvidence(t *testing.T, st *postgres.Store, rootID, path string) {
	t.Helper()
	var ev string
	var missing *time.Time
	var consec int
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT removal_evidence_state, missing_since, consecutive_complete_missing
		   FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND canonical_path=$2 AND resource_presence='PRESENT'`,
		rootID, path).Scan(&ev, &missing, &consec); err != nil {
		t.Fatalf("load removal evidence for %s: %v", path, err)
	}
	if ev != "NONE" || missing != nil || consec != 0 {
		t.Fatalf("path %s must carry no removal evidence, got %s/%v/%d", path, ev, missing, consec)
	}
}

// TestP6RealPGWatchToCanonical proves the full accepted path in one manual cycle.
func TestP6RealPGWatchToCanonical(t *testing.T) {
	st, ctx := p6NewStore(t)
	srv, refresh := p6NewAListServer(t)
	now := p6Now
	rootID := "a6000000-0000-0000-0000-0000000000a1"
	p6AddRoot(t, st, ctx, rootID, srv.URL)
	p6CreateDueWatch(t, st, ctx, rootID, "/", now.Add(-time.Hour))

	runner := p6Runner(t, st, p6CycleRunner(t, p6RealExecutor(t, st, now)), func() time.Time { return now })
	res, err := runner.RunCycle(ctx, p6ValidConfig())
	if err != nil {
		t.Fatalf("run cycle: %v", err)
	}
	if res.StopReason != incrementalorch.StopCompleted {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if res.DueEmitted != 1 || res.DueStale != 0 || res.DueAttempted != 1 {
		t.Fatalf("due counts = %+v", res)
	}
	if !res.ExecutorRan || res.Executor.SelectedItems != 1 || res.Executor.Succeeded != 1 {
		t.Fatalf("executor result = %+v", res.Executor)
	}

	w, err := st.GetWatch(ctx, rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if w.LastDueAt == nil || !w.LastDueAt.UTC().Equal(now) {
		t.Fatalf("last_due_at = %v, want %s", w.LastDueAt, now)
	}
	if w.NextDueAt == nil || !w.NextDueAt.After(now) {
		t.Fatalf("next_due_at = %v, want after %s", w.NextDueAt, now)
	}

	wk, err := st.GetWork(ctx, rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if wk.WorkState != state.WorkVerified {
		t.Fatalf("work state = %s, want VERIFIED", wk.WorkState)
	}
	p6AssertPresent(t, st, rootID, "/a.txt")
	p6AssertNoRemovalEvidence(t, st, rootID, "/a.txt")

	if got := atomic.LoadInt32(refresh); got != 1 {
		t.Fatalf("exactly one refresh=true provider request required, got %d", got)
	}
}

// TestP6RealPGNoDueWatchDrainsExistingPending proves P5 still runs without new polls.
func TestP6RealPGNoDueWatchDrainsExistingPending(t *testing.T) {
	st, ctx := p6NewStore(t)
	srv, refresh := p6NewAListServer(t)
	now := p6Now
	rootID := "a6000000-0000-0000-0000-0000000000a2"
	p6AddRoot(t, st, ctx, rootID, srv.URL)
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: now,
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	runner := p6Runner(t, st, p6CycleRunner(t, p6RealExecutor(t, st, now)), func() time.Time { return now })
	res, err := runner.RunCycle(ctx, p6ValidConfig())
	if err != nil {
		t.Fatalf("run cycle: %v", err)
	}
	if res.DueCandidates != 0 || res.DueEmitted != 0 {
		t.Fatalf("no due watches expected, got %+v", res)
	}
	if res.StopReason != incrementalorch.StopCompleted {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if !res.ExecutorRan || res.Executor.SelectedItems != 1 || res.Executor.Succeeded != 1 {
		t.Fatalf("preexisting pending work must drain, executor = %+v", res.Executor)
	}
	p6AssertPresent(t, st, rootID, "/a.txt")
	if got := atomic.LoadInt32(refresh); got != 1 {
		t.Fatalf("one provider refresh expected, got %d", got)
	}
}

// TestP6RealPGWallDeadlineDuringNestedP5Claim proves the overall deadline cancels
// a nested claimed item and leaves it for external recovery only.
func TestP6RealPGWallDeadlineDuringNestedP5Claim(t *testing.T) {
	st, ctx := p6NewStore(t)
	now := p6Now
	rootID := "a6000000-0000-0000-0000-0000000000a3"
	p6AddRoot(t, st, ctx, rootID, "http://127.0.0.1:1")
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: now,
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	block := &p6BlockingScanner{}
	one, err := incrementalexec.New(st, block, p6ExecConfig(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	runner := p6Runner(t, st, p6CycleRunner(t, one), func() time.Time { return now })

	cfg := incrementalorch.Config{MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: 50 * time.Millisecond}
	res, err := runner.RunCycle(ctx, cfg)
	if err != nil {
		t.Fatalf("P6 deadline is a normal bounded stop, got %v", err)
	}
	if res.StopReason != incrementalorch.StopMaxWallTime {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if !res.ExecutorRan || !res.Executor.InterruptedInFlight {
		t.Fatalf("nested P5 result must be retained with InterruptedInFlight, got %+v", res.Executor)
	}
	wk, err := st.GetWork(ctx, rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if wk.WorkState != state.WorkInFlight {
		t.Fatalf("interrupted claim must remain IN_FLIGHT, got %s", wk.WorkState)
	}
	recovered, err := st.RecoverStaleInflight(context.Background(), rootID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("external recovery must requeue 1 item, got %d", recovered)
	}
}

// TestP6RealPGDuePollIntoRetryWaitNotPromoted proves a poll does not promote a
// RETRY_WAIT row while independent pending work still executes.
func TestP6RealPGDuePollIntoRetryWaitNotPromoted(t *testing.T) {
	st, ctx := p6NewStore(t)
	now := p6Now
	rootID := "a6000000-0000-0000-0000-0000000000a4"
	p6AddRoot(t, st, ctx, rootID, "http://127.0.0.1:1")
	p6CreateDueWatch(t, st, ctx, rootID, "/retry", now.Add(-time.Hour))
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/retry", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: now,
	}); err != nil {
		t.Fatalf("seed retry work: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `
		UPDATE index_dirty_scope_work SET work_state='RETRY_WAIT', pending_not_before=$2
		 WHERE root_id=$1::uuid AND scope_key='/retry'`, rootID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/pending", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("seed independent pending: %v", err)
	}

	sc := &p6AppliedScanner{}
	one, err := incrementalexec.New(st, sc, p6ExecConfig(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	runner := p6Runner(t, st, p6CycleRunner(t, one), func() time.Time { return now })

	res, err := runner.RunCycle(ctx, p6ValidConfig())
	if err != nil {
		t.Fatalf("run cycle: %v", err)
	}
	if res.DueEmitted != 1 {
		t.Fatalf("poll must be emitted, got %+v", res)
	}
	wRetry, err := st.GetWork(ctx, rootID, "/retry")
	if err != nil {
		t.Fatal(err)
	}
	if wRetry.WorkState != state.WorkRetryWait {
		t.Fatalf("RETRY_WAIT must not be promoted, got %s", wRetry.WorkState)
	}
	wPending, err := st.GetWork(ctx, rootID, "/pending")
	if err != nil {
		t.Fatal(err)
	}
	if wPending.WorkState != state.WorkVerified {
		t.Fatalf("independent pending work must execute, got %s", wPending.WorkState)
	}
	if res.Executor.SelectedItems != 1 || res.Executor.Succeeded != 1 {
		t.Fatalf("executor result = %+v", res.Executor)
	}
}

// TestP6RealPGExistingPendingCoalescing proves poll coalescing adds one signal_seq
// with no lost wakeup.
func TestP6RealPGExistingPendingCoalescing(t *testing.T) {
	st, ctx := p6NewStore(t)
	now := p6Now
	rootID := "a6000000-0000-0000-0000-0000000000a5"
	p6AddRoot(t, st, ctx, rootID, "http://127.0.0.1:1")
	p6CreateDueWatch(t, st, ctx, rootID, "/", now.Add(-time.Hour))
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: now,
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	before, err := st.GetWork(ctx, rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if before.SignalSeq != 1 {
		t.Fatalf("seed signal_seq = %d, want 1", before.SignalSeq)
	}

	sc := &p6AppliedScanner{}
	one, err := incrementalexec.New(st, sc, p6ExecConfig(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	runner := p6Runner(t, st, p6CycleRunner(t, one), func() time.Time { return now })

	res, err := runner.RunCycle(ctx, p6ValidConfig())
	if err != nil {
		t.Fatalf("run cycle: %v", err)
	}
	if res.DueEmitted != 1 {
		t.Fatalf("poll must be emitted, got %+v", res)
	}
	after, err := st.GetWork(ctx, rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if after.SignalSeq != before.SignalSeq+1 {
		t.Fatalf("poll coalescing must add exactly one signal_seq: %d -> %d", before.SignalSeq, after.SignalSeq)
	}
	if after.WorkState != state.WorkVerified {
		t.Fatalf("coalesced epoch must be satisfied exactly once, got %s", after.WorkState)
	}
	if res.Executor.SelectedItems != 1 || res.Executor.Succeeded != 1 {
		t.Fatalf("exactly one execution must satisfy the coalesced epoch, got %+v", res.Executor)
	}
}
