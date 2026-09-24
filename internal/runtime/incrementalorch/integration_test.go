package incrementalorch_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
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

// p6AListMock is a mutable, coherent single-page AList listing that counts forced
// refresh requests.
type p6AListMock struct {
	mu      sync.Mutex
	content []map[string]any
	refresh int32
}

func (m *p6AListMock) set(content ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, len(content))
	copy(out, content)
	m.content = out
}

func (m *p6AListMock) refreshCount() int { return int(atomic.LoadInt32(&m.refresh)) }

func p6FileEntry(name string, size int64, sha1 string) map[string]any {
	return map[string]any{
		"name": name, "size": size, "is_dir": false,
		"modified": "2026-01-02T03:04:05Z", "hash_info": map[string]string{"sha1": sha1},
	}
}

// p6NewAListServer serves the mock's current content and counts forced refresh
// requests.
func p6NewAListServer(t *testing.T) (*httptest.Server, *p6AListMock) {
	t.Helper()
	mock := &p6AListMock{}
	mock.set(p6FileEntry("a.txt", 5, "aaa"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path    string `json:"path"`
			Refresh bool   `json:"refresh"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Refresh {
			atomic.AddInt32(&mock.refresh, 1)
		}
		mock.mu.Lock()
		content := make([]map[string]any, len(mock.content))
		copy(content, mock.content)
		mock.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"content": content, "total": len(content)},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, mock
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
	srv, mock := p6NewAListServer(t)
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
	// Watch execution attribution: POLL_SCHEDULE -> ClaimWork -> CompleteSuccess
	// must have advanced the watch health bookkeeping.
	if w.LastAttemptStartedAt == nil || !w.LastAttemptStartedAt.UTC().Equal(now) {
		t.Fatalf("last_attempt_started_at = %v, want %s", w.LastAttemptStartedAt, now)
	}
	if w.LastAttemptFinishedAt == nil || w.LastAttemptFinishedAt.Before(now) {
		t.Fatalf("last_attempt_finished_at = %v, want >= %s", w.LastAttemptFinishedAt, now)
	}
	if w.LastSuccessAt == nil || !w.LastSuccessAt.UTC().Equal(now) {
		t.Fatalf("last_success_at = %v, want %s", w.LastSuccessAt, now)
	}
	if w.ConsecutiveFailures != 0 {
		t.Fatalf("consecutive_failures = %d, want 0", w.ConsecutiveFailures)
	}
	if w.LastErrorClass != nil {
		t.Fatalf("last_error_class = %v, want nil", *w.LastErrorClass)
	}

	wk, err := st.GetWork(ctx, rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if wk.WorkState != state.WorkVerified {
		t.Fatalf("work state = %s, want VERIFIED", wk.WorkState)
	}
	if wk.LastVerifiedSignalSeq == nil || *wk.LastVerifiedSignalSeq < 1 {
		t.Fatalf("poll signal must survive into claim-scoped verification, got %v", wk.LastVerifiedSignalSeq)
	}
	p6AssertPresent(t, st, rootID, "/a.txt")
	p6AssertNoRemovalEvidence(t, st, rootID, "/a.txt")

	if got := mock.refreshCount(); got != 1 {
		t.Fatalf("exactly one refresh=true provider request required, got %d", got)
	}
}

// TestP6RealPGNoDueWatchDrainsExistingPending proves P5 still runs without new polls.
func TestP6RealPGNoDueWatchDrainsExistingPending(t *testing.T) {
	st, ctx := p6NewStore(t)
	srv, mock := p6NewAListServer(t)
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
	if got := mock.refreshCount(); got != 1 {
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

// TestP6RealPGDuePollIntoBlockedNotPromoted proves a poll merges into a BLOCKED
// row without promoting it, while independent PENDING work still executes.
func TestP6RealPGDuePollIntoBlockedNotPromoted(t *testing.T) {
	st, ctx := p6NewStore(t)
	now := p6Now
	rootID := "a6000000-0000-0000-0000-0000000000a6"
	p6AddRoot(t, st, ctx, rootID, "http://127.0.0.1:1")
	p6CreateDueWatch(t, st, ctx, rootID, "/blocked", now.Add(-time.Hour))
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/blocked", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: now,
	}); err != nil {
		t.Fatalf("seed blocked work: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `
		UPDATE index_dirty_scope_work SET work_state='BLOCKED'
		 WHERE root_id=$1::uuid AND scope_key='/blocked'`, rootID); err != nil {
		t.Fatal(err)
	}
	blockedBefore, err := st.GetWork(ctx, rootID, "/blocked")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/pending", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("seed independent pending: %v", err)
	}

	one, err := incrementalexec.New(st, &p6AppliedScanner{}, p6ExecConfig(), func() time.Time { return now })
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
	wBlocked, err := st.GetWork(ctx, rootID, "/blocked")
	if err != nil {
		t.Fatal(err)
	}
	if wBlocked.WorkState != state.WorkBlocked {
		t.Fatalf("BLOCKED must not be promoted, got %s", wBlocked.WorkState)
	}
	if wBlocked.AttemptCount != blockedBefore.AttemptCount {
		t.Fatalf("BLOCKED row must not be executed, attempt_count %d -> %d",
			blockedBefore.AttemptCount, wBlocked.AttemptCount)
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

// TestP6RealPGScopedAbsenceCreatesNoRemovalEvidence proves a PARTIAL scoped
// observation that omits a known canonical child does not create removal
// evidence and does not drop the canonical resource.
func TestP6RealPGScopedAbsenceCreatesNoRemovalEvidence(t *testing.T) {
	st, ctx := p6NewStore(t)
	srv, mock := p6NewAListServer(t)
	mock.set(p6FileEntry("old.txt", 4, "old"), p6FileEntry("keep.txt", 4, "keep"))
	now := p6Now
	rootID := "a6000000-0000-0000-0000-0000000000a7"
	p6AddRoot(t, st, ctx, rootID, srv.URL)

	runner := p6Runner(t, st, p6CycleRunner(t, p6RealExecutor(t, st, now)), func() time.Time { return now })

	// First observation establishes /old.txt and /keep.txt as canonical truth.
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunCycle(ctx, p6ValidConfig()); err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	p6AssertPresent(t, st, rootID, "/old.txt")
	p6AssertPresent(t, st, rootID, "/keep.txt")

	// A later PARTIAL scoped refresh omits /old.txt.
	mock.set(p6FileEntry("keep.txt", 4, "keep"))
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunCycle(ctx, p6ValidConfig()); err != nil {
		t.Fatalf("second cycle: %v", err)
	}

	p6AssertPresent(t, st, rootID, "/old.txt")
	p6AssertPresent(t, st, rootID, "/keep.txt")
	p6AssertNoRemovalEvidence(t, st, rootID, "/old.txt")
}
