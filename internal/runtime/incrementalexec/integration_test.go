package incrementalexec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// p4AListMock is a deterministic httptest AList /api/fs/list endpoint. It counts
// forced refresh requests so a test can prove at most one provider refresh per
// invocation.
type p4AListMock struct {
	mu      sync.Mutex
	dirs    map[string][]map[string]any
	refresh int32
}

func (m *p4AListMock) set(dir string, entries ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entries == nil {
		entries = []map[string]any{}
	}
	m.dirs[dir] = entries
}

func (m *p4AListMock) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path    string `json:"path"`
			Refresh bool   `json:"refresh"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Refresh {
			atomic.AddInt32(&m.refresh, 1)
		}
		m.mu.Lock()
		entries := m.dirs[req.Path]
		out := make([]map[string]any, len(entries))
		copy(out, entries)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"content": out, "total": len(out)},
		})
	}
}

func (m *p4AListMock) refreshCount() int { return int(atomic.LoadInt32(&m.refresh)) }

func p4File(name string, size int64, sha1 string) map[string]any {
	return map[string]any{
		"name": name, "size": size, "is_dir": false,
		"modified": "2026-01-02T03:04:05Z", "hash_info": map[string]string{"sha1": sha1},
	}
}

func p4Dir(name string) map[string]any {
	return map[string]any{"name": name, "size": 0, "is_dir": true, "modified": "2026-01-02T03:04:05Z"}
}

const p4IntegrationRoot = "a1000000-0000-0000-0000-0000000000a1"

func p4SetupRootPath(t *testing.T, rootPath string) (*postgres.Store, *p4AListMock, string) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	if err := st.CreateRoot(ctx, st.Pool(), p4IntegrationRoot, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, p4IntegrationRoot, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	mock := &p4AListMock{dirs: map[string][]map[string]any{}}
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)
	acfg, _ := json.Marshal(map[string]string{"base_url": srv.URL, "path": rootPath})
	if err := st.UpsertAdapterConfig(ctx, p4IntegrationRoot,
		postgres.AdapterConfig{CollectorKind: "alist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}
	return st, mock, p4IntegrationRoot
}

func p4RealScanner(st *postgres.Store) *scan.Service {
	return scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func p4Executor(t *testing.T, st incrementalexec.WorkStore, scanner incrementalexec.ScopedScanner, now time.Time) *incrementalexec.Executor {
	t.Helper()
	ex, err := incrementalexec.New(st, scanner, p4Config(), func() time.Time { return now })
	if err != nil {
		t.Fatalf("build executor: %v", err)
	}
	return ex
}

func p4SeedPending(t *testing.T, st *postgres.Store, ctx context.Context, rootID, scope string, seenAt time.Time, notBefore *time.Time) {
	t.Helper()
	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: scope, Source: state.SourceManualOperator, Reason: state.ReasonManualVerify,
		Priority: state.PriorityNormal, SeenAt: seenAt, NotBefore: notBefore,
	}); err != nil {
		t.Fatalf("seed pending work %s: %v", scope, err)
	}
}

func p4AssertPresent(t *testing.T, st *postgres.Store, path string) {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND canonical_path=$2 AND resource_presence='PRESENT'`,
		p4IntegrationRoot, path).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", path, err)
	}
	if n != 1 {
		t.Fatalf("expected exactly one PRESENT resource at %s, got %d", path, n)
	}
}

func p4AssertNoRemovalEvidence(t *testing.T, st *postgres.Store, path string) {
	t.Helper()
	var ev string
	var missingSince *time.Time
	var consec int
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT removal_evidence_state, missing_since, consecutive_complete_missing
		   FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND canonical_path=$2 AND resource_presence='PRESENT'`,
		p4IntegrationRoot, path).Scan(&ev, &missingSince, &consec); err != nil {
		t.Fatalf("load removal evidence for %s: %v", path, err)
	}
	if ev != "NONE" || missingSince != nil || consec != 0 {
		t.Fatalf("path %s must carry no removal evidence, got state=%s missing_since=%v consec=%d",
			path, ev, missingSince, consec)
	}
}

type blockingDelegatingScanner struct {
	inner   incrementalexec.ScopedScanner
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   int32
}

func (b *blockingDelegatingScanner) ScanScope(ctx context.Context, rootID, scope string, maxEntries int) (scan.Result, error) {
	atomic.AddInt32(&b.calls, 1)
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return b.inner.ScanScope(ctx, rootID, scope, maxEntries)
	case <-ctx.Done():
		return scan.Result{}, ctx.Err()
	}
}

type blockingFailingScanner struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
	calls   int32
}

func (b *blockingFailingScanner) ScanScope(ctx context.Context, _, _ string, _ int) (scan.Result, error) {
	atomic.AddInt32(&b.calls, 1)
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return scan.Result{}, b.err
	case <-ctx.Done():
		return scan.Result{}, ctx.Err()
	}
}

// --- Actual P0 success integration ------------------------------------------

// TestP4ExecuteOneActualP0Success proves the executor reuses the accepted P0
// path: exactly one refresh=true request, the PARTIAL Snapshot is
// admitted/reconciled, the canonical resource becomes visible, the Work reaches
// VERIFIED, and a later scoped observation that omits a known child creates no
// removal evidence.
func TestP4ExecuteOneActualP0Success(t *testing.T) {
	st, mock, rootID := p4SetupRootPath(t, "/")
	ctx := context.Background()
	now := p4Now

	mock.set("/", p4File("a.txt", 5, "aaa"), p4Dir("sub"))
	p4SeedPending(t, st, ctx, rootID, "/", now, nil)

	ex := p4Executor(t, st, p4RealScanner(st), now)
	res, err := ex.ExecuteOne(ctx)
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if !res.Selected || res.SnapshotID == "" || res.FinalWorkState != state.WorkVerified {
		t.Fatalf("unexpected result %+v", res)
	}
	if res.ClaimedSignalSeq != 1 {
		t.Fatalf("claimed signal sequence = %d, want 1", res.ClaimedSignalSeq)
	}
	if n := mock.refreshCount(); n != 1 {
		t.Fatalf("exactly one scoped refresh request required, got %d", n)
	}
	p4AssertPresent(t, st, "/a.txt")
	p4AssertPresent(t, st, "/sub")
	p4AssertNoRemovalEvidence(t, st, "/a.txt")
	p4AssertNoRemovalEvidence(t, st, "/sub")

	// Second scoped observation omits a known child: PARTIAL absence must not
	// produce removal evidence nor remove the resource.
	mock.set("/", p4File("a.txt", 5, "aaa"))
	p4SeedPending(t, st, ctx, rootID, "/", now.Add(time.Minute), nil)
	res2, err := ex.ExecuteOne(ctx)
	if err != nil {
		t.Fatalf("second ExecuteOne: %v", err)
	}
	if res2.FinalWorkState != state.WorkVerified {
		t.Fatalf("second observation must verify, got %+v", res2)
	}
	p4AssertPresent(t, st, "/sub")
	p4AssertNoRemovalEvidence(t, st, "/sub")
	if n := mock.refreshCount(); n != 2 {
		t.Fatalf("two invocations must issue exactly two refresh requests, got %d", n)
	}
}

// TestP4ExecuteOneOneShotCardinality proves one invocation leaves a second
// eligible row untouched.
func TestP4ExecuteOneOneShotCardinality(t *testing.T) {
	st, _, rootID := p4SetupRootPath(t, "/")
	ctx := context.Background()
	now := p4Now

	p4SeedPending(t, st, ctx, rootID, "/older", now, nil)
	p4SeedPending(t, st, ctx, rootID, "/newer", now.Add(time.Minute), nil)

	sc := &countingScanner{result: scan.Result{
		SnapshotID: "snap-x",
		Outcome:    postgres.ReconcileOutcome{Status: domain.AdmissionApplied},
	}}
	ex := p4Executor(t, st, sc, now)
	res, err := ex.ExecuteOne(ctx)
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if !res.Selected || res.ScopeKey != "/older" {
		t.Fatalf("oldest eligible row must be selected, got %+v", res)
	}
	if n := atomic.LoadInt32(&sc.calls); n != 1 {
		t.Fatalf("exactly one scan required, got %d", n)
	}
	first, err := st.GetWork(ctx, rootID, "/older")
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkState != state.WorkVerified {
		t.Fatalf("selected work must be VERIFIED, got %s", first.WorkState)
	}
	second, err := st.GetWork(ctx, rootID, "/newer")
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkState != state.WorkPending || second.AttemptCount != 0 || second.ClaimedSignalSeq != nil {
		t.Fatalf("second eligible row must remain untouched, got %+v", second)
	}
}

// TestP4ExecuteOneSignalDuringExecution proves signal 8 survives signal 7's
// completion: the Work returns to PENDING with only signal 8 pending and the
// signal 7 watermark recorded.
func TestP4ExecuteOneSignalDuringExecution(t *testing.T) {
	st, mock, rootID := p4SetupRootPath(t, "/")
	ctx := context.Background()
	now := p4Now

	mock.set("/", p4File("a.txt", 5, "aaa"))
	p4SeedPending(t, st, ctx, rootID, "/", now, nil)

	block := &blockingDelegatingScanner{
		inner:   p4RealScanner(st),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	ex := p4Executor(t, st, block, now)

	done := make(chan error, 1)
	go func() {
		_, err := ex.ExecuteOne(ctx)
		done <- err
	}()
	<-block.entered

	if _, err := st.MergeSignal(ctx, state.DirtySignal{
		RootID: rootID, ScopeKey: "/", Source: state.SourceProviderEvent, Reason: state.ReasonPossibleChange,
		Priority: state.PriorityNormal, SeenAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("merge post-claim signal: %v", err)
	}
	close(block.release)
	if err := <-done; err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}

	w, err := st.GetWork(ctx, rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if w.WorkState != state.WorkPending {
		t.Fatalf("Work must return to PENDING with signal 8 pending, got %s", w.WorkState)
	}
	if w.LastVerifiedSignalSeq == nil || *w.LastVerifiedSignalSeq != 1 {
		t.Fatalf("claimed signal 7 watermark must be recorded, got %v", w.LastVerifiedSignalSeq)
	}
	if w.SignalSeq != 2 {
		t.Fatalf("signal_seq must be 2 (signal 8 merged), got %d", w.SignalSeq)
	}
	hasProvider := false
	for _, s := range w.PendingSourceSet {
		if s == state.SourceProviderEvent {
			hasProvider = true
		}
		if s == state.SourceManualOperator {
			t.Fatalf("claimed signal provenance must not be re-added to pending, got %v", w.PendingSourceSet)
		}
	}
	if !hasProvider || len(w.PendingSourceSet) == 0 {
		t.Fatalf("signal 8 provenance must survive, got %v", w.PendingSourceSet)
	}
}

// --- Typed failure mapping ---------------------------------------------------

func TestP4ExecuteOneTypedFailureMapping(t *testing.T) {
	st, _, rootID := p4SetupRootPath(t, "/")
	ctx := context.Background()
	now := p4Now

	cases := []struct {
		kind      scan.ScopeFailureKind
		wantState state.WorkState
		wantRetry bool
	}{
		{scan.ScopeFailureAuthOrPermission, state.WorkBlocked, false},
		{scan.ScopeFailureThrottled, state.WorkRetryWait, true},
		{scan.ScopeFailureTooLarge, state.WorkBlocked, false},
		{scan.ScopeFailureTransientProvider, state.WorkRetryWait, true},
		{scan.ScopeFailureInvalidScope, state.WorkBlocked, false},
		{scan.ScopeFailureConfigInvalid, state.WorkBlocked, false},
		{scan.ScopeFailureRootInactive, state.WorkSuspended, false},
		{scan.ScopeFailureInternal, state.WorkRetryWait, true},
	}
	for i, tc := range cases {
		scope := fmt.Sprintf("/case%d", i)
		p4SeedPending(t, st, ctx, rootID, scope, now, nil)
		sc := &countingScanner{err: scan.NewScopeError(tc.kind, errors.New("typed failure"))}
		ex := p4Executor(t, st, sc, now)

		res, err := ex.ExecuteOne(ctx)
		if !errors.Is(err, incrementalexec.ErrScannerFailed) {
			t.Fatalf("%s: want ErrScannerFailed, got %v", tc.kind, err)
		}
		if res.FailureClass == nil || *res.FailureClass != state.ErrorClass(tc.kind) {
			t.Fatalf("%s: failure class = %v", tc.kind, res.FailureClass)
		}
		w, gerr := st.GetWork(ctx, rootID, scope)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if w.WorkState != tc.wantState {
			t.Fatalf("%s: work state = %s, want %s", tc.kind, w.WorkState, tc.wantState)
		}
		if w.LastErrorClass == nil || *w.LastErrorClass != state.ErrorClass(tc.kind) {
			t.Fatalf("%s: last_error_class = %v", tc.kind, w.LastErrorClass)
		}
		if tc.wantRetry {
			if w.PendingNotBefore == nil || !w.PendingNotBefore.After(now) {
				t.Fatalf("%s: RETRY_WAIT needs a future eligibility, got %v", tc.kind, w.PendingNotBefore)
			}
		} else if w.PendingNotBefore != nil {
			t.Fatalf("%s: non-retryable class must carry no retry eligibility, got %v", tc.kind, w.PendingNotBefore)
		}
	}
}

// TestP4ExecuteOnePostClaimBarrierSurvives proves a post-claim future barrier
// survives every terminal failure class.
func TestP4ExecuteOnePostClaimBarrierSurvives(t *testing.T) {
	st, _, rootID := p4SetupRootPath(t, "/")
	ctx := context.Background()
	now := p4Now

	for i, kind := range []scan.ScopeFailureKind{
		scan.ScopeFailureTransientProvider,
		scan.ScopeFailureAuthOrPermission,
		scan.ScopeFailureRootInactive,
	} {
		scope := fmt.Sprintf("/barrier%d", i)
		p4SeedPending(t, st, ctx, rootID, scope, now, nil)

		block := &blockingFailingScanner{
			entered: make(chan struct{}),
			release: make(chan struct{}),
			err:     scan.NewScopeError(kind, errors.New("failure")),
		}
		ex := p4Executor(t, st, block, now)
		done := make(chan error, 1)
		go func() {
			_, err := ex.ExecuteOne(ctx)
			done <- err
		}()
		<-block.entered

		later := now.Add(3 * time.Hour)
		if _, err := st.MergeSignal(ctx, state.DirtySignal{
			RootID: rootID, ScopeKey: scope, Source: state.SourceProviderEvent, Reason: state.ReasonPossibleChange,
			Priority: state.PriorityNormal, SeenAt: now.Add(time.Minute), NotBefore: &later,
		}); err != nil {
			t.Fatalf("%s: merge post-claim signal: %v", kind, err)
		}
		close(block.release)
		if err := <-done; !errors.Is(err, incrementalexec.ErrScannerFailed) {
			t.Fatalf("%s: want ErrScannerFailed, got %v", kind, err)
		}

		w, gerr := st.GetWork(ctx, rootID, scope)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if w.PendingNotBefore == nil || !w.PendingNotBefore.Equal(later) {
			t.Fatalf("%s: post-claim barrier must survive, got %v want %v", kind, w.PendingNotBefore, later)
		}
	}
}

// --- Stale selection --------------------------------------------------------

type staleSelectionStore struct {
	*postgres.Store
	hit func()
}

func (s *staleSelectionStore) NextEligiblePendingWork(ctx context.Context, now time.Time) (state.DirtyScopeWork, bool, error) {
	wk, found, err := s.Store.NextEligiblePendingWork(ctx, now)
	if err == nil && found && s.hit != nil {
		h := s.hit
		s.hit = nil
		h()
	}
	return wk, found, err
}

func TestP4ExecuteOneStaleSelectionNoScan(t *testing.T) {
	st, _, rootID := p4SetupRootPath(t, "/")
	ctx := context.Background()
	now := p4Now

	p4SeedPending(t, st, ctx, rootID, "/", now, nil)

	stale := &staleSelectionStore{Store: st}
	stale.hit = func() {
		if _, err := st.MergeSignal(ctx, state.DirtySignal{
			RootID: rootID, ScopeKey: "/", Source: state.SourceProviderEvent, Reason: state.ReasonPossibleChange,
			Priority: state.PriorityUrgent, SeenAt: now.Add(time.Minute),
		}); err != nil {
			t.Errorf("merge competing signal: %v", err)
		}
	}
	sc := &countingScanner{}
	ex := p4Executor(t, stale, sc, now)

	res, err := ex.ExecuteOne(ctx)
	if !errors.Is(err, incrementalexec.ErrStaleSelection) {
		t.Fatalf("want ErrStaleSelection, got %v", err)
	}
	if !res.Selected {
		t.Fatalf("result must expose the stale selection, got %+v", res)
	}
	if n := atomic.LoadInt32(&sc.calls); n != 0 {
		t.Fatalf("stale selection must not scan, got %d", n)
	}
	w, gerr := st.GetWork(ctx, rootID, "/")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if w.WorkState != state.WorkPending || w.AttemptCount != 0 {
		t.Fatalf("stale selection must not mutate work, got %+v", w)
	}
}

// --- Cancellation / recovery ------------------------------------------------

func TestP4ExecuteOneCancellationLeavesInflight(t *testing.T) {
	st, mock, rootID := p4SetupRootPath(t, "/")
	now := p4Now
	ctx, cancel := context.WithCancel(context.Background())

	mock.set("/", p4File("a.txt", 5, "aaa"))
	p4SeedPending(t, st, context.Background(), rootID, "/", now, nil)

	block := &blockingDelegatingScanner{
		inner:   p4RealScanner(st),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	ex := p4Executor(t, st, block, now)
	done := make(chan error, 1)
	go func() {
		_, err := ex.ExecuteOne(ctx)
		done <- err
	}()
	<-block.entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}

	w, err := st.GetWork(context.Background(), rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if w.WorkState != state.WorkInFlight || w.ClaimedSignalSeq == nil {
		t.Fatalf("cancellation must leave the claim IN_FLIGHT, got %+v", w)
	}
	if w.LastErrorClass != nil {
		t.Fatalf("caller cancellation must not write a failure class, got %v", *w.LastErrorClass)
	}

	recovered, err := st.RecoverStaleInflight(context.Background(), rootID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("explicit P3 recovery must requeue exactly one item, got %d", recovered)
	}
	w2, err := st.GetWork(context.Background(), rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if w2.WorkState != state.WorkPending {
		t.Fatalf("recovered work must be PENDING, got %s", w2.WorkState)
	}
}

// --- Completion failure -----------------------------------------------------

type failingCompleteStore struct {
	*postgres.Store
}

func (s *failingCompleteStore) CompleteSuccess(context.Context, string, string, int64, time.Time) (state.DirtyScopeWork, error) {
	return state.DirtyScopeWork{}, errors.New("injected completion failure")
}

func TestP4ExecuteOneCompletionFailure(t *testing.T) {
	st, mock, rootID := p4SetupRootPath(t, "/")
	ctx := context.Background()
	now := p4Now

	mock.set("/", p4File("a.txt", 5, "aaa"))
	p4SeedPending(t, st, ctx, rootID, "/", now, nil)

	ex := p4Executor(t, &failingCompleteStore{Store: st}, p4RealScanner(st), now)
	if _, err := ex.ExecuteOne(ctx); !errors.Is(err, incrementalexec.ErrCompletionFailed) {
		t.Fatalf("want ErrCompletionFailed, got %v", err)
	}
	if n := mock.refreshCount(); n != 1 {
		t.Fatalf("completion failure must not re-run the provider, refresh count %d", n)
	}
	w, err := st.GetWork(ctx, rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if w.WorkState != state.WorkInFlight {
		t.Fatalf("work must remain IN_FLIGHT after a failed completion, got %s", w.WorkState)
	}
}

// TestP4ExecuteOneNonVerifyingOutcome proves a nil scanner error that did not
// apply fresh coverage never verifies dirty work.
func TestP4ExecuteOneNonVerifyingOutcome(t *testing.T) {
	st, _, rootID := p4SetupRootPath(t, "/")
	ctx := context.Background()
	now := p4Now
	const scope = "/stale"
	p4SeedPending(t, st, ctx, rootID, scope, now, nil)

	sc := &countingScanner{result: scan.Result{
		SnapshotID: "snap-stale",
		Outcome:    postgres.ReconcileOutcome{Status: domain.AdmissionStaleInput},
	}}
	ex := p4Executor(t, st, sc, now)
	res, err := ex.ExecuteOne(ctx)
	if !errors.Is(err, incrementalexec.ErrScannerFailed) {
		t.Fatalf("want ErrScannerFailed, got %v", err)
	}
	if n := atomic.LoadInt32(&sc.calls); n != 1 {
		t.Fatalf("exactly one scan required, got %d", n)
	}
	if res.FailureClass == nil || *res.FailureClass != state.ErrorInternal {
		t.Fatalf("failure class = %v, want INTERNAL", res.FailureClass)
	}
	w, gerr := st.GetWork(ctx, rootID, scope)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if w.WorkState != state.WorkRetryWait {
		t.Fatalf("work must stay unsatisfied/retryable, got %s", w.WorkState)
	}
	if w.PendingNotBefore == nil {
		t.Fatal("RETRY_WAIT must carry a future retry eligibility")
	}
}

// TestP4ExecuteOnePostScanCancellation proves cancellation after a successful
// ScanScope but before completion leaves recoverable IN_FLIGHT work.
func TestP4ExecuteOnePostScanCancellation(t *testing.T) {
	st, _, rootID := p4SetupRootPath(t, "/")
	now := p4Now
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p4SeedPending(t, st, context.Background(), rootID, "/", now, nil)

	// The scanner returns a successful, applying result; cancellation becomes
	// observable before the executor reaches completion.
	sc := &countingScanner{fn: func(context.Context, string, string, int) (scan.Result, error) {
		cancel()
		return scan.Result{
			SnapshotID: "snap-ok",
			Outcome:    postgres.ReconcileOutcome{Status: domain.AdmissionApplied},
		}, nil
	}}
	ex := p4Executor(t, st, sc, now)
	if _, err := ex.ExecuteOne(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	w, err := st.GetWork(context.Background(), rootID, "/")
	if err != nil {
		t.Fatal(err)
	}
	if w.WorkState != state.WorkInFlight {
		t.Fatalf("post-scan cancellation must leave IN_FLIGHT, got %s", w.WorkState)
	}
	if w.LastErrorClass != nil {
		t.Fatalf("no failure class may be written, got %v", *w.LastErrorClass)
	}
	recovered, err := st.RecoverStaleInflight(context.Background(), rootID, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("explicit recovery must requeue 1 item, got %d", recovered)
	}
}

const p4LoginRoot = "a2000000-0000-0000-0000-0000000000a2"

func p4SetupRootWithLogin(t *testing.T, baseURL string) (*postgres.Store, string) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	if err := st.CreateRoot(ctx, st.Pool(), p4LoginRoot, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, p4LoginRoot, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	acfg, _ := json.Marshal(map[string]string{
		"base_url": baseURL, "path": "/",
		"username_env": "P4_TEST_LOGIN_USER", "password_env": "P4_TEST_LOGIN_PASS",
	})
	if err := st.UpsertAdapterConfig(ctx, p4LoginRoot,
		postgres.AdapterConfig{CollectorKind: "alist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}
	return st, p4LoginRoot
}

// TestP4ExecuteOneLoginPermissionIsAuth proves a scoped username/password login
// rejection is classified AUTH_OR_PERMISSION -> BLOCKED through the executor.
func TestP4ExecuteOneLoginPermissionIsAuth(t *testing.T) {
	t.Setenv("P4_TEST_LOGIN_USER", "operator")
	t.Setenv("P4_TEST_LOGIN_PASS", "secret")

	var listCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/api/auth/login") {
			_, _ = io.WriteString(w, `{"code":403,"message":"forbidden","data":null}`)
			return
		}
		atomic.AddInt32(&listCalls, 1)
		_, _ = io.WriteString(w, `{"code":200,"message":"ok","data":{"content":[],"total":0}}`)
	}))
	t.Cleanup(srv.Close)

	st, rootID := p4SetupRootWithLogin(t, srv.URL)
	ctx := context.Background()
	now := p4Now
	p4SeedPending(t, st, ctx, rootID, "/", now, nil)

	ex := p4Executor(t, st, p4RealScanner(st), now)
	res, err := ex.ExecuteOne(ctx)
	if !errors.Is(err, incrementalexec.ErrScannerFailed) {
		t.Fatalf("want ErrScannerFailed, got %v", err)
	}
	if res.FailureClass == nil || *res.FailureClass != state.ErrorAuthOrPermission {
		t.Fatalf("failure class = %v, want AUTH_OR_PERMISSION", res.FailureClass)
	}
	if n := atomic.LoadInt32(&listCalls); n != 0 {
		t.Fatalf("a failed login must not list, got %d list calls", n)
	}
	w, gerr := st.GetWork(ctx, rootID, "/")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if w.WorkState != state.WorkBlocked {
		t.Fatalf("AUTH_OR_PERMISSION must BLOCK, got %s", w.WorkState)
	}
}

// demoteAfterClaimStore demotes the root immediately after a successful claim,
// reproducing the selector-proved-ACTIVE -> root-deprecated-before-ScanScope
// race.
type demoteAfterClaimStore struct {
	*postgres.Store
	rootID string
	done   bool
}

func (s *demoteAfterClaimStore) ClaimWork(ctx context.Context, rootID, scopeKey string, expectedVersion int64, now time.Time) (state.DirtyScopeWork, error) {
	wk, err := s.Store.ClaimWork(ctx, rootID, scopeKey, expectedVersion, now)
	if err == nil && !s.done {
		s.done = true
		if _, terr := s.Store.TransitionRootLifecycle(ctx, s.rootID, domain.RootDeprecated); terr != nil {
			return state.DirtyScopeWork{}, terr
		}
	}
	return wk, err
}

// TestP4ExecuteOneLifecycleDemotedAfterClaim proves the scoped actuator fails
// closed when the root stops being ACTIVE after the claim committed.
func TestP4ExecuteOneLifecycleDemotedAfterClaim(t *testing.T) {
	st, mock, rootID := p4SetupRootPath(t, "/")
	ctx := context.Background()
	now := p4Now

	mock.set("/", p4File("a.txt", 5, "aaa"))
	p4SeedPending(t, st, ctx, rootID, "/", now, nil)

	ex := p4Executor(t, &demoteAfterClaimStore{Store: st, rootID: rootID}, p4RealScanner(st), now)
	res, err := ex.ExecuteOne(ctx)
	if !errors.Is(err, incrementalexec.ErrScannerFailed) {
		t.Fatalf("want ErrScannerFailed, got %v", err)
	}
	if res.FailureClass == nil || *res.FailureClass != state.ErrorRootInactive {
		t.Fatalf("failure class = %v, want ROOT_INACTIVE", res.FailureClass)
	}
	if n := mock.refreshCount(); n != 0 {
		t.Fatalf("a non-ACTIVE root must not reach the provider, got %d requests", n)
	}
	w, gerr := st.GetWork(ctx, rootID, "/")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if w.WorkState != state.WorkSuspended {
		t.Fatalf("ROOT_INACTIVE must SUSPEND the work, got %s", w.WorkState)
	}
}
