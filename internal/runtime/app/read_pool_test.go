package app

import (
	"context"
	"encoding/json"
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
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

type poolHTTP struct {
	calls, active, peak, code, delay atomic.Int64
	api, duplicates                  atomic.Int64
	seen                             sync.Map
	visible                          atomic.Bool
	bad                              string
	block                            bool
	http429                          bool
	started                          chan struct{}
	once                             sync.Once
}

func (h *poolHTTP) serve(w http.ResponseWriter, r *http.Request) {
	h.api.Add(1)
	switch r.URL.Path {
	case "/api/me":
		io.WriteString(w, `{"code":200,"data":{"base_path":"/drive/library"}}`)
		return
	case "/api/admin/storage/list":
		io.WriteString(w, `{"code":200,"data":{"total":1,"content":[{"id":1,"mount_path":"/drive","driver":"115 Open","status":"work","disabled":false}]}}`)
		return
	case "/api/fs/list":
	default:
		http.NotFound(w, r)
		return
	}
	var req struct {
		Path    string
		Refresh bool
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || !req.Refresh {
		http.Error(w, "bad fixture request", 400)
		return
	}
	if _, loaded := h.seen.LoadOrStore(req.Path, true); loaded {
		h.duplicates.Add(1)
	}
	n := h.active.Add(1)
	defer h.active.Add(-1)
	for old := h.peak.Load(); n > old; old = h.peak.Load() {
		if h.peak.CompareAndSwap(old, n) {
			break
		}
	}
	h.calls.Add(1)
	if h.block {
		h.once.Do(func() { close(h.started) })
		<-r.Context().Done()
		return
	}
	if d := h.delay.Load(); d > 0 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Duration(d)):
		}
	}
	if h.bad == req.Path && h.code.Load() != 0 {
		if h.http429 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": h.code.Load()})
		return
	}
	var id int
	if _, e := fmt.Sscanf(req.Path, "/d%d", &id); e != nil {
		http.Error(w, "unexpected scope", 400)
		return
	}
	entries := []map[string]any{}
	if h.visible.Load() {
		entries = append(entries, map[string]any{"name": "late.bin", "size": 100000 + id, "is_dir": false, "modified": "2026-01-02T03:04:05Z", "hash_info": map[string]string{"sha1": fmt.Sprintf("unique-%d", id)}})
	}
	json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": entries, "total": len(entries)}})
}
func poolFixture(t *testing.T, roots, scopes int) (*postgres.Store, *poolHTTP, *httptest.Server, []string) {
	t.Helper()
	pool := testutil.Issue138Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if e := postgres.Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	st := postgres.New(pool)
	h := &poolHTTP{started: make(chan struct{})}
	h.visible.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(srv.Close)
	t.Setenv("ISSUE138_POOL_TOPOLOGY", "owned-fixture-only")
	ids := []string{}
	for i := 0; i < roots; i++ {
		root := fmt.Sprintf("b1380000-0000-4000-8000-%012d", i+1)
		ids = append(ids, root)
		if e := st.CreateRoot(ctx, pool, root, []byte(`{}`), domain.RootActive); e != nil {
			t.Fatal(e)
		}
		if e := st.UpsertRootPolicy(ctx, root, postgres.RootPolicy{RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour, MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}); e != nil {
			t.Fatal(e)
		}
		cfg, _ := json.Marshal(map[string]any{"base_url": srv.URL, "path": "/", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_token_env": "ISSUE138_POOL_TOPOLOGY"})
		if e := st.UpsertAdapterConfig(ctx, root, postgres.AdapterConfig{CollectorKind: "openlist", Config: cfg}); e != nil {
			t.Fatal(e)
		}
		for j := 0; j < scopes; j++ {
			scope := fmt.Sprintf("/d%06d", j)
			resource := fmt.Sprintf("b138%04d-0000-4000-8000-%012d", i+1, j+1)
			if _, e := pool.Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,name,is_dir,size,mtime) VALUES($1,$2,0,0,'PRESENT','NONE',$3,$4,true,0,'2026-01-01T00:00:00Z')`, resource, root, scope, strings.TrimPrefix(scope, "/")); e != nil {
				t.Fatal(e)
			}
			if _, e := st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: scope, Source: state.SourceFullVerifyBackstop, Reason: state.ReasonDriftVerify, Priority: state.PriorityNormal, SeenAt: time.Now().Add(-time.Hour)}); e != nil {
				t.Fatal(e)
			}
		}
	}
	return st, h, srv, ids
}
func poolConfig(n int) config.Config {
	c := config.Defaults()
	c.IncrementalRuntimeEnabled = true
	c.IncrementalReadWorkers = n
	c.ScanTimeout = 10 * time.Second
	c.SourceGlobalInterval = time.Millisecond
	c.SourceRootInterval = time.Millisecond
	c.SourceProviderInterval = time.Millisecond
	return c
}
func poolLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestIssue138PoolAppSameRootOrder(t *testing.T) {
	for _, n := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			st, h, _, roots := poolFixture(t, 1, 8)
			h.delay.Store(int64(1250 * time.Millisecond))
			cfg := poolConfig(n)
			cycle, e := buildIncrementalCycle(st, cfg, poolLogger())
			if e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 3; i++ {
				_, e = cycle.RunCycle(context.Background(), runtimeConfig(cfg).Cycle)
				if e != nil {
					t.Fatal(e)
				}
			}
			var verified int
			if e := st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM index_dirty_scope_work WHERE work_state='VERIFIED'`).Scan(&verified); e != nil {
				t.Fatal(e)
			}
			if verified != 8 || h.calls.Load() != 8 || h.active.Load() != 0 || h.peak.Load() > int64(n) || (n > 1 && h.peak.Load() < 2) {
				t.Fatal("not real bounded same-root execution", verified, h.calls.Load(), h.peak.Load())
			}
			qr := postgres.NewQueryReader(st.Pool())
			events, e := qr.ReadJournal(context.Background(), roots[0], 0, 100)
			if e != nil || len(events) != 8 {
				t.Fatal("Q8", len(events), e)
			}
			for i, ev := range events {
				if ev.GenerationNumber != int64(i+1) || ev.EventType != domain.EventResourceAdded || ev.ResourceID == nil {
					t.Fatal("Q8 order", ev)
				}
				var path string
				if e := st.Pool().QueryRow(context.Background(), `SELECT canonical_path FROM index_canonical_resource WHERE resource_id=$1`, *ev.ResourceID).Scan(&path); e != nil {
					t.Fatal(e)
				}
				q, e := qr.ResolvePath(context.Background(), roots[0], path, query.ReadOptions{})
				if e != nil || len(q.Matches) != 1 || q.Matches[0].ResourceID != *ev.ResourceID {
					t.Fatal("Q5 identity", q, e)
				}
			}
		})
	}
}
func TestIssue138PoolPermanentFailureDoesNotBlockPeers(t *testing.T) {
	st, h, _, roots := poolFixture(t, 1, 8)
	h.bad = "/d000003"
	h.code.Store(403)
	cfg := poolConfig(4)
	cycle, e := buildIncrementalCycle(st, cfg, poolLogger())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = cycle.RunCycle(context.Background(), runtimeConfig(cfg).Cycle); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 8; i++ {
		w, e := st.GetWork(context.Background(), roots[0], fmt.Sprintf("/d%06d", i))
		if e != nil {
			t.Fatal(e)
		}
		if i == 3 {
			if w.WorkState != state.WorkBlocked || w.LastErrorClass == nil || *w.LastErrorClass != state.ErrorAuthOrPermission {
				t.Fatal("failed slot class", w)
			}
		} else if w.WorkState != state.WorkRetryWait || w.LastErrorClass == nil || *w.LastErrorClass != state.ErrorTransientProvider {
			t.Fatal("innocent peer misclassified", w)
		}
	}
	var drafts int
	st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM index_snapshot`).Scan(&drafts)
	if drafts != 0 {
		t.Fatal("partial batch persisted", drafts)
	}
}
func TestIssue138PoolFairRootAndCanceledRecovery(t *testing.T) {
	st, h, _, roots := poolFixture(t, 2, 8)
	h.block = true
	cfg := poolConfig(4)
	cycle, e := buildIncrementalCycle(st, cfg, poolLogger())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var interrupted []string
	go func() {
		defer close(done)
		r, _ := cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle)
		interrupted = r.Executor.InterruptedRoots
	}()
	select {
	case <-h.started:
	case <-time.After(3 * time.Second):
		t.Fatal("source not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("readers not joined")
	}
	if len(interrupted) != 2 {
		t.Fatal("lost interrupted/fair root", interrupted)
	}
	rt, e := incrementalruntime.New(st, cycle, runtimeConfig(cfg), poolLogger(), nil)
	if e != nil {
		t.Fatal(e)
	}
	n, e := rt.RecoverStartupInflight(context.Background())
	if e != nil || n != 8 {
		t.Fatal("startup pair-root recovery", n, e)
	}
	for _, root := range roots {
		var count int
		st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM index_dirty_scope_work WHERE root_id=$1 AND work_state='IN_FLIGHT'`, root).Scan(&count)
		if count != 0 {
			t.Fatal("stranded claim", root, count)
		}
	}
	if h.calls.Load() != 1 {
		t.Fatal("canceled pool issued extra request", h.calls.Load())
	}
}
func TestIssue138PoolSerialFallbackDoesNotRereadFailedCohort(t *testing.T) {
	st, h, _, roots := poolFixture(t, 1, 16)
	h.bad = "/d000003"
	h.code.Store(403)
	cfg := poolConfig(4)
	cycle, e := buildIncrementalCycle(st, cfg, poolLogger())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = cycle.RunCycle(context.Background(), runtimeConfig(cfg).Cycle); e != nil {
		t.Fatal(e)
	}
	before := h.calls.Load()
	h.peak.Store(0)
	h.delay.Store(int64(50 * time.Millisecond))
	if _, e = cycle.RunCycle(context.Background(), runtimeConfig(cfg).Cycle); e != nil {
		t.Fatal(e)
	}
	if h.calls.Load() != before+8 || h.peak.Load() != 1 {
		t.Fatal("fallback did not execute exactly the new8 scopes serially", before, h.calls.Load(), h.peak.Load())
	}
	for i := 0; i < 16; i++ {
		w, e := st.GetWork(context.Background(), roots[0], fmt.Sprintf("/d%06d", i))
		if e != nil {
			t.Fatal(e)
		}
		if i >= 8 && w.WorkState != state.WorkVerified {
			t.Fatal("serial fallback did not verify new scope", w)
		}
		if i < 8 && w.AttemptCount != 1 {
			t.Fatal("failed/uncertain cohort reread", w)
		}
	}
}
func TestIssue138PoolCooldownSkipsBeforeClaimAndKeepsOtherRootLive(t *testing.T) {
	for _, n := range []int{1, 4} {
		for _, http429 := range []bool{false, true} {
			t.Run(fmt.Sprintf("N=%d/http=%v", n, http429), func(t *testing.T) {
				st, h, _, roots := poolFixture(t, 2, 16)
				h.bad = "/d000000"
				h.code.Store(429)
				h.http429 = http429
				healthy := &poolHTTP{started: make(chan struct{})}
				healthy.visible.Store(true)
				srvB := httptest.NewServer(http.HandlerFunc(healthy.serve))
				defer srvB.Close()
				adapter, e := st.GetAdapterConfig(context.Background(), roots[1])
				if e != nil {
					t.Fatal(e)
				}
				var ac map[string]any
				json.Unmarshal(adapter.Config, &ac)
				ac["base_url"] = srvB.URL
				adapter.Config, _ = json.Marshal(ac)
				if e = st.UpsertAdapterConfig(context.Background(), roots[1], adapter); e != nil {
					t.Fatal(e)
				}
				cfg := poolConfig(n)
				cycle, e := buildIncrementalCycle(st, cfg, poolLogger())
				if e != nil {
					t.Fatal(e)
				}
				start := time.Now()
				if _, e = cycle.RunCycle(context.Background(), runtimeConfig(cfg).Cycle); e != nil {
					t.Fatal(e)
				}
				var attempts int64
				if e = st.Pool().QueryRow(context.Background(), `SELECT sum(attempt_count) FROM index_dirty_scope_work WHERE root_id=$1`, roots[0]).Scan(&attempts); e != nil {
					t.Fatal(e)
				}
				calls := h.calls.Load()
				if _, e = cycle.RunCycle(context.Background(), runtimeConfig(cfg).Cycle); e != nil {
					t.Fatal(e)
				}
				var after int64
				if e = st.Pool().QueryRow(context.Background(), `SELECT sum(attempt_count) FROM index_dirty_scope_work WHERE root_id=$1`, roots[0]).Scan(&after); e != nil {
					t.Fatal(e)
				}
				if attempts != after || calls != h.calls.Load() || healthy.calls.Load() == 0 {
					t.Fatal("cooldown wasted attempts or blocked healthy Root", attempts, after, calls, h.calls.Load(), healthy.calls.Load())
				}
				var healthyVerified int
				if e = st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM index_dirty_scope_work WHERE root_id=$1 AND work_state='VERIFIED'`, roots[1]).Scan(&healthyVerified); e != nil || healthyVerified == 0 {
					t.Fatal("healthy Root read did not produce a verified completion", healthyVerified, e)
				}
				if http429 {
					var earliest time.Time
					if e = st.Pool().QueryRow(context.Background(), `SELECT min(pending_not_before) FROM index_dirty_scope_work WHERE root_id=$1 AND work_state='RETRY_WAIT'`, roots[0]).Scan(&earliest); e != nil {
						t.Fatal(e)
					}
					if earliest.Before(start.Add(120 * time.Second)) {
						t.Fatal("durable retry ignored Retry-After", earliest)
					}
				}
			})
		}
	}
}
