package incrementalexec_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"reflect"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalhint"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Test-only model. This is NOT a new production scheduler or configuration.
func TestIssue134F3BudgetModel(t *testing.T) {
	const historic = 48.267
	for _, n := range []int{425, 2000, 20000, 50000} {
		offered := float64(n) / 2
		background := historic / 2
		roundMinutes := float64(n) / background
		if offered <= historic || roundMinutes <= 2 {
			t.Fatal("capacity hypothesis", n)
		}
		t.Logf("F3_MODEL dirs=%d fixed120_due_per_min=%.3f historical_capacity=%.3f background50_capacity=%.3f minimum_round_minutes=%.3f illustrative_unserved_intents_per_min=%.3f", n, offered, historic, background, roundMinutes, math.Max(0, offered-historic))
	}
	// Conservative downward adaptation, reserving trusted/root/retry service.
	estimate := 48.0
	debt := 0.0
	for minute := 0; minute < 120; minute++ {
		actual := 48.0
		if minute >= 40 && minute < 80 {
			actual = 24
		}
		estimate = math.Min(actual, 0.8*estimate+0.2*actual)
		background := math.Floor(estimate / 2)
		debt = math.Max(0, debt+12+background-actual)
		if debt != 0 {
			t.Fatal("modeled admissible workload grew", minute, debt)
		}
	}
	// Trusted demand above capacity is overload, never dropped or called stable.
	debt = 0
	for i := 0; i < 10; i++ {
		debt += 12 - 8
	}
	if debt != 40 {
		t.Fatal(debt)
	}
	t.Log("F3_MODEL adaptive_120min_workload=12_trusted_plus_budgeted_background modeled_debt=0 overloaded_12_trusted_vs8_service_debt_after10min=40; no runtime adaptation exists")
}

// HTTP fixture control, never a real provider endpoint. The delay is real wall
// time; all calls still go through the accepted collector/Kernel.
type f3HTTP struct {
	qualified     atomic.Bool
	calls, waitNS atomic.Int64
}

func f3AttachHTTP(t *testing.T, f *f2Fixture, delay time.Duration) *f3HTTP {
	t.Helper()
	a, err := f.st.GetAdapterConfig(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(a.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(cfg["base_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	m := &f3HTTP{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/admin/storage/list" {
			driver := "Alias"
			if m.qualified.Load() {
				driver = "115 Open"
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"id": 1, "mount_path": "/fixture", "driver": driver, "status": "work", "disabled": false}}}})
			return
		}
		if r.URL.Path == "/api/fs/list" {
			start := time.Now()
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				http.Error(w, "fixture canceled", 499)
				return
			}
			m.calls.Add(1)
			m.waitNS.Add(int64(time.Since(start)))
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("F3_FIXTURE_TOPOLOGY", "fixture-only-topology")
	cfg["base_url"] = srv.URL
	cfg["topology_token_env"] = "F3_FIXTURE_TOPOLOGY"
	a.Config, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.UpsertAdapterConfig(context.Background(), f.root, a); err != nil {
		t.Fatal(err)
	}
	return m
}

// SQL setup is a population/load fixture, NOT50k successful source observations.
// Call only after Issue134Pool verifies the disposable target, before reset.
func f3Seed(t *testing.T, f *f2Fixture, n int) {
	t.Helper()
	ctx := context.Background()
	old := time.Now().UTC().Add(-2 * time.Hour)
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	scope := func(i int) string {
		if i%100 == 0 {
			return fmt.Sprintf("/g%04d", i/100)
		}
		return fmt.Sprintf("/g%04d/d%02d", i/100, i%100)
	}
	copyRows := func(table string, cols []string, fn func(int) ([]any, error), count int) {
		i := 0
		got, err := f.st.Pool().CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromFunc(func() ([]any, error) {
			if i == count {
				return nil, nil
			}
			row, err := fn(i)
			i++
			return row, err
		}))
		if err != nil || got != int64(count) {
			t.Fatal("seed", table, got, err)
		}
	}
	copyRows("index_canonical_resource", []string{"resource_id", "root_id", "introduced_at_generation", "last_confirmed_generation", "resource_presence", "removal_evidence_state", "canonical_path", "name", "parent_resource_id", "is_dir", "size", "mtime", "updated_at"}, func(i int) ([]any, error) {
		var parent any
		if i%100 != 0 {
			parent = fmt.Sprintf("f3000000-0000-4000-8000-%012d", i/100*100+1)
		}
		return []any{fmt.Sprintf("f3000000-0000-4000-8000-%012d", i+1), f.root, 0, 0, "PRESENT", "NONE", "/library" + scope(i), path.Base(scope(i)), parent, true, int64(0), mtime, old}, nil
	}, n)
	copyRows("index_dirty_scope_work", []string{"root_id", "scope_key", "work_state", "signal_seq", "last_seen_at", "attempt_count", "last_verified_at", "last_verified_signal_seq"}, func(i int) ([]any, error) {
		s := scope(i)
		if i == n {
			s = "/"
		}
		return []any{f.root, s, "VERIFIED", int64(1), old, int64(1), old.Add(time.Hour), int64(1)}, nil
	}, n+1)
	copyRows("index_scope_watch_state", []string{"root_id", "scope_key", "watch_state", "cadence_class", "effective_interval_seconds", "source_set", "priority_class", "next_due_at"}, func(i int) ([]any, error) {
		return []any{f.root, scope(i), "WARM", "MIRROR_REVISIT", int64(120), []string{"BACKSTOP_ENROLL"}, "NORMAL", time.Now().UTC().Add(120 * time.Second)}, nil
	}, n)
	roots := []map[string]any{}
	for g := 0; g < n/100; g++ {
		name := fmt.Sprintf("g%04d", g)
		roots = append(roots, p4Dir(name))
		children := []map[string]any{}
		for d := 1; d < 100; d++ {
			children = append(children, p4Dir(fmt.Sprintf("d%02d", d)))
		}
		f.mock.set("/library/"+name, children...)
	}
	f.mock.set("/library", roots...)
	sec := int64(30)
	due := time.Now().UTC().Add(30 * time.Second)
	if _, err := f.st.CreateWatch(ctx, state.ScopeWatchState{RootID: f.root, ScopeKey: "/", WatchState: state.WatchHot, CadenceClass: "SOURCE_MIRROR", SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy}, Priority: state.PriorityHigh, EffectiveIntervalSeconds: &sec, NextDueAt: &due}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool().Exec(ctx, "ANALYZE"); err != nil {
		t.Fatal(err)
	}
}

type f3SQL struct {
	sql  string
	args []any
}

func (q *f3SQL) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	q.sql = d.SQL
	q.args = append([]any(nil), d.Args...)
	return ctx
}
func (q *f3SQL) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func f3QueryEvidence(t *testing.T, f *f2Fixture, n int) {
	t.Helper()
	trace := &f3SQL{}
	cfg, err := pgxpool.ParseConfig(testutil.TestDSN())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = trace
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	st := postgres.New(pool)
	ctx := context.Background()
	for _, name := range []string{"legacy_due", "fair_due", "pending", "first_discovery", "canonical_root"} {
		times := []time.Duration{}
		samples := 10
		if name == "canonical_root" {
			samples = 3
		}
		for i := 0; i < samples; i++ {
			start := time.Now()
			switch name {
			case "legacy_due":
				_, err = st.ListDueWatches(ctx, f.now(), 6)
			case "fair_due":
				_, err = st.ListDueWatchesWithMirrorFairness(ctx, f.now(), 6)
			case "pending":
				_, _, err = st.NextEligiblePendingWork(ctx, f.now())
			case "first_discovery":
				_, _, err = st.NextFirstDiscoveryWork(ctx, f.now(), nil)
			case "canonical_root":
				_, err = st.ListCanonicalResources(ctx, pool, f.root)
			}
			if err != nil {
				t.Fatal(name, err)
			}
			times = append(times, time.Since(start))
		}
		sql, args := trace.sql, trace.args
		var plan json.RawMessage
		if err := pool.QueryRow(ctx, "EXPLAIN(ANALYZE,BUFFERS,FORMAT JSON) "+sql, args...).Scan(&plan); err != nil {
			t.Fatal(name, err)
		}
		t.Logf("F3_SQL dirs=%d query=%s N=%d client_ms_p50_p95_p99=%v actual_plan=%s", n, name, samples, f2Quantiles(times), plan)
	}
}

func f3Queue(t *testing.T, f *f2Fixture, n int, phase string) {
	t.Helper()
	var watches, due, pending int
	var oldestDue, oldestPending float64
	var size, temp int64
	var blocked int
	ctx := context.Background()
	err := f.st.Pool().QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE next_due_at<=$1),coalesce(max(extract(epoch FROM $1-next_due_at)) FILTER(WHERE next_due_at<=$1),0) FROM index_scope_watch_state WHERE root_id=$2 AND watch_state IN('HOT','WARM')`, f.now(), f.root).Scan(&watches, &due, &oldestDue)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.Pool().QueryRow(ctx, `SELECT count(*),coalesce(max(extract(epoch FROM $1-pending_first_seen_at)),0) FROM index_dirty_scope_work WHERE root_id=$2 AND work_state='PENDING'`, f.now(), f.root).Scan(&pending, &oldestPending); err != nil {
		t.Fatal(err)
	}
	if err = f.st.Pool().QueryRow(ctx, `SELECT pg_database_size(current_database()),temp_bytes FROM pg_stat_database WHERE datname=current_database()`).Scan(&size, &temp); err != nil {
		t.Fatal(err)
	}
	if err = f.st.Pool().QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND cardinality(pg_blocking_pids(pid))>0`).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("F3_QUEUE dirs=%d phase=%s active_watches=%d due_watches=%d oldest_due_s=%.3f pending=%d oldest_pending_s=%.3f db_bytes=%d db_temp_bytes=%d blocked_sessions_sample=%d heap_alloc_bytes=%d", n, phase, watches, due, oldestDue, pending, oldestPending, size, temp, blocked, mem.HeapAlloc)
	if watches < n || due < n-50 {
		t.Fatal("storm fixture lost durable intent", watches, due)
	}
}

type f3ServiceTiming struct {
	trace   *f2TimedScanner
	calls   int
	elapsed time.Duration
}

func (s *f3ServiceTiming) ScanScope(ctx context.Context, root, scope string, limit int) (scan.Result, error) {
	start := time.Now()
	r, err := s.trace.ScanScope(ctx, root, scope, limit)
	s.calls++
	s.elapsed += time.Since(start)
	return r, err
}

func TestIssue134F3Capacity(t *testing.T) {
	raw := os.Getenv("INDEXCORE_ISSUE134_F3_SCALE")
	if raw == "" {
		t.Skip("explicit bounded F3 population fixture required")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || (n != 2000 && n != 20000 && n != 50000) {
		t.Fatal("F3 scale must be exactly2000/20000/50000")
	}
	testutil.Issue134Pool(t) // BEFORE f2Setup schema reset or population write.
	f := f2Setup(t, 2*time.Minute)
	f3Seed(t, f, n)
	source := f3AttachHTTP(t, f, 1250*time.Millisecond)
	f.offset = 121 * time.Second
	f.rebuild(t, true)
	trace := &f2TimedScanner{inner: p4RealScanner(f.st), start: time.Now(), claims: map[string]time.Duration{}, canonical: map[string]time.Duration{}, snapshots: map[string]string{}}
	cfg := p4Config()
	cfg.DiscoveryScope = ""
	cfg.FirstDiscoveryEnabled = true
	cfg.MirrorRevisitInterval = 2 * time.Minute
	service := &f3ServiceTiming{trace: trace}
	f.ex, err = incrementalexec.New(f.st, service, cfg, f.now)
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := incrementalexec.NewCycleRunner(f.ex)
	if err != nil {
		t.Fatal(err)
	}
	orch, err := incrementalorch.NewRunner(f.st, cycle, f.now)
	if err != nil {
		t.Fatal(err)
	}
	run := func(phase string, round int) {
		_, err := f.st.MergeSignal(context.Background(), state.DirtySignal{RootID: f.root, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify, Priority: state.PriorityHigh, SeenAt: f.now()})
		if err != nil {
			t.Fatal(err)
		}
		r, err := orch.RunCycle(context.Background(), incrementalorch.Config{MirrorRevisitEnabled: true, MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: 60 * time.Second})
		if err != nil {
			t.Fatal(r, err)
		}
		t.Logf("F3_CYCLE dirs=%d phase=%s round=%d due_emitted=%d execute_completed=%d", n, phase, round, r.DueEmitted, r.Executor.Succeeded)
		f3Queue(t, f, n, fmt.Sprintf("%s-%d", phase, round))
	}
	f3Queue(t, f, n, "initial_due_storm")
	for i := 0; i < 2; i++ {
		run("no_change", i)
	}
	// Real HTTP source gets1% late files, but only bounded observed samples are
	// accepted as correct. Unobserved remainder is explicitly counted, not passed.
	start := time.Now()
	trace.start = start
	trace.order = nil
	trace.claims = map[string]time.Duration{}
	trace.canonical = map[string]time.Duration{}
	for g := 0; g < n/100; g++ {
		d := 1
		if g == 0 {
			d = 8
		} // known queued/due old directory, deliberately no Hint
		f.mock.set(fmt.Sprintf("/library/g%04d/d%02d", g, d), p4File("late.txt", 1, fmt.Sprintf("f3-%d", g)))
	}
	children := []map[string]any{}
	for d := 1; d < 100; d++ {
		children = append(children, p4Dir(fmt.Sprintf("d%02d", d)))
	}
	children = append(children, p4Dir("fresh"))
	f.mock.set("/library/g0000", children...)
	f.mock.set("/library/g0000/fresh", p4Dir("nested"))
	f.mock.set("/library/g0000/fresh/nested", p4File("new.txt", 2, "f3-new"))
	f.parent(t, "/g0000")
	discovered := time.Since(start)
	oldScope := fmt.Sprintf("/g%04d/d99", n/100-1)
	if _, err = f.st.MergeSignal(context.Background(), state.DirtySignal{RootID: f.root, ScopeKey: oldScope, Source: state.SourceRecovery, Reason: state.ReasonPossibleChange, Priority: state.PriorityNormal, SeenAt: f.now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	hint, err := incrementalhint.New(f.st, f.now)
	if err != nil {
		t.Fatal(err)
	}
	targets := []string{}
	for j := 0; j < 8; j++ {
		scope := fmt.Sprintf("/g%04d/d01", j*(n/100-1)/7)
		if j == 0 {
			scope = "/g0000/d08"
		}
		targets = append(targets, scope)
		if j == 0 {
			continue
		}
		if _, err = hint.IngestOne(context.Background(), incrementalhint.Request{RootID: f.root, ScopeKey: scope}); err != nil {
			t.Fatal(err)
		}
	}
	f3QueryEvidence(t, f, n)
	for i := 0; i < 4; i++ {
		run("change_hint_fresh_root_storm", i)
	}
	claims, canonical := []time.Duration{}, []time.Duration{}
	for _, scope := range append(targets, "/g0000/fresh/nested") {
		name := "late.txt"
		if scope == "/g0000/fresh/nested" {
			name = "new.txt"
		}
		f2AssertQ5Q8(t, f, "/library"+scope+"/"+name)
		var snapshot string
		if err := f.st.Pool().QueryRow(context.Background(), `SELECT g.produced_by_snapshot_id::text FROM index_generation g JOIN index_canonical_resource c ON c.root_id=g.root_id AND c.introduced_at_generation=g.generation_number WHERE c.root_id=$1 AND c.canonical_path=$2 AND c.resource_presence='PRESENT'`, f.root, "/library"+scope+"/"+name).Scan(&snapshot); err != nil || snapshot != trace.snapshots[scope] {
			t.Fatal("timing not anchored to producing Snapshot", scope, err)
		}
		c, ok := trace.claims[scope]
		done, exists := trace.canonical[scope]
		if !ok || !exists || done < c {
			t.Fatal("target not served within bounded window", scope)
		}
		claims = append(claims, c)
		canonical = append(canonical, done)
	}
	if _, ok := trace.claims[oldScope]; !ok {
		t.Fatal("aged NORMAL starved")
	}
	fresh, ok := trace.claims["/g0000/fresh"]
	if !ok || fresh < discovered {
		t.Fatal("fresh child did not progress")
	}
	var observed, removed int
	if err = f.st.Pool().QueryRow(context.Background(), `SELECT count(*) FILTER(WHERE canonical_path LIKE '%/late.txt' AND resource_presence='PRESENT'),count(*) FILTER(WHERE resource_presence='REMOVED') FROM index_canonical_resource WHERE root_id=$1`, f.root).Scan(&observed, &removed); err != nil || removed != 0 {
		t.Fatal("canonical safety", observed, removed, err)
	}
	if source.calls.Load() != int64(f.mock.refreshCount()) {
		t.Fatal("unexpected non-forced/list calls")
	}
	elapsed := time.Since(trace.start)
	t.Logf("F3_SERVICE dirs=%d real_scans=%d scan_service_s=%.3f forced_http_wait_s=%.3f scan_remainder_s=%.3f measured_serial_service_scopes_per_min=%.3f", n, service.calls, service.elapsed.Seconds(), float64(source.waitNS.Load())/1e9, (service.elapsed - time.Duration(source.waitNS.Load())).Seconds(), float64(service.calls)*60/service.elapsed.Seconds())
	t.Logf("F3_RESULT dirs=%d source_changes=%d observed_change_files=%d unobserved_changes=%d sampled_targets=%d source_to_claim_ms_p50_p95_p99=%v source_to_canonical_return_ms_p50_p95_p99=%v fresh_discovery_to_claim_ms=%d aged_normal_claim_ms=%d sample_window_s=%.3f cumulative_forced_calls=%d cumulative_http_wait_s=%.3f no_false_removals=true full_traversal=NOT_RUN", n, n/100, observed, n/100-observed, len(canonical), f2Quantiles(claims), f2Quantiles(canonical), (fresh - discovered).Milliseconds(), trace.claims[oldScope].Milliseconds(), elapsed.Seconds(), source.calls.Load(), float64(source.waitNS.Load())/1e9)
}

func TestIssue134F3WatchLifecycleEvidence(t *testing.T) {
	f := f2Setup(t, 2*time.Minute)
	source := f3AttachHTTP(t, f, 0)
	source.qualified.Store(true)
	f.mock.set("/library", p4Dir("gone"), p4Dir("keep"))
	f.parent(t, "/")
	f.drain(t, 5)
	ctx := context.Background()
	var oldID string
	if err := f.st.Pool().QueryRow(ctx, `SELECT resource_id::text FROM index_canonical_resource WHERE canonical_path='/library/gone' AND resource_presence='PRESENT'`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	f.mock.set("/library", p4Dir("keep"))
	f.parent(t, "/")
	var removed int
	if err := f.st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_journal_event WHERE event_type='resource-removed' AND resource_id=$1`, oldID).Scan(&removed); err != nil || removed != 1 {
		t.Fatal("real trusted removal not committed", removed, err)
	}
	f.offset = 121 * time.Second
	w, err := f.st.GetWatch(ctx, f.root, "/gone")
	if err != nil || !w.Due(f.now()) {
		t.Fatal("expected currently retained stale watch", w, err)
	}
	before, err := f.st.GetWork(ctx, f.root, "/gone")
	if err != nil {
		t.Fatal(err)
	}
	original := w
	w.WatchState = state.WatchDisabled
	w.NextDueAt = nil
	w.EffectiveIntervalSeconds = nil
	if _, err = f.st.CASUpdateWatch(ctx, w, w.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.CASUpdateWatch(ctx, w, original.Version); err != postgres.ErrStateCASConflict {
		t.Fatal("stale policy CAS allowed", err)
	}
	after, err := f.st.GetWork(ctx, f.root, "/gone")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("manual policy CAS changed Work history", err)
	}
	// Existing manual CAS is not an automatic identity-aware retirement API.
	f.mock.set("/library", p4Dir("gone"), p4Dir("keep"))
	f.parent(t, "/")
	var newID string
	if err = f.st.Pool().QueryRow(ctx, `SELECT resource_id::text FROM index_canonical_resource WHERE canonical_path='/library/gone' AND resource_presence='PRESENT'`).Scan(&newID); err != nil || newID == oldID {
		t.Fatal("path reuse resurrected identity", err)
	}
	w, err = f.st.GetWatch(ctx, f.root, "/gone")
	if err != nil || w.WatchState != state.WatchDisabled {
		t.Fatal("old disabled watch unexpectedly rebound", err)
	}
	source.qualified.Store(false)
	f.mock.set("/library")
	f.parent(t, "/")
	p4AssertPresent(t, f.st, "/library/gone")
	p4AssertPresent(t, f.st, "/library/keep")
	p4AssertNoRemovalEvidence(t, f.st, "/library/gone")
	// An auth-blocked Work still owns its intent; due polling does not retire it
	// or clear failure history. This is not permission to retry a real provider.
	if _, err = f.st.MergeSignal(ctx, state.DirtySignal{RootID: f.root, ScopeKey: "/keep", Source: state.SourceRecovery, Reason: state.ReasonPossibleChange, Priority: state.PriorityNormal, SeenAt: f.now()}); err != nil {
		t.Fatal(err)
	}
	keep, err := f.st.GetWork(ctx, f.root, "/keep")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := f.st.ClaimWork(ctx, f.root, "/keep", keep.Version, f.now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.CompleteFailure(ctx, f.root, "/keep", *claimed.ClaimedSignalSeq, state.ErrorAuthOrPermission, nil, f.now()); err != nil {
		t.Fatal(err)
	}
	kw, err := f.st.GetWatch(ctx, f.root, "/keep")
	if err != nil || !kw.Due(f.now()) {
		t.Fatal("blocked retained-watch fixture", err)
	}
	blocked, err := f.st.EmitDuePoll(ctx, f.root, "/keep", kw.Version, f.now())
	if err != nil || blocked.WorkState != state.WorkBlocked || blocked.ConsecutiveFailures != 1 {
		t.Fatal("poll bypassed blocked state", blocked, err)
	}
	pool, err := pgxpool.New(ctx, testutil.TestDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	restarted := postgres.New(pool)
	durable, err := restarted.GetWork(ctx, f.root, "/keep")
	if err != nil || !reflect.DeepEqual(blocked, durable) {
		t.Fatal("restart lost intent/history", err)
	}
	if _, err = f.st.TransitionRootLifecycle(ctx, f.root, domain.RootDeprecated); err != nil {
		t.Fatal(err)
	}
	due, err := f.st.ListDueWatchesWithMirrorFairness(ctx, f.now(), 6)
	if err != nil || len(due) != 0 {
		t.Fatal("inactive root still selected", err)
	}
	var count int
	if err = f.st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_scope_watch_state WHERE root_id=$1`, f.root).Scan(&count); err != nil || count != 2 {
		t.Fatal("history unexpectedly reclaimed", count, err)
	}
	t.Log("F3_LIFECYCLE trusted_removed_watch_still_due=true manual_CAS_preserves_Work=true stale_CAS_refused=true replaced_identity_not_rebound=true blocked_intent_preserved=true new_pool_restart=true inactive_due_filtered=true retained_watches=2 automatic_retirement=ABSENT")
}
