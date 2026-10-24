package incrementalexec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalhint"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

type f2Fixture struct {
	st       *postgres.Store
	mock     *p4AListMock
	root     string
	offset   time.Duration
	ex       *incrementalexec.Executor
	interval time.Duration
}

func f2Setup(t *testing.T, interval time.Duration) *f2Fixture {
	t.Helper()
	st, mock, root := p4SetupRootPath(t, "/library")
	a, err := st.GetAdapterConfig(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(a.Config, &raw); err != nil {
		t.Fatal(err)
	}
	raw["mirror_presence"] = true
	raw["expected_base_path"] = "/fixture"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/me" {
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"base_path": "/fixture"}})
			return
		}
		if r.URL.Path == "/api/admin/storage/list" {
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"id": 1, "mount_path": "/fixture/library", "driver": "Alias", "status": "work", "disabled": false}}}})
			return
		}
		mock.handler()(w, r)
	}))
	t.Cleanup(server.Close)
	raw["base_url"] = server.URL
	a.Config, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAdapterConfig(context.Background(), root, a); err != nil {
		t.Fatal(err)
	}
	f := &f2Fixture{st: st, mock: mock, root: root, interval: interval}
	f.rebuild(t, false)
	return f
}

func (f *f2Fixture) now() time.Time { return time.Now().UTC().Add(f.offset) }
func (f *f2Fixture) rebuild(t *testing.T, first bool) {
	t.Helper()
	cfg := p4Config()
	cfg.DiscoveryScope = ""
	cfg.MirrorRevisitInterval = f.interval
	cfg.FirstDiscoveryEnabled = first
	var err error
	f.ex, err = incrementalexec.New(postgres.New(f.st.Pool()), p4RealScanner(f.st), cfg, f.now)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *f2Fixture) parent(t *testing.T, scope string) incrementalexec.Result {
	t.Helper()
	_, err := f.st.MergeSignal(context.Background(), state.DirtySignal{RootID: f.root, ScopeKey: scope, Source: state.SourceManualOperator, Reason: state.ReasonManualVerify, Priority: state.PriorityHigh, SeenAt: f.now()})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.ex.ExecuteOne(context.Background())
	if err != nil || r.ScopeKey != scope {
		t.Fatal("parent", r, err)
	}
	return r
}

func (f *f2Fixture) drain(t *testing.T, max int) int {
	t.Helper()
	for i := 0; i <= max; i++ {
		_, err := f.ex.ExecuteOne(context.Background())
		if errors.Is(err, incrementalexec.ErrNoEligibleWork) {
			return i
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("unbounded queue drain")
	return 0
}

func f2AssertQ5Q8(t *testing.T, f *f2Fixture, path string) {
	t.Helper()
	q := postgres.NewQueryReader(f.st.Pool())
	r, err := q.ResolvePath(context.Background(), f.root, path, query.ReadOptions{})
	if err != nil || len(r.Matches) != 1 {
		t.Fatal("Q5", path, r, err)
	}
	var added, removed int
	if err := f.st.Pool().QueryRow(context.Background(), `SELECT count(*) FILTER(WHERE event_type='resource-added' AND resource_id=$2),count(*) FILTER(WHERE event_type='resource-removed') FROM index_journal_event WHERE root_id=$1`, f.root, r.Matches[0].ResourceID).Scan(&added, &removed); err != nil || added != 1 || removed != 0 {
		t.Fatal("Q8", added, removed, err)
	}
}

func TestIssue134MirrorLateFilesNestedHintAndRestart(t *testing.T) {
	f := f2Setup(t, 2*time.Minute)
	f.mock.set("/library", p4Dir("old"), p4Dir("hinted"))
	f.mock.set("/library/old", p4Dir("nested"))
	f.mock.set("/library/old/nested")
	f.mock.set("/library/hinted")
	f.parent(t, "/")
	if n := f.drain(t, 10); n != 3 {
		t.Fatal(n)
	}
	before := f.mock.refreshCount()
	for i := 0; i < 4; i++ {
		f.offset = time.Duration(i) * 30 * time.Second // logical HOT cadence
		r := f.parent(t, "/")
		if r.Discovery.VerifiedReused != 2 || f.drain(t, 5) != 0 {
			t.Fatal(r.Discovery)
		}
	}
	if f.mock.refreshCount() != before+4 {
		t.Fatal("repeated parents caused child refreshes")
	}
	// Parent directory metadata remains byte-identical. No mtime revision exists.
	f.mock.set("/library/old", p4Dir("nested"), p4File("late.txt", 1, "late-one"))
	f.mock.set("/library/old/nested", p4File("later.txt", 2, "late-two"))
	f.offset = 121 * time.Second // explicit eligibility clock, not a wall benchmark
	f.rebuild(t, false)
	runner, _ := incrementalexec.NewCycleRunner(f.ex)
	orch, err := incrementalorch.NewRunner(postgres.New(f.st.Pool()), runner, f.now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := orch.RunCycle(context.Background(), incrementalorch.Config{MirrorRevisitEnabled: true, MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: 60 * time.Second})
	if err != nil || res.DueEmitted != 3 {
		t.Fatal(res, err)
	}
	f.drain(t, 10)
	f2AssertQ5Q8(t, f, "/library/old/late.txt")
	f2AssertQ5Q8(t, f, "/library/old/nested/later.txt")
	// Formal Hint is immediate before the next periodic deadline, not suppressed.
	f.mock.set("/library/hinted", p4File("hint.txt", 3, "hint-three"))
	hint, _ := incrementalhint.New(f.st, f.now)
	w, err := hint.IngestOne(context.Background(), incrementalhint.Request{RootID: f.root, ScopeKey: "/hinted"})
	if err != nil || w.WorkState != state.WorkPending {
		t.Fatal(w, err)
	}
	if r, err := f.ex.ExecuteOne(context.Background()); err != nil || r.ScopeKey != "/hinted" {
		t.Fatal(r, err)
	}
	f2AssertQ5Q8(t, f, "/library/hinted/hint.txt")
	// Missing/empty source without topology authority remains additive-only.
	a, err := f.st.GetAdapterConfig(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(a.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("F2_FIXTURE_TOPOLOGY", "test-only-topology")
	cfg["topology_token_env"] = "F2_FIXTURE_TOPOLOGY"
	a.Config, err = json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertAdapterConfig(context.Background(), f.root, a); err != nil {
		t.Fatal(err)
	}
	f.mock.set("/library/old/nested")
	f.parent(t, "/old/nested")
	f2AssertQ5Q8(t, f, "/library/old/nested/later.txt")
	p4AssertNoRemovalEvidence(t, f.st, "/library/old/nested/later.txt")
	t.Logf("late-file case: repeated_parent_refreshes=4 repeated_child_refreshes=0 Q5/Q8=3_unique_file_additions removed=0")
}

func TestIssue134MirrorDuplicateAmplification100And401(t *testing.T) {
	for _, count := range []int{110, 401} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("children=%d/enabled=%t", count, enabled), func(t *testing.T) {
				interval := time.Duration(0)
				if enabled {
					interval = time.Hour
				}
				f := f2Setup(t, interval)
				dirs := []map[string]any{}
				for i := 0; i < count; i++ {
					name := fmt.Sprintf("old-%04d", i)
					dirs = append(dirs, p4Dir(name))
					f.mock.set("/library/" + name)
				}
				f.mock.set("/library", dirs...)
				f.parent(t, "/")
				if n := f.drain(t, count+1); n != count {
					t.Fatal(n)
				}
				before := f.mock.refreshCount()
				merged := 0
				reused := 0
				peak := 0
				var seqBefore, seqAfter int64
				if err := f.st.Pool().QueryRow(context.Background(), `SELECT sum(signal_seq) FROM index_dirty_scope_work WHERE root_id=$1`, f.root).Scan(&seqBefore); err != nil {
					t.Fatal(err)
				}
				for cycle := 0; cycle < 3; cycle++ {
					r := f.parent(t, "/")
					merged += r.Discovery.SignalsMerged
					reused += r.Discovery.VerifiedReused
					var pending int
					if err := f.st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM index_dirty_scope_work WHERE root_id=$1 AND work_state='PENDING'`, f.root).Scan(&pending); err != nil {
						t.Fatal(err)
					}
					if pending > peak {
						peak = pending
					}
					n := f.drain(t, count+1)
					if enabled && (n != 0 || pending != 0 || r.Discovery.VerifiedReused != count) {
						t.Fatal(n, pending, r.Discovery)
					}
					if !enabled && (n != count || pending != count) {
						t.Fatal(n, pending)
					}
				}
				calls := f.mock.refreshCount() - before
				if err := f.st.Pool().QueryRow(context.Background(), `SELECT sum(signal_seq) FROM index_dirty_scope_work WHERE root_id=$1`, f.root).Scan(&seqAfter); err != nil {
					t.Fatal(err)
				}
				fanoutSignals := seqAfter - seqBefore - 3
				if enabled && fanoutSignals != 0 || !enabled && fanoutSignals != int64(3*count) {
					t.Fatal("signal amplification measurement", fanoutSignals)
				}
				if enabled && (calls != 3 || merged != 0 || reused != 3*count) {
					t.Fatal(calls, merged, reused)
				}
				if !enabled && calls != 3*(count+1) {
					t.Fatal(calls)
				}
				f2AssertQ5Q8(t, f, "/library/old-0000")
				t.Logf("AMPLIFICATION children=%d enabled=%t parent_observations=3 forced_refreshes=%d peak_pending=%d eligible_injected_per_parent=%d measured_fanout_signals=%d committed_policy_merges=%d verified_reused=%d end_pending=0", count, enabled, calls, peak, peak, fanoutSignals, merged, reused)
			})
		}
	}
}

// Optional heavier 2k nested fixture. Explicitly mandatory in its dedicated CI
// step; it is not 20k/50k capacity acceptance and never uses real providers.
func TestIssue134Mirror2kNestedFixture(t *testing.T) {
	if os.Getenv("INDEXCORE_ISSUE134_F2_SCALE") != "2000" {
		t.Skip("explicit owned-PG18 2k fixture required")
	}
	testutil.Issue134Pool(t)
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			interval := time.Duration(0)
			if enabled {
				interval = time.Hour
			}
			f := f2Setup(t, interval)
			groups := []map[string]any{}
			for g := 0; g < 4; g++ {
				group := fmt.Sprintf("group-%d", g)
				groups = append(groups, p4Dir(group))
				dirs := []map[string]any{}
				for i := 0; i < 499; i++ {
					name := fmt.Sprintf("old-%04d", i)
					dirs = append(dirs, p4Dir(name))
					f.mock.set("/library/" + group + "/" + name)
				}
				f.mock.set("/library/"+group, dirs...)
			}
			f.mock.set("/library", groups...)
			f.parent(t, "/")
			if n := f.drain(t, 2100); n != 2000 {
				t.Fatal(n)
			}
			baseline := f.mock.refreshCount()
			peak := 0
			merged := 0
			reused := 0
			var seqBefore, seqAfter int64
			if err := f.st.Pool().QueryRow(context.Background(), `SELECT sum(signal_seq) FROM index_dirty_scope_work WHERE root_id=$1`, f.root).Scan(&seqBefore); err != nil {
				t.Fatal(err)
			}
			for observation := 0; observation < 2; observation++ {
				r := f.parent(t, "/")
				merged += r.Discovery.SignalsMerged
				reused += r.Discovery.VerifiedReused
				for g := 0; g < 4; g++ {
					r = f.parent(t, fmt.Sprintf("/group-%d", g))
					merged += r.Discovery.SignalsMerged
					reused += r.Discovery.VerifiedReused
				}
				var pending int
				if err := f.st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM index_dirty_scope_work WHERE root_id=$1 AND work_state='PENDING'`, f.root).Scan(&pending); err != nil {
					t.Fatal(err)
				}
				if pending > peak {
					peak = pending
				}
				if enabled && pending != 0 {
					t.Fatal("ordinary fanout amplification", pending)
				}
				if !enabled && pending != 1996 {
					t.Fatal("baseline not reproduced", pending)
				}
			}
			if err := f.st.Pool().QueryRow(context.Background(), `SELECT sum(signal_seq) FROM index_dirty_scope_work WHERE root_id=$1`, f.root).Scan(&seqAfter); err != nil {
				t.Fatal(err)
			}
			fanoutSignals := seqAfter - seqBefore - 10
			if enabled && fanoutSignals != 0 || !enabled && fanoutSignals != 4000 {
				t.Fatal("2k merge amplification", fanoutSignals)
			}
			f2AssertQ5Q8(t, f, "/library/group-0/old-0000")
			// Real late file in an old nested directory, discovered by formal Hint.
			f.mock.set("/library/group-0/old-0000", p4File("late.txt", 1, "scale-late"))
			hint, _ := incrementalhint.New(f.st, f.now)
			if _, err := hint.IngestOne(context.Background(), incrementalhint.Request{RootID: f.root, ScopeKey: "/group-0/old-0000"}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.ex.ExecuteOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			f2AssertQ5Q8(t, f, "/library/group-0/old-0000/late.txt")
			t.Logf("NESTED2000 enabled=%t initial_forced_refreshes=%d repeat_parent_refreshes=%d peak_pending=%d measured_fanout_signals=%d committed_policy_merges=%d verified_reused=%d", enabled, baseline, f.mock.refreshCount()-baseline-1, peak, fanoutSignals, merged, reused)
		})
	}
}

func f2Quantiles(samples []time.Duration) []int64 {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	out := []int64{}
	for _, q := range []float64{.5, .95, .99} {
		i := int(math.Ceil(q*float64(len(samples)))) - 1
		out = append(out, samples[i].Milliseconds())
	}
	return out
}

type f2TimedScanner struct {
	inner             incrementalexec.ScopedScanner
	start             time.Time
	claims, canonical map[string]time.Duration
	snapshots         map[string]string
	order             []string
}

func (s *f2TimedScanner) ScanScope(ctx context.Context, root, scope string, cap int) (scan.Result, error) {
	s.order = append(s.order, scope)
	s.claims[scope] = time.Since(s.start)
	r, err := s.inner.ScanScope(ctx, root, scope, cap)
	if err == nil {
		s.canonical[scope] = time.Since(s.start)
		s.snapshots[scope] = r.SnapshotID
	}
	return r, err
}

// Real elapsed time, including the actual30s periodic deadline, not the virtual
// clock used in the fast functional test. One bounded N40 sample per mode.
func TestIssue134Mirror400RealLatency(t *testing.T) {
	if os.Getenv("INDEXCORE_ISSUE134_F2_PERF") != "1" {
		t.Skip("explicit owned-PG18 real-time latency fixture required")
	}
	testutil.Issue134Pool(t)
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			interval := time.Duration(0)
			if enabled {
				interval = 30 * time.Second
			}
			f := f2Setup(t, interval)
			dirs := []map[string]any{}
			for i := 0; i < 401; i++ {
				name := fmt.Sprintf("old-%04d", i)
				dirs = append(dirs, p4Dir(name))
				f.mock.set("/library/" + name)
			}
			f.mock.set("/library", dirs...)
			seconds := int64(30)
			due := f.now().Add(30 * time.Second)
			if _, err := f.st.CreateWatch(context.Background(), state.ScopeWatchState{RootID: f.root, ScopeKey: "/", WatchState: state.WatchHot, CadenceClass: "SOURCE_MIRROR", SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy}, Priority: state.PriorityHigh, EffectiveIntervalSeconds: &seconds, NextDueAt: &due}); err != nil {
				t.Fatal(err)
			}
			f.parent(t, "/")
			f.drain(t, 402)
			before := f.mock.refreshCount()
			trace := &f2TimedScanner{inner: p4RealScanner(f.st), claims: map[string]time.Duration{}, canonical: map[string]time.Duration{}, snapshots: map[string]string{}, start: time.Now()}
			cfg := p4Config()
			cfg.DiscoveryScope = ""
			cfg.MirrorRevisitInterval = interval
			var err error
			f.ex, err = incrementalexec.New(f.st, trace, cfg, f.now)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 400; i += 10 {
				f.mock.set(fmt.Sprintf("/library/old-%04d", i), p4File("late.txt", 1, fmt.Sprintf("late-%d", i)))
			}
			{
				scope := "/"
				if enabled {
					scope = "/old-0000"
				}
				w, err := f.st.GetWatch(context.Background(), f.root, scope)
				if err != nil {
					t.Fatal(err)
				}
				wait := time.Until(*w.NextDueAt)
				if wait > 0 {
					timer := time.NewTimer(wait)
					<-timer.C
				}
			}
			cycle, _ := incrementalexec.NewCycleRunner(f.ex)
			orch, _ := incrementalorch.NewRunner(f.st, cycle, f.now)
			for round := 0; round < 100; round++ {
				result, err := orch.RunCycle(context.Background(), incrementalorch.Config{MirrorRevisitEnabled: enabled, MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: 60 * time.Second})
				if err != nil {
					t.Fatal(result, err)
				}
				if _, ok := trace.canonical["/old-0390"]; ok {
					break
				}
				if round == 99 {
					t.Fatal("bounded400 fixture did not progress")
				}
			}
			claims, canonical, service := []time.Duration{}, []time.Duration{}, []time.Duration{}
			for i := 0; i < 400; i += 10 {
				scope := fmt.Sprintf("/old-%04d", i)
				path := "/library" + scope + "/late.txt"
				f2AssertQ5Q8(t, f, path)
				claim, cok := trace.claims[scope]
				done, dok := trace.canonical[scope]
				if !cok || !dok || done < claim {
					t.Fatal("missing timing", scope)
				}
				claims = append(claims, claim)
				canonical = append(canonical, done)
				service = append(service, done-claim)
				var snapshot string
				if err := f.st.Pool().QueryRow(context.Background(), `SELECT g.produced_by_snapshot_id FROM index_generation g JOIN index_canonical_resource c ON c.root_id=g.root_id AND c.introduced_at_generation=g.generation_number WHERE c.root_id=$1 AND c.canonical_path=$2 AND c.resource_presence='PRESENT'`, f.root, path).Scan(&snapshot); err != nil || snapshot != trace.snapshots[scope] {
					t.Fatal("timing not anchored to producing Snapshot", err)
				}
			}
			t.Logf("LATENCY400 enabled=%t N=40 actual_source_to_claim_ms_p50_p95_p99=%v actual_source_to_scan_committed_return_ms_p50_p95_p99=%v actual_scan_service_ms_p50_p95_p99=%v forced_refreshes=%d interval=%s", enabled, f2Quantiles(claims), f2Quantiles(canonical), f2Quantiles(service), f.mock.refreshCount()-before, interval)
		})
	}
}

func TestIssue134MirrorKeepsF1FastLane(t *testing.T) {
	f := f2Setup(t, 2*time.Minute)
	dirs := []map[string]any{}
	for i := 0; i < 41; i++ {
		name := fmt.Sprintf("old-%03d", i)
		dirs = append(dirs, p4Dir(name))
		f.mock.set("/library/" + name)
	}
	f.mock.set("/library", dirs...)
	f.parent(t, "/")
	f.drain(t, 42)
	for i := 0; i < 41; i++ {
		if _, err := f.st.MergeSignal(context.Background(), state.DirtySignal{RootID: f.root, ScopeKey: fmt.Sprintf("/old-%03d", i), Source: state.SourceRecovery, Reason: state.ReasonPossibleChange, Priority: state.PriorityNormal, SeenAt: f.now().Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	f.mock.set("/library", append(dirs, p4Dir("fresh"))...)
	f.mock.set("/library/fresh", p4Dir("nested"))
	f.mock.set("/library/fresh/nested", p4File("new.txt", 1, "f1f2"))
	f.parent(t, "/")
	trace := &f2TimedScanner{inner: p4RealScanner(f.st), start: time.Now(), claims: map[string]time.Duration{}, canonical: map[string]time.Duration{}, snapshots: map[string]string{}}
	cfg := p4Config()
	cfg.DiscoveryScope = ""
	cfg.MirrorRevisitInterval = f.interval
	cfg.FirstDiscoveryEnabled = true
	ex, err := incrementalexec.New(f.st, trace, cfg, f.now)
	if err != nil {
		t.Fatal(err)
	}
	cycle, _ := incrementalexec.NewCycleRunner(ex)
	for i := 0; i < 2; i++ {
		if _, err := cycle.Run(context.Background(), incrementalexec.CycleConfig{MaxItems: 5, MaxWallTime: 60 * time.Second}); err != nil {
			t.Fatal(err)
		}
	}
	if len(trace.order) < 2 || trace.order[1] != "/fresh" {
		t.Fatal("F1 delayed by dedupe", trace.order)
	}
	f2AssertQ5Q8(t, f, "/library/fresh/nested/new.txt")
}

func TestIssue134MirrorDueProgressUnderContinuousRoot(t *testing.T) {
	f := f2Setup(t, 2*time.Minute)
	dirs := []map[string]any{}
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("old-%02d", i)
		dirs = append(dirs, p4Dir(name))
		f.mock.set("/library/" + name)
	}
	f.mock.set("/library", dirs...)
	f.parent(t, "/")
	f.drain(t, 11)
	for i := 0; i < 10; i++ {
		f.mock.set(fmt.Sprintf("/library/old-%02d", i), p4File("late.txt", 1, fmt.Sprintf("fair-%d", i)))
	}
	f.offset = 121 * time.Second
	f.rebuild(t, true)
	cycle, _ := incrementalexec.NewCycleRunner(f.ex)
	orch, _ := incrementalorch.NewRunner(f.st, cycle, f.now)
	seconds := int64(1)
	due := f.now()
	if _, err := f.st.CreateWatch(context.Background(), state.ScopeWatchState{RootID: f.root, ScopeKey: "/", WatchState: state.WatchHot, CadenceClass: "TEST_ROOT_HOT", SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy}, Priority: state.PriorityHigh, EffectiveIntervalSeconds: &seconds, NextDueAt: &due}); err != nil {
		t.Fatal(err)
	}
	before := f.mock.refreshCount()
	for round := 0; round < 3; round++ {
		r, err := orch.RunCycle(context.Background(), incrementalorch.Config{MirrorRevisitEnabled: true, MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: 60 * time.Second})
		if err != nil {
			t.Fatal(r, err)
		}
		f.offset += time.Second
	}
	for i := 0; i < 10; i++ {
		f2AssertQ5Q8(t, f, fmt.Sprintf("/library/old-%02d/late.txt", i))
	}
	if n := f.mock.refreshCount() - before; n != 13 {
		t.Fatal("unexpected duplicate source scans under continuous root", n)
	}
}

func TestIssue134MirrorCompositionAndNonMirrorFallback(t *testing.T) {
	cfg := p4Config()
	cfg.MirrorRevisitInterval = time.Minute
	if _, err := incrementalexec.New(&fakeStore{}, &countingScanner{}, cfg, nil); err == nil {
		t.Fatal("missing atomic policy port accepted")
	}
	st, mock, root := p4SetupRootPath(t, "/library")
	mock.set("/library", p4Dir("child"))
	mock.set("/library/child")
	cfg.DiscoveryScope = "/"
	ex, err := incrementalexec.New(st, p4RealScanner(st), cfg, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	p4SeedPending(t, st, context.Background(), root, "/", time.Now().UTC(), nil)
	r, err := ex.ExecuteOne(context.Background())
	if err != nil || r.Discovery.WatchEnrolled != 0 {
		t.Fatal(r, err)
	}
	var watches int
	if err := st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM index_scope_watch_state`).Scan(&watches); err != nil || watches != 0 {
		t.Fatal("ordinary collector got mirror watchers", watches, err)
	}
	if _, err := ex.ExecuteOne(context.Background()); err != nil {
		t.Fatal("legacy discovery lost child", err)
	}
	f2AssertQ5Q8(t, &f2Fixture{st: st, root: root}, "/library/child")
}
