package incrementalexec_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func p5NewStore(t *testing.T) (*postgres.Store, context.Context) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return postgres.New(pool), ctx
}

func p5AddRoot(t *testing.T, st *postgres.Store, ctx context.Context, rootID, baseURL string) {
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

func p5RunnerFor(t *testing.T, one incrementalexec.OneShotExecutor) *incrementalexec.CycleRunner {
	t.Helper()
	r, err := incrementalexec.NewCycleRunner(one)
	if err != nil {
		t.Fatalf("new cycle runner: %v", err)
	}
	return r
}

// p5BlockingScanner blocks until its context is cancelled and then returns the
// context error, mimicking an in-flight P4 scan interrupted by the cycle budget.
type p5BlockingScanner struct {
	calls int32
}

func (b *p5BlockingScanner) ScanScope(ctx context.Context, _, _ string, _ int) (scan.Result, error) {
	atomic.AddInt32(&b.calls, 1)
	<-ctx.Done()
	return scan.Result{}, ctx.Err()
}

// TestP5CycleRealPGMultiItem drains three real PostgreSQL items serially through
// the accepted P4 executor + real ScanScope + httptest AList.
func TestP5CycleRealPGMultiItem(t *testing.T) {
	st, ctx := p5NewStore(t)
	mock := &p4AListMock{dirs: map[string][]map[string]any{}}
	mock.set("/", p4File("a.txt", 5, "aaa"))
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)

	rootIDs := []string{
		"c0000000-0000-0000-0000-0000000000c1",
		"c0000000-0000-0000-0000-0000000000c2",
		"c0000000-0000-0000-0000-0000000000c3",
	}
	now := p4Now
	for i, id := range rootIDs {
		p5AddRoot(t, st, ctx, id, srv.URL)
		p4SeedPending(t, st, ctx, id, "/", now.Add(time.Duration(i)*time.Second), nil)
	}

	runner := p5RunnerFor(t, p4Executor(t, st, p4RealScanner(st), now))
	res, err := runner.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 30 * time.Second})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.StopReason != incrementalexec.StopNoEligibleWork {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if res.Invocations != 4 || res.SelectedItems != 3 || res.Succeeded != 3 || res.Failed != 0 {
		t.Fatalf("counts = %+v", res)
	}
	if n := mock.refreshCount(); n != 3 {
		t.Fatalf("provider refresh count = %d, want 3 (<= selected)", n)
	}
	for _, id := range rootIDs {
		w, gerr := st.GetWork(ctx, id, "/")
		if gerr != nil {
			t.Fatal(gerr)
		}
		if w.WorkState != state.WorkVerified {
			t.Fatalf("root %s work = %s, want VERIFIED", id, w.WorkState)
		}
	}
}

// TestP5CycleRealPGItemLocalFailureContinues proves one durable item-local
// provider failure does not stop the cycle from draining independent work.
func TestP5CycleRealPGItemLocalFailureContinues(t *testing.T) {
	st, ctx := p5NewStore(t)

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":403,"message":"forbidden","data":null}`)
	}))
	t.Cleanup(srvA.Close)

	mockB := &p4AListMock{dirs: map[string][]map[string]any{}}
	mockB.set("/", p4File("b.txt", 5, "bbb"))
	srvB := httptest.NewServer(mockB.handler())
	t.Cleanup(srvB.Close)

	rootA := "d0000000-0000-0000-0000-0000000000d1"
	rootB := "d0000000-0000-0000-0000-0000000000d2"
	now := p4Now
	p5AddRoot(t, st, ctx, rootA, srvA.URL)
	p5AddRoot(t, st, ctx, rootB, srvB.URL)
	p4SeedPending(t, st, ctx, rootA, "/", now, nil)
	p4SeedPending(t, st, ctx, rootB, "/", now.Add(time.Second), nil)

	runner := p5RunnerFor(t, p4Executor(t, st, p4RealScanner(st), now))
	res, err := runner.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 30 * time.Second})
	if err != nil {
		t.Fatalf("item-local failure must not stop the cycle: %v", err)
	}
	if res.StopReason != incrementalexec.StopNoEligibleWork {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if res.Invocations != 3 || res.SelectedItems != 2 || res.Succeeded != 1 || res.Failed != 1 {
		t.Fatalf("counts = %+v", res)
	}
	wA, gerr := st.GetWork(ctx, rootA, "/")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if wA.WorkState != state.WorkBlocked {
		t.Fatalf("AUTH failure must leave BLOCKED, got %s", wA.WorkState)
	}
	wB, gerr := st.GetWork(ctx, rootB, "/")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if wB.WorkState != state.WorkVerified {
		t.Fatalf("independent work must still verify, got %s", wB.WorkState)
	}
}

// TestP5CycleRealPGWallTimeInterruptsClaimedItem proves the cycle-owned deadline
// cancels a claimed in-flight item, leaves it IN_FLIGHT, and does not recover it.
func TestP5CycleRealPGWallTimeInterruptsClaimedItem(t *testing.T) {
	st, ctx := p5NewStore(t)
	rootID := "e0000000-0000-0000-0000-0000000000e1"
	now := p4Now
	p5AddRoot(t, st, ctx, rootID, "http://127.0.0.1:1")
	p4SeedPending(t, st, ctx, rootID, "/", now, nil)

	block := &p5BlockingScanner{}
	runner := p5RunnerFor(t, p4Executor(t, st, block, now))

	res, err := runner.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("cycle wall deadline is a normal bounded stop: %v", err)
	}
	if res.StopReason != incrementalexec.StopMaxWallTime {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if !res.InterruptedInFlight {
		t.Fatal("a cancelled claimed item must report InterruptedInFlight=true")
	}
	if res.Invocations != 1 {
		t.Fatalf("no next item may start, invocations=%d", res.Invocations)
	}

	w, gerr := st.GetWork(ctx, rootID, "/")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if w.WorkState != state.WorkInFlight {
		t.Fatalf("interrupted claim must remain IN_FLIGHT, got %s", w.WorkState)
	}

	// P5 must not recover; an external actor does.
	recovered, err := st.RecoverStaleInflight(context.Background(), rootID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("external recovery must requeue 1 item, got %d", recovered)
	}
	w2, gerr := st.GetWork(ctx, rootID, "/")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if w2.WorkState != state.WorkPending {
		t.Fatalf("recovered work must be PENDING, got %s", w2.WorkState)
	}
}

// TestP5CycleRealPGOverdueRetryWaitNotPromoted proves P5 never auto-promotes an
// overdue RETRY_WAIT row.
func TestP5CycleRealPGOverdueRetryWaitNotPromoted(t *testing.T) {
	st, ctx := p5NewStore(t)
	rootID := "f0000000-0000-0000-0000-0000000000f1"
	now := p4Now
	p5AddRoot(t, st, ctx, rootID, "http://127.0.0.1:1")

	p4SeedPending(t, st, ctx, rootID, "/pending", now, nil)
	p4SeedPending(t, st, ctx, rootID, "/retry", now, nil)
	if _, err := st.Pool().Exec(ctx, `
		UPDATE index_dirty_scope_work SET work_state='RETRY_WAIT', pending_not_before=$2
		 WHERE root_id=$1::uuid AND scope_key='/retry'`, rootID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	sc := &countingScanner{result: p4AppliedResult()}
	runner := p5RunnerFor(t, p4Executor(t, st, sc, now))
	res, err := runner.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: time.Minute})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.StopReason != incrementalexec.StopNoEligibleWork {
		t.Fatalf("stop reason = %s", res.StopReason)
	}
	if res.SelectedItems != 1 || res.Succeeded != 1 || res.Failed != 0 {
		t.Fatalf("counts = %+v", res)
	}
	if res.Last.ScopeKey != "/pending" {
		t.Fatalf("only the PENDING item may execute, got %q", res.Last.ScopeKey)
	}

	w, gerr := st.GetWork(ctx, rootID, "/retry")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if w.WorkState != state.WorkRetryWait {
		t.Fatalf("RETRY_WAIT must remain RETRY_WAIT, got %s", w.WorkState)
	}
	if w.AttemptCount != 0 {
		t.Fatalf("RETRY_WAIT must not be attempted, got attempt_count=%d", w.AttemptCount)
	}
}

// p5BlockingCompleteStore blocks CompleteSuccess until the caller context ends,
// reproducing a scan that succeeded but whose completion is interrupted by the
// cycle-owned deadline.
type p5BlockingCompleteStore struct {
	*postgres.Store
	entered chan struct{}
	once    sync.Once
}

func (s *p5BlockingCompleteStore) CompleteSuccess(ctx context.Context, _, _ string, _ int64, _ time.Time) (state.DirtyScopeWork, error) {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	return state.DirtyScopeWork{}, ctx.Err()
}

// TestP5CycleRealPGCompletionDeadlineIsMaxWallTime proves a real P0/P4 scan that
// succeeded still reports MAX_WALL_TIME when its completion is interrupted by the
// cycle-owned deadline, and leaves the Work IN_FLIGHT.
func TestP5CycleRealPGCompletionDeadlineIsMaxWallTime(t *testing.T) {
	st, ctx := p5NewStore(t)
	mock := &p4AListMock{dirs: map[string][]map[string]any{}}
	mock.set("/", p4File("a.txt", 5, "aaa"))
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)

	rootID := "a1000000-0000-0000-0000-0000000000a3"
	now := p4Now
	p5AddRoot(t, st, ctx, rootID, srv.URL)
	p4SeedPending(t, st, ctx, rootID, "/", now, nil)

	blocking := &p5BlockingCompleteStore{Store: st, entered: make(chan struct{})}
	runner := p5RunnerFor(t, p4Executor(t, blocking, p4RealScanner(st), now))

	type outcome struct {
		res incrementalexec.CycleResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := runner.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 500 * time.Millisecond})
		done <- outcome{res: res, err: err}
	}()

	select {
	case <-blocking.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("CompleteSuccess was not reached")
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("completion deadline is a normal bounded stop, got %v", got.err)
	}
	if got.res.StopReason != incrementalexec.StopMaxWallTime {
		t.Fatalf("stop reason = %s", got.res.StopReason)
	}
	if !got.res.InterruptedInFlight {
		t.Fatal("a claimed item interrupted during completion must report InterruptedInFlight=true")
	}
	if n := mock.refreshCount(); n != 1 {
		t.Fatalf("one scan must issue one refresh, got %d", n)
	}
	w, gerr := st.GetWork(ctx, rootID, "/")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if w.WorkState != state.WorkInFlight {
		t.Fatalf("interrupted completion must remain IN_FLIGHT, got %s", w.WorkState)
	}
}

// TestP5CycleRealPGNewSignalBoundedByMaxItems proves a newer signal arriving
// during a scan is executed as new work in a later iteration, still bounded.
func TestP5CycleRealPGNewSignalBoundedByMaxItems(t *testing.T) {
	st, ctx := p5NewStore(t)
	mock := &p4AListMock{dirs: map[string][]map[string]any{}}
	mock.set("/", p4File("a.txt", 5, "aaa"))
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)

	rootID := "a1000000-0000-0000-0000-0000000000a2"
	now := p4Now
	p5AddRoot(t, st, ctx, rootID, srv.URL)
	p4SeedPending(t, st, ctx, rootID, "/", now, nil)

	block := &blockingDelegatingScanner{
		inner:   p4RealScanner(st),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	runner := p5RunnerFor(t, p4Executor(t, st, block, now))

	type outcome struct {
		res incrementalexec.CycleResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := runner.Run(ctx, incrementalexec.CycleConfig{MaxItems: 2, MaxWallTime: 30 * time.Second})
		done <- outcome{res: res, err: err}
	}()

	<-block.entered
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/", Source: state.SourceProviderEvent, Reason: state.ReasonPossibleChange,
		Priority: state.PriorityNormal, SeenAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("merge signal: %v", err)
	}
	close(block.release)

	got := <-done
	if got.err != nil {
		t.Fatalf("run: %v", got.err)
	}
	if got.res.Invocations != 2 || got.res.SelectedItems != 2 || got.res.Succeeded != 2 {
		t.Fatalf("new signal must be executed as new bounded work, counts = %+v", got.res)
	}
	if got.res.StopReason != incrementalexec.StopMaxItems {
		t.Fatalf("stop reason = %s", got.res.StopReason)
	}
	w, gerr := st.GetWork(ctx, rootID, "/")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if w.WorkState != state.WorkVerified {
		t.Fatalf("new epoch must verify on the second iteration, got %s", w.WorkState)
	}
}
