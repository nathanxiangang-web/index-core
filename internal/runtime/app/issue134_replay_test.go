package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Verification-only replay. Production composition, wall clock, existing CAS,
// one writer and real loopback HTTP; no alternate scheduler or provider access.
type replayTrace struct {
	mu                              sync.Mutex
	f                               *os.File
	err                             error
	start                           time.Time
	active, peak, requests, repeats int
	seen                            map[string]int
	claims                          map[string]time.Time
	discovered                      map[string]time.Time
	visible                         map[string]time.Time
	normal                          []float64
	starts                          []time.Time
	statuses                        map[int]int
}

func (r *replayTrace) write(v map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v["at"] = time.Now().UTC()
	if e := json.NewEncoder(r.f).Encode(v); e != nil && r.err == nil {
		r.err = e
	}
}

func replayQuantiles(v []float64) map[string]any {
	if len(v) == 0 {
		return map[string]any{"n": 0, "status": "NOT_TRACKED"}
	}
	v = append([]float64(nil), v...)
	sort.Float64s(v)
	q := func(p float64) float64 { return v[int(math.Ceil(p*float64(len(v))))-1] }
	return map[string]any{"n": len(v), "p50_seconds": q(.5), "p95_seconds": q(.95), "p99_seconds": q(.99), "max_seconds": v[len(v)-1]}
}

func (r *replayTrace) handler(t *testing.T, st *postgres.Store, root string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		began := time.Now()
		r.mu.Lock()
		experimentStart := r.start
		r.active++
		r.requests++
		r.starts = append(r.starts, began)
		if r.active > r.peak {
			r.peak = r.active
		}
		active := r.active
		r.mu.Unlock()
		status := 200
		defer func() {
			r.write(map[string]any{"kind": "http", "start": began.UTC(), "elapsed_ms": float64(time.Since(began)) / float64(time.Millisecond), "endpoint": req.URL.Path, "status": status, "active_at_start": active})
			r.mu.Lock()
			r.statuses[status]++
			r.active--
			r.mu.Unlock()
		}()
		w.Header().Set("Content-Type", "application/json")
		encode := func(data any) {
			if e := json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": data}); e != nil {
				t.Errorf("fixture response: %v", e)
			}
		}
		switch req.URL.Path {
		case "/api/me":
			encode(map[string]any{"base_path": "/drive/library"})
			return
		case "/api/admin/storage/list":
			encode(map[string]any{"total": 1, "content": []map[string]any{{"id": 1, "mount_path": "/drive", "driver": "115 Open", "status": "work", "disabled": false}}})
			return
		case "/api/fs/list":
		default:
			status = 404
			http.NotFound(w, req)
			return
		}
		var body struct {
			Path    string
			Refresh bool
		}
		if e := json.NewDecoder(req.Body).Decode(&body); e != nil || !body.Refresh {
			status = 400
			http.Error(w, "invalid replay request", status)
			return
		}
		wk, e := st.GetWork(req.Context(), root, body.Path)
		if e != nil || wk.LastAttemptStartedAt == nil || wk.ClaimedFirstSeenAt == nil || wk.WorkState != state.WorkInFlight {
			t.Errorf("HTTP without durable claim: %s %v", body.Path, e)
			status = 500
			http.Error(w, "missing claim", status)
			return
		}
		r.mu.Lock()
		if r.seen[body.Path] > 0 {
			r.repeats++
		}
		r.seen[body.Path]++
		if _, ok := r.claims[body.Path]; !ok {
			r.claims[body.Path] = *wk.LastAttemptStartedAt
		}
		if strings.HasPrefix(body.Path, "/d") {
			r.normal = append(r.normal, wk.LastAttemptStartedAt.Sub(*wk.ClaimedFirstSeenAt).Seconds())
		}
		r.mu.Unlock()
		r.write(map[string]any{"kind": "claim", "scope": body.Path, "claimed_at": wk.LastAttemptStartedAt, "first_seen_at": wk.ClaimedFirstSeenAt, "signal_seq": wk.ClaimedSignalSeq, "attempt": wk.AttemptCount})
		entry := func(name string, dir bool) map[string]any {
			return map[string]any{"name": name, "is_dir": dir, "size": 0, "modified": "2026-01-01T00:00:00Z"}
		}
		entries := []map[string]any{}
		switch {
		case body.Path == "/":
			for i := 0; i < 401; i++ {
				entries = append(entries, entry(fmt.Sprintf("d%06d", i), true))
			}
			if began.Sub(experimentStart) >= 5*time.Second {
				for i := 0; i < 12; i++ {
					scope := fmt.Sprintf("/fresh%02d", i)
					entries = append(entries, entry(strings.TrimPrefix(scope, "/"), true))
					r.mu.Lock()
					if _, ok := r.discovered[scope]; !ok {
						r.discovered[scope] = began
						r.mu.Unlock()
						r.write(map[string]any{"kind": "discovered", "scope": scope, "observed_at": began.UTC()})
					} else {
						r.mu.Unlock()
					}
				}
			}
		case strings.HasPrefix(body.Path, "/fresh"):
			if strings.HasSuffix(body.Path, "/nested") {
				entries = append(entries, entry("deep.bin", false))
			} else {
				entries = append(entries, entry("new.bin", false), entry("nested", true))
			}
		case body.Path == "/d000000" && began.Sub(experimentStart) >= 90*time.Second:
			entries = append(entries, entry("late.bin", false))
		case strings.HasPrefix(body.Path, "/d"):
		default:
			status = 400
			http.Error(w, "unexpected replay scope", status)
			return
		}
		encode(map[string]any{"total": len(entries), "content": entries})
	}
}

func TestIssue134SchedulingReplay(t *testing.T) {
	mode := os.Getenv("INDEXCORE_ISSUE134_REPLAY")
	if mode == "" {
		t.Skip("opt-in owned PG18 wall-clock replay: baseline/f1/f2")
	}
	if mode != "baseline" && mode != "f1" && mode != "f2" {
		t.Fatal("replay mode must be baseline/f1/f2")
	}
	st, _, _, roots := poolFixture(t, 1, 401)
	root := roots[0]
	ctx := context.Background()
	// Operational fixture history only: known directories were already verified.
	for i := 0; i < 401; i++ {
		scope := fmt.Sprintf("/d%06d", i)
		wk, e := st.GetWork(ctx, root, scope)
		if e != nil {
			t.Fatal(e)
		}
		c, e := st.ClaimWork(ctx, root, scope, wk.Version, time.Now())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = st.CompleteSuccess(ctx, root, scope, *c.ClaimedSignalSeq, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	dir := os.Getenv("INDEXCORE_REPLAY_EVIDENCE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	f, e := os.OpenFile(filepath.Join(dir, mode+".ndjson"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	r := &replayTrace{f: f, seen: map[string]int{}, claims: map[string]time.Time{}, discovered: map[string]time.Time{}, visible: map[string]time.Time{}, statuses: map[int]int{}}
	t.Cleanup(func() {
		if e := f.Close(); e != nil {
			t.Error(e)
		}
	})
	srv := httptest.NewServer(r.handler(t, st, root))
	t.Cleanup(srv.Close)
	a, e := st.GetAdapterConfig(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	var ac map[string]any
	if e = json.Unmarshal(a.Config, &ac); e != nil {
		t.Fatal(e)
	}
	ac["base_url"] = srv.URL
	a.Config, e = json.Marshal(ac)
	if e != nil {
		t.Fatal(e)
	}
	if e = st.UpsertAdapterConfig(ctx, root, a); e != nil {
		t.Fatal(e)
	}
	lock, e := st.AcquireWriterLock(ctx)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { lock.Release(context.Background()) })
	queuedAt := time.Now()
	for i := 0; i < 401; i++ {
		if _, e = st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: fmt.Sprintf("/d%06d", i), Source: state.SourceRecovery, Reason: state.ReasonPossibleChange, Priority: state.PriorityNormal, SeenAt: queuedAt}); e != nil {
			t.Fatal(e)
		}
	}
	seconds := int64(30)
	due := time.Now().Add(6 * time.Second)
	if _, e = st.CreateWatch(ctx, state.ScopeWatchState{RootID: root, ScopeKey: "/", WatchState: state.WatchHot, CadenceClass: "SOURCE_MIRROR", EffectiveIntervalSeconds: &seconds, SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy}, Priority: state.PriorityHigh, NextDueAt: &due}); e != nil {
		t.Fatal(e)
	}
	cfg := poolConfig(1)
	cfg.SourceGlobalInterval = 200 * time.Millisecond
	cfg.SourceRootInterval = cfg.SourceGlobalInterval
	cfg.SourceProviderInterval = cfg.SourceGlobalInterval
	cfg.IncrementalWakeInterval = 5 * time.Second
	cfg.IncrementalFirstDiscovery = mode == "f1"
	if mode == "f2" {
		cfg.IncrementalMirrorRevisit = 2 * time.Minute
	}
	cycle, e := buildIncrementalCycle(st, cfg, poolLogger())
	if e != nil {
		t.Fatal(e)
	}
	rt, e := incrementalruntime.New(st, cycle, runtimeConfig(cfg), poolLogger(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if n, e := rt.RecoverStartupInflight(ctx); e != nil || n != 0 {
		t.Fatal("startup", n, e)
	}
	// Begin after all DB/configuration preparation. Otherwise a slow fixture can
	// release the +5s source change before Runtime.Run has started scanning.
	r.mu.Lock()
	r.start = time.Now()
	r.mu.Unlock()
	r.write(map[string]any{"kind": "workload", "mode": mode, "backlog": 401, "readers": 1, "spacing_ms": 200, "start": r.start, "new_directories_at": r.start.Add(5 * time.Second), "late_file_at": r.start.Add(90 * time.Second), "duration_seconds": 480, "seed_history": "401 API-only operational verifications; canonical directory fixtures at generation0"})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- rt.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(15 * time.Second):
			t.Error("runtime did not drain")
		}
	})
	qr := postgres.NewQueryReader(st.Pool())
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	seq := int64(0)
	lastGen := int64(0)
	deadline := r.start.Add(480 * time.Second)
	observeJournal := func() {
		events, e := qr.ReadJournal(ctx, root, seq, 1000)
		if e != nil {
			t.Fatal(e)
		}
		for _, ev := range events {
			if ev.EventSeq != seq+1 || ev.GenerationNumber < lastGen || ev.ResourceID == nil || ev.EventType != domain.EventResourceAdded {
				t.Fatal("Q8 ordering/absence", ev)
			}
			var path, snapshot string
			var generation int64
			if e = st.Pool().QueryRow(ctx, `SELECT c.canonical_path,g.produced_by_snapshot_id::text,c.introduced_at_generation FROM index_canonical_resource c JOIN index_generation g ON g.root_id=c.root_id AND g.generation_number=$2 WHERE c.resource_id=$1`, *ev.ResourceID, ev.GenerationNumber).Scan(&path, &snapshot, &generation); e != nil || generation != ev.GenerationNumber {
				t.Fatal("generation/Snapshot lineage", e)
			}
			resolved, e := qr.ResolvePath(ctx, root, path, query.ReadOptions{})
			if e != nil || resolved.Ambiguous || len(resolved.Matches) != 1 || resolved.Matches[0].ResourceID != *ev.ResourceID {
				t.Fatal("Q5/Q8 identity", path, e)
			}
			r.mu.Lock()
			r.visible[path] = time.Now()
			r.mu.Unlock()
			r.write(map[string]any{"kind": "visible", "path": path, "seq": ev.EventSeq, "generation": ev.GenerationNumber, "snapshot": snapshot, "resource_id": *ev.ResourceID})
			seq = ev.EventSeq
			lastGen = ev.GenerationNumber
		}
	}
	for time.Now().Before(deadline) {
		select {
		case <-ticker.C:
		case e := <-done:
			done <- e
			t.Fatal("runtime exited", e)
		}
		observeJournal()
		var watches, dueCount, pending, locks int
		var debt float64
		var dbBytes, reads, hits int64
		if e = st.Pool().QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE next_due_at<=now()),coalesce(sum(greatest(0,extract(epoch FROM(now()-next_due_at)))),0) FROM index_scope_watch_state`).Scan(&watches, &dueCount, &debt); e != nil {
			t.Fatal(e)
		}
		if e = st.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM index_dirty_scope_work WHERE work_state='PENDING'),(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'),pg_database_size(current_database()),blks_read,blks_hit FROM pg_stat_database WHERE datname=current_database()`).Scan(&pending, &locks, &dbBytes, &reads, &hits); e != nil {
			t.Fatal(e)
		}
		r.write(map[string]any{"kind": "sample", "elapsed_seconds": time.Since(r.start).Seconds(), "watches": watches, "due_watches": dueCount, "freshness_debt_overdue_seconds": debt, "pending": pending, "lock_waiters": locks, "db_bytes": dbBytes, "blocks_read": reads, "blocks_hit": hits})
	}
	cancel()
	select {
	case e := <-done:
		done <- nil
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("drain timeout")
	}
	// Include any commit between the final polling tick and the joined drain.
	observeJournal()
	srv.Close()
	claims, visible := []float64{}, []float64{}
	r.mu.Lock()
	for i := 0; i < 12; i++ {
		scope := fmt.Sprintf("/fresh%02d", i)
		discovered, ok := r.discovered[scope]
		if !ok {
			continue
		}
		if c, ok := r.claims[scope]; ok {
			claims = append(claims, c.Sub(discovered).Seconds())
		}
		if v, ok := r.visible[scope+"/new.bin"]; ok {
			visible = append(visible, v.Sub(discovered).Seconds())
		}
	}
	minGap := math.Inf(1)
	buckets := map[int64]int{}
	for i, at := range r.starts {
		buckets[int64(at.Sub(r.start)/time.Second)]++
		if i > 0 && at.Sub(r.starts[i-1]).Seconds() < minGap {
			minGap = at.Sub(r.starts[i-1]).Seconds()
		}
	}
	maxStarts := 0
	for _, n := range buckets {
		if n > maxStarts {
			maxStarts = n
		}
	}
	_, late := r.visible["/d000000/late.bin"]
	serverErrors := 0
	for code, n := range r.statuses {
		if code >= 500 && code < 600 {
			serverErrors += n
		}
	}
	summary := map[string]any{"kind": "summary", "mode": mode, "first_discovery_claim": replayQuantiles(claims), "first_discovery_q5q8": replayQuantiles(visible), "normal_claim_wait": replayQuantiles(r.normal), "http_requests": r.requests, "list_executions": 0, "repeat_list_executions": r.repeats, "http_peak": r.peak, "min_http_gap_seconds": minGap, "max_http_starts_in_anchored_second": maxStarts, "late_file_visible": late, "q8_events": seq, "watch_automatic_retirement": "ABSENT", "http_429": r.statuses[429], "http_5xx": serverErrors, "http_timeouts": "NOT_TRACKED", "observer_period_ms": 1000, "elapsed_seconds": time.Since(r.start).Seconds()}
	lists := 0
	for _, n := range r.seen {
		lists += n
	}
	summary["list_executions"] = lists
	traceErr := r.err
	r.mu.Unlock()
	if traceErr != nil {
		t.Fatal(traceErr)
	}
	r.write(summary)
	raw, _ := json.Marshal(summary)
	t.Log("ISSUE134_REPLAY " + string(raw))
	// Arrival gaps include loopback scheduling jitter after budget acquisition.
	// Exact pacing is asserted by sourcebudget's deterministic clock tests;
	// this receiver checks the measured rate envelope without inventing a strict
	// client-reservation timestamp from a server-arrival observation.
	if r.peak > 1 || maxStarts > 6 || len(r.normal) == 0 {
		t.Error("source budget or NORMAL progress violated")
	}
	if len(claims) != 12 || len(visible) != 12 {
		t.Errorf("FIX: fresh set incomplete: claims=%d visible=%d of12", len(claims), len(visible))
	}
	if !late {
		t.Error("FIX: late file did not converge in finite replay")
	}
}
