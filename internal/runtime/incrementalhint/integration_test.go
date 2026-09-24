package incrementalhint_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalhint"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

const (
	p8RootActive   = "b8000000-0000-0000-0000-0000000000a1"
	p8RootInactive = "b8000000-0000-0000-0000-0000000000a2"
)

type p8Clock struct {
	t     time.Time
	mutex sync.Mutex
}

func (c *p8Clock) now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.t
}

func (c *p8Clock) set(t time.Time) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.t = t
}

func p8NewStore(t *testing.T) (*postgres.Store, context.Context) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return postgres.New(pool), ctx
}

func p8AddRoot(t *testing.T, st *postgres.Store, ctx context.Context, rootID, baseURL string, lifecycle domain.RootLifecycleState) {
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
	if lifecycle != domain.RootActive {
		if _, err := st.TransitionRootLifecycle(ctx, rootID, lifecycle); err != nil {
			t.Fatalf("transition root to %s: %v", lifecycle, err)
		}
	}
}

func p8ServiceWithClock(t *testing.T, st incrementalhint.Store, now func() time.Time) *incrementalhint.Service {
	t.Helper()
	s, err := incrementalhint.New(st, now)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return s
}

func p8SeedWatch(t *testing.T, st *postgres.Store, ctx context.Context, rootID, scopeKey string, dueAt time.Time) {
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

func p8Claim(t *testing.T, st *postgres.Store, ctx context.Context, rootID, scopeKey string, now time.Time) state.DirtyScopeWork {
	t.Helper()
	w, err := st.GetWork(ctx, rootID, scopeKey)
	if err != nil {
		t.Fatalf("get work: %v", err)
	}
	c, err := st.ClaimWork(ctx, rootID, scopeKey, w.Version, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return c
}

func p8GetWork(t *testing.T, st *postgres.Store, ctx context.Context, rootID, scopeKey string) state.DirtyScopeWork {
	t.Helper()
	w, err := st.GetWork(ctx, rootID, scopeKey)
	if err != nil {
		t.Fatalf("get work: %v", err)
	}
	return w
}

func p8CountWorkRows(t *testing.T, st *postgres.Store, ctx context.Context, rootID string) int {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM index_dirty_scope_work WHERE root_id=$1::uuid`, rootID).Scan(&n); err != nil {
		t.Fatalf("count work rows: %v", err)
	}
	return n
}

// p8AListMock is a mutable single-page AList listing that counts forced
// refresh requests.
type p8AListMock struct {
	mu      sync.Mutex
	content []map[string]any
	refresh int32
}

func (m *p8AListMock) set(content ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, len(content))
	copy(out, content)
	m.content = out
}

func (m *p8AListMock) refreshCount() int { return int(atomic.LoadInt32(&m.refresh)) }

func p8FileEntry(name string, size int64, sha1 string) map[string]any {
	return map[string]any{
		"name": name, "size": size, "is_dir": false,
		"modified": "2026-01-02T03:04:05Z", "hash_info": map[string]string{"sha1": sha1},
	}
}

func p8NewAListServer(t *testing.T) (*httptest.Server, *p8AListMock) {
	t.Helper()
	mock := &p8AListMock{}
	mock.set(p8FileEntry("a.txt", 5, "aaa"))
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

func p8RealExecutor(t *testing.T, st *postgres.Store, now time.Time) incrementalexec.OneShotExecutor {
	t.Helper()
	svc := scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	one, err := incrementalexec.New(st, svc, incrementalexec.Config{
		MaxEntriesPerScope: 1000,
		Retry: incrementalexec.RetryPolicy{
			TransientProvider: 30 * time.Second, Throttled: 45 * time.Second, Internal: 60 * time.Second,
		},
	}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	return one
}

func p8CycleRunner(t *testing.T, one incrementalexec.OneShotExecutor) *incrementalexec.CycleRunner {
	t.Helper()
	c, err := incrementalexec.NewCycleRunner(one)
	if err != nil {
		t.Fatalf("new cycle runner: %v", err)
	}
	return c
}

func p8AssertPresent(t *testing.T, st *postgres.Store, rootID, path string) {
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

func p8AssertNoRemovalEvidence(t *testing.T, st *postgres.Store, rootID, path string) {
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

func p8Contains(sources []state.TriggerSource, want state.TriggerSource) bool {
	for _, s := range sources {
		if s == want {
			return true
		}
	}
	return false
}

func TestP8ActiveRootFirstHint(t *testing.T) {
	st, ctx := p8NewStore(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, "http://127.0.0.1:1", domain.RootActive)
	svc := p8ServiceWithClock(t, st, func() time.Time { return now })

	got, err := svc.IngestOne(ctx, incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/a"})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	w := p8GetWork(t, st, ctx, p8RootActive, "/a")
	if w.WorkState != state.WorkPending {
		t.Fatalf("work state = %s, want PENDING", w.WorkState)
	}
	if w.SignalSeq != 1 {
		t.Fatalf("signal_seq = %d, want 1", w.SignalSeq)
	}
	if !p8Contains(w.PendingSourceSet, state.SourceMutationHint) || len(w.PendingSourceSet) != 1 {
		t.Fatalf("pending_source_set = %v, want [MUTATION_HINT]", w.PendingSourceSet)
	}
	if w.PendingPriority == nil || *w.PendingPriority != state.PriorityHigh {
		t.Fatalf("pending_priority = %v, want HIGH", w.PendingPriority)
	}
	if w.PendingFirstSeenAt == nil || !w.PendingFirstSeenAt.UTC().Equal(now) {
		t.Fatalf("pending_first_seen_at = %v, want %v", w.PendingFirstSeenAt, now)
	}
	if w.PendingNotBefore == nil || !w.PendingNotBefore.UTC().Equal(now) {
		t.Fatalf("pending_not_before = %v, want %v", w.PendingNotBefore, now)
	}
	if !w.LastSeenAt.UTC().Equal(now) {
		t.Fatalf("last_seen_at = %v, want %v", w.LastSeenAt, now)
	}
	if got.SignalSeq != 1 {
		t.Fatalf("returned work signal_seq = %d", got.SignalSeq)
	}
	if p8CountWorkRows(t, st, ctx, p8RootActive) != 1 {
		t.Fatal("exactly one work row expected")
	}
}

func TestP8DuplicateReplayCoalesces(t *testing.T) {
	st, ctx := p8NewStore(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, "http://127.0.0.1:1", domain.RootActive)
	clock := &p8Clock{t: now}
	svc := p8ServiceWithClock(t, st, clock.now)
	req := incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/a"}
	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	clock.set(now.Add(time.Minute))
	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}

	w := p8GetWork(t, st, ctx, p8RootActive, "/a")
	// P8 is row-coalescing, not external-event-id idempotent.
	if w.SignalSeq != 2 {
		t.Fatalf("signal_seq = %d, want 2 (one increment per accepted call)", w.SignalSeq)
	}
	if p8CountWorkRows(t, st, ctx, p8RootActive) != 1 {
		t.Fatal("repeated hints must coalesce into one row")
	}
	if len(w.PendingSourceSet) != 1 || !p8Contains(w.PendingSourceSet, state.SourceMutationHint) {
		t.Fatalf("pending_source_set must stay de-duplicated, got %v", w.PendingSourceSet)
	}
	if len(w.PendingReasonSet) != 1 {
		t.Fatalf("pending_reason_set must stay de-duplicated, got %v", w.PendingReasonSet)
	}
	if w.PendingFirstSeenAt == nil || !w.PendingFirstSeenAt.UTC().Equal(now) {
		t.Fatalf("pending_first_seen_at must remain earliest, got %v", w.PendingFirstSeenAt)
	}
	if !w.LastSeenAt.UTC().Equal(now.Add(time.Minute)) {
		t.Fatalf("last_seen_at must advance to second acceptance, got %v", w.LastSeenAt)
	}
}

func TestP8VerifiedReopensNewEpoch(t *testing.T) {
	st, ctx := p8NewStore(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, "http://127.0.0.1:1", domain.RootActive)
	clock := &p8Clock{t: now}
	svc := p8ServiceWithClock(t, st, clock.now)
	req := incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/a"}
	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	claimed := p8Claim(t, st, ctx, p8RootActive, "/a", now)
	if _, err := st.CompleteSuccess(ctx, p8RootActive, "/a", *claimed.ClaimedSignalSeq, now); err != nil {
		t.Fatalf("complete success: %v", err)
	}
	if w := p8GetWork(t, st, ctx, p8RootActive, "/a"); w.WorkState != state.WorkVerified {
		t.Fatalf("precondition: work state = %s, want VERIFIED", w.WorkState)
	}

	clock.set(now.Add(time.Hour))
	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	w := p8GetWork(t, st, ctx, p8RootActive, "/a")
	if w.WorkState != state.WorkPending {
		t.Fatalf("new epoch state = %s, want PENDING", w.WorkState)
	}
	if w.SignalSeq != 2 {
		t.Fatalf("signal_seq = %d, want 2", w.SignalSeq)
	}
	if len(w.PendingSourceSet) != 1 || !p8Contains(w.PendingSourceSet, state.SourceMutationHint) {
		t.Fatalf("new epoch provenance must be only the new hint, got %v", w.PendingSourceSet)
	}
	if w.PendingFirstSeenAt == nil || !w.PendingFirstSeenAt.UTC().Equal(now.Add(time.Hour)) {
		t.Fatalf("pending_first_seen_at = %v, want reset to new accepted time", w.PendingFirstSeenAt)
	}
	if !w.LastSeenAt.UTC().Equal(now.Add(time.Hour)) {
		t.Fatalf("last_seen_at = %v, want new accepted time", w.LastSeenAt)
	}
	if w.LastVerifiedSignalSeq == nil || *w.LastVerifiedSignalSeq != 1 {
		t.Fatalf("last_verified_signal_seq = %v, want 1", w.LastVerifiedSignalSeq)
	}
}

func TestP8InFlightLostWakeup(t *testing.T) {
	st, ctx := p8NewStore(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, "http://127.0.0.1:1", domain.RootActive)
	clock := &p8Clock{t: now}
	svc := p8ServiceWithClock(t, st, clock.now)
	req := incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/a"}
	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	claimed := p8Claim(t, st, ctx, p8RootActive, "/a", now)
	claimedSources := claimed.ClaimedSourceSet
	if claimed.WorkState != state.WorkInFlight {
		t.Fatalf("precondition: work state = %s, want IN_FLIGHT", claimed.WorkState)
	}

	clock.set(now.Add(time.Minute))
	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	w := p8GetWork(t, st, ctx, p8RootActive, "/a")
	if w.WorkState != state.WorkInFlight {
		t.Fatalf("state = %s, want IN_FLIGHT", w.WorkState)
	}
	if w.ClaimedSignalSeq == nil || *w.ClaimedSignalSeq != 1 {
		t.Fatalf("claimed_signal_seq = %v, want unchanged 1", w.ClaimedSignalSeq)
	}
	if !reflect.DeepEqual(w.ClaimedSourceSet, claimedSources) {
		t.Fatalf("claimed_source_set changed: %v -> %v", claimedSources, w.ClaimedSourceSet)
	}
	if w.SignalSeq != 2 {
		t.Fatalf("signal_seq = %d, want 2", w.SignalSeq)
	}
	if !p8Contains(w.PendingSourceSet, state.SourceMutationHint) {
		t.Fatalf("hint must land in pending_*, got %v", w.PendingSourceSet)
	}

	if _, err := st.CompleteSuccess(ctx, p8RootActive, "/a", 1, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("complete success: %v", err)
	}
	w2 := p8GetWork(t, st, ctx, p8RootActive, "/a")
	if w2.WorkState != state.WorkPending {
		t.Fatalf("late hint must survive older success, state = %s", w2.WorkState)
	}
	if !p8Contains(w2.PendingSourceSet, state.SourceMutationHint) {
		t.Fatalf("late hint provenance lost, got %v", w2.PendingSourceSet)
	}
	if w2.LastVerifiedSignalSeq == nil || *w2.LastVerifiedSignalSeq != 1 {
		t.Fatalf("last_verified_signal_seq = %v, want 1", w2.LastVerifiedSignalSeq)
	}
}

func TestP8RetryWaitBarrierPreserved(t *testing.T) {
	st, ctx := p8NewStore(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, "http://127.0.0.1:1", domain.RootActive)
	clock := &p8Clock{t: now}
	svc := p8ServiceWithClock(t, st, clock.now)
	req := incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/a"}
	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	claimed := p8Claim(t, st, ctx, p8RootActive, "/a", now)
	future := now.Add(time.Hour)
	if _, err := st.CompleteFailure(ctx, p8RootActive, "/a", *claimed.ClaimedSignalSeq,
		state.ErrorTransientProvider, &future, now); err != nil {
		t.Fatalf("complete failure: %v", err)
	}
	before := p8GetWork(t, st, ctx, p8RootActive, "/a")
	if before.WorkState != state.WorkRetryWait {
		t.Fatalf("precondition: state = %s", before.WorkState)
	}

	clock.set(now.Add(time.Minute))
	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	w := p8GetWork(t, st, ctx, p8RootActive, "/a")
	if w.WorkState != state.WorkRetryWait {
		t.Fatalf("state = %s, want RETRY_WAIT (no auto RetryReady)", w.WorkState)
	}
	if w.SignalSeq != before.SignalSeq+1 {
		t.Fatalf("signal_seq = %d, want %d", w.SignalSeq, before.SignalSeq+1)
	}
	if w.PendingNotBefore == nil || !w.PendingNotBefore.UTC().Equal(future) {
		t.Fatalf("retry barrier changed: %v -> %v", before.PendingNotBefore, w.PendingNotBefore)
	}
	if !p8Contains(w.PendingSourceSet, state.SourceMutationHint) {
		t.Fatalf("hint provenance must merge, got %v", w.PendingSourceSet)
	}
}

func TestP8BlockedPreserved(t *testing.T) {
	st, ctx := p8NewStore(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, "http://127.0.0.1:1", domain.RootActive)
	svc := p8ServiceWithClock(t, st, func() time.Time { return now })
	req := incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/a"}
	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	claimed := p8Claim(t, st, ctx, p8RootActive, "/a", now)
	if _, err := st.CompleteFailure(ctx, p8RootActive, "/a", *claimed.ClaimedSignalSeq,
		state.ErrorAuthOrPermission, nil, now); err != nil {
		t.Fatalf("complete failure: %v", err)
	}
	if before := p8GetWork(t, st, ctx, p8RootActive, "/a"); before.WorkState != state.WorkBlocked {
		t.Fatalf("precondition: state = %s", before.WorkState)
	}

	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	w := p8GetWork(t, st, ctx, p8RootActive, "/a")
	if w.WorkState != state.WorkBlocked {
		t.Fatalf("state = %s, want BLOCKED (no repair)", w.WorkState)
	}
	if w.SignalSeq != 2 {
		t.Fatalf("signal_seq = %d, want 2", w.SignalSeq)
	}
	if !p8Contains(w.PendingSourceSet, state.SourceMutationHint) {
		t.Fatalf("hint provenance must be retained, got %v", w.PendingSourceSet)
	}
}

func TestP8InactiveRootSuspended(t *testing.T) {
	st, ctx := p8NewStore(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootInactive, "http://127.0.0.1:1", domain.RootDeprecated)
	svc := p8ServiceWithClock(t, st, func() time.Time { return now })
	req := incrementalhint.Request{RootID: p8RootInactive, ScopeKey: "/a"}

	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	if w := p8GetWork(t, st, ctx, p8RootInactive, "/a"); w.WorkState != state.WorkSuspended {
		t.Fatalf("first hint on inactive root: state = %s, want SUSPENDED", w.WorkState)
	}
	if _, err := svc.IngestOne(ctx, req); err != nil {
		t.Fatal(err)
	}
	w := p8GetWork(t, st, ctx, p8RootInactive, "/a")
	if w.WorkState != state.WorkSuspended {
		t.Fatalf("subsequent hint must remain SUSPENDED, got %s", w.WorkState)
	}
	root, err := st.GetRoot(ctx, st.Pool(), p8RootInactive)
	if err != nil {
		t.Fatal(err)
	}
	if root.LifecycleState != domain.RootDeprecated {
		t.Fatalf("hint must not activate a root, lifecycle = %s", root.LifecycleState)
	}
}

func TestP8WatchPolicyUntouched(t *testing.T) {
	st, ctx := p8NewStore(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, "http://127.0.0.1:1", domain.RootActive)
	p8SeedWatch(t, st, ctx, p8RootActive, "/a", now.Add(-time.Hour))
	before, err := st.GetWatch(ctx, p8RootActive, "/a")
	if err != nil {
		t.Fatal(err)
	}
	svc := p8ServiceWithClock(t, st, func() time.Time { return now })
	if _, err := svc.IngestOne(ctx, incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/a"}); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetWatch(ctx, p8RootActive, "/a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("hint-only ingestion must not mutate the watch:\n before=%+v\n after =%+v", before, after)
	}
}
func p8ExecConfig() incrementalexec.Config {
	return incrementalexec.Config{
		MaxEntriesPerScope: 1000,
		Retry: incrementalexec.RetryPolicy{
			TransientProvider: 30 * time.Second, Throttled: 45 * time.Second, Internal: 60 * time.Second,
		},
	}
}

func p8ExecutorWithScanner(t *testing.T, st *postgres.Store, scanner incrementalexec.ScopedScanner, now time.Time) incrementalexec.OneShotExecutor {
	t.Helper()
	one, err := incrementalexec.New(st, scanner, p8ExecConfig(), func() time.Time { return now })
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	return one
}

// p8BlockingScanner blocks the first ScanScope call until released, allowing a
// test to observe the claim-scoped provenance while the attempt is in flight.
type p8BlockingScanner struct {
	inner   incrementalexec.ScopedScanner
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *p8BlockingScanner) ScanScope(ctx context.Context, rootID, scope string, maxEntries int) (scan.Result, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return b.inner.ScanScope(ctx, rootID, scope, maxEntries)
	case <-ctx.Done():
		return scan.Result{}, ctx.Err()
	}
}

func TestP8HintOnlyExecutionIsNotPollSuccess(t *testing.T) {
	st, ctx := p8NewStore(t)
	srv, mock := p8NewAListServer(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, srv.URL, domain.RootActive)
	// A watch exists but is not due, so no POLL_SCHEDULE is emitted.
	p8SeedWatch(t, st, ctx, p8RootActive, "/", now.Add(time.Hour))
	before, err := st.GetWatch(ctx, p8RootActive, "/")
	if err != nil {
		t.Fatal(err)
	}

	svc := p8ServiceWithClock(t, st, func() time.Time { return now })
	if _, err := svc.IngestOne(ctx, incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/"}); err != nil {
		t.Fatal(err)
	}

	realSvc := scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	block := &p8BlockingScanner{inner: realSvc, entered: make(chan struct{}), release: make(chan struct{})}
	cycle := p8CycleRunner(t, p8ExecutorWithScanner(t, st, block, now))

	done := make(chan error, 1)
	go func() {
		_, err := cycle.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 30 * time.Second})
		done <- err
	}()
	<-block.entered

	w := p8GetWork(t, st, ctx, p8RootActive, "/")
	if !p8Contains(w.ClaimedSourceSet, state.SourceMutationHint) {
		t.Fatalf("claim provenance must include MUTATION_HINT, got %v", w.ClaimedSourceSet)
	}
	if p8Contains(w.ClaimedSourceSet, state.SourcePollSchedule) {
		t.Fatalf("hint-only claim must not include POLL_SCHEDULE, got %v", w.ClaimedSourceSet)
	}
	if mid, err := st.GetWatch(ctx, p8RootActive, "/"); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(before, mid) {
		t.Fatalf("hint-only claim must not touch poll health:\n before=%+v\n mid   =%+v", before, mid)
	}

	close(block.release)
	if err := <-done; err != nil {
		t.Fatalf("cycle: %v", err)
	}
	final := p8GetWork(t, st, ctx, p8RootActive, "/")
	if final.WorkState != state.WorkVerified {
		t.Fatalf("work state = %s, want VERIFIED", final.WorkState)
	}
	after, err := st.GetWatch(ctx, p8RootActive, "/")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("hint-only execution must not update poll bookkeeping:\n before=%+v\n after =%+v", before, after)
	}
	if got := mock.refreshCount(); got != 1 {
		t.Fatalf("exactly one provider refresh expected, got %d", got)
	}
}

func TestP8CoalescedPollAndHintAttribution(t *testing.T) {
	st, ctx := p8NewStore(t)
	srv, mock := p8NewAListServer(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, srv.URL, domain.RootActive)
	p8SeedWatch(t, st, ctx, p8RootActive, "/", now.Add(-time.Hour))
	beforeWatch, err := st.GetWatch(ctx, p8RootActive, "/")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.EmitDuePoll(ctx, p8RootActive, "/", beforeWatch.Version, now); err != nil {
		t.Fatalf("emit due poll: %v", err)
	}
	svc := p8ServiceWithClock(t, st, func() time.Time { return now })
	if _, err := svc.IngestOne(ctx, incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/"}); err != nil {
		t.Fatal(err)
	}

	coalesced := p8GetWork(t, st, ctx, p8RootActive, "/")
	if !p8Contains(coalesced.PendingSourceSet, state.SourcePollSchedule) ||
		!p8Contains(coalesced.PendingSourceSet, state.SourceMutationHint) {
		t.Fatalf("coalesced epoch must contain both sources, got %v", coalesced.PendingSourceSet)
	}
	seq := coalesced.SignalSeq

	cycle := p8CycleRunner(t, p8RealExecutor(t, st, now))
	res, err := cycle.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 30 * time.Second})
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if res.SelectedItems != 1 || res.Succeeded != 1 {
		t.Fatalf("one execution must satisfy the coalesced epoch, got %+v", res)
	}
	final := p8GetWork(t, st, ctx, p8RootActive, "/")
	if final.WorkState != state.WorkVerified {
		t.Fatalf("work state = %s, want VERIFIED", final.WorkState)
	}
	if final.SignalSeq != seq {
		t.Fatalf("no extra signal expected, signal_seq %d -> %d", seq, final.SignalSeq)
	}
	afterWatch, err := st.GetWatch(ctx, p8RootActive, "/")
	if err != nil {
		t.Fatal(err)
	}
	if afterWatch.LastSuccessAt == nil {
		t.Fatal("watch attribution must occur because POLL_SCHEDULE was claimed")
	}
	if got := mock.refreshCount(); got != 1 {
		t.Fatalf("exactly one provider refresh expected, got %d", got)
	}
}

func TestP8RealEndToEnd(t *testing.T) {
	st, ctx := p8NewStore(t)
	srv, mock := p8NewAListServer(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, srv.URL, domain.RootActive)
	svc := p8ServiceWithClock(t, st, func() time.Time { return now })

	wk, err := svc.IngestOne(ctx, incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/"})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if wk.WorkState != state.WorkPending {
		t.Fatalf("ingress state = %s, want PENDING", wk.WorkState)
	}

	cycle := p8CycleRunner(t, p8RealExecutor(t, st, now))
	res, err := cycle.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 30 * time.Second})
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if res.SelectedItems != 1 || res.Succeeded != 1 {
		t.Fatalf("executor result = %+v", res)
	}
	final := p8GetWork(t, st, ctx, p8RootActive, "/")
	if final.WorkState != state.WorkVerified {
		t.Fatalf("work state = %s, want VERIFIED", final.WorkState)
	}
	p8AssertPresent(t, st, p8RootActive, "/a.txt")
	if got := mock.refreshCount(); got != 1 {
		t.Fatalf("exactly one refresh=true request expected, got %d", got)
	}
}

func TestP8DeleteHintDoesNotRemove(t *testing.T) {
	st, ctx := p8NewStore(t)
	srv, mock := p8NewAListServer(t)
	mock.set(p8FileEntry("old.txt", 4, "old"), p8FileEntry("keep.txt", 4, "keep"))
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, srv.URL, domain.RootActive)
	clock := &p8Clock{t: now}
	svc := p8ServiceWithClock(t, st, clock.now)
	cfg := incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 30 * time.Second}

	// Establish canonical truth through an accepted hint-only ingestion.
	if _, err := svc.IngestOne(ctx, incrementalhint.Request{
		RootID: p8RootActive, ScopeKey: "/", Reason: state.ReasonPossibleChange,
	}); err != nil {
		t.Fatal(err)
	}
	cycle := p8CycleRunner(t, p8RealExecutor(t, st, now))
	if _, err := cycle.Run(ctx, cfg); err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	p8AssertPresent(t, st, p8RootActive, "/old.txt")
	p8AssertPresent(t, st, p8RootActive, "/keep.txt")

	// DELETE_HINT, then the scoped refresh omits /old.txt.
	mock.set(p8FileEntry("keep.txt", 4, "keep"))
	clock.set(now.Add(time.Minute))
	if _, err := svc.IngestOne(ctx, incrementalhint.Request{
		RootID: p8RootActive, ScopeKey: "/", Reason: state.ReasonDeleteHint,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cycle.Run(ctx, cfg); err != nil {
		t.Fatalf("second cycle: %v", err)
	}

	p8AssertPresent(t, st, p8RootActive, "/old.txt")
	p8AssertPresent(t, st, p8RootActive, "/keep.txt")
	p8AssertNoRemovalEvidence(t, st, p8RootActive, "/old.txt")
}

func TestP8InvalidProviderScopeDeferredToExecutor(t *testing.T) {
	st, ctx := p8NewStore(t)
	srv, mock := p8NewAListServer(t)
	now := p8Now
	p8AddRoot(t, st, ctx, p8RootActive, srv.URL, domain.RootActive)
	svc := p8ServiceWithClock(t, st, func() time.Time { return now })

	wk, err := svc.IngestOne(ctx, incrementalhint.Request{RootID: p8RootActive, ScopeKey: "/missing"})
	if err != nil {
		t.Fatalf("ingress must accept a syntactically valid scope: %v", err)
	}
	if wk.WorkState != state.WorkPending {
		t.Fatalf("ingress state = %s, want PENDING", wk.WorkState)
	}
	if got := mock.refreshCount(); got != 0 {
		t.Fatalf("ingress must not call the provider, got %d requests", got)
	}

	cycle := p8CycleRunner(t, p8RealExecutor(t, st, now))
	if _, err := cycle.Run(ctx, incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 30 * time.Second}); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	final := p8GetWork(t, st, ctx, p8RootActive, "/missing")
	if final.WorkState != state.WorkBlocked {
		t.Fatalf("executor must classify a nonexistent scope as BLOCKED, got %s", final.WorkState)
	}
	if final.LastErrorClass == nil || *final.LastErrorClass != state.ErrorInvalidScope {
		t.Fatalf("last_error_class = %v, want INVALID_SCOPE", final.LastErrorClass)
	}
	if got := mock.refreshCount(); got != 0 {
		t.Fatalf("INVALID_SCOPE must not reach the provider, got %d requests", got)
	}
}
