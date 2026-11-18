package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

type rollingWire struct {
	HTTP              string
	Scope             string
	Started, Finished time.Time
	Status            int
}
type rollingClaim struct {
	Scope         string
	Sequence      int64
	Claimed, Seen time.Time
}
type rollingSample struct {
	At              time.Time
	Stats           postgres.MirrorRollingStats
	ManagedPollWork int
}
type rollingVisibility struct {
	Scope                                              string
	Discovered, Claimed, FileVisible, DirectoryVisible *time.Time
	ResourceID                                         string
}
type rollingReceiver struct {
	mu             sync.Mutex
	start          time.Time
	injected       time.Time
	wire           []rollingWire
	claims         []rollingClaim
	claimSeen      map[string]bool
	inject         atomic.Bool
	oldLists       atomic.Int64
	st             *postgres.Store
	root           string
	groups, leaves int
	encoder        *json.Encoder
	writeError     error
}

func rollingDir(name string) map[string]any {
	return map[string]any{"name": name, "is_dir": true, "size": 0, "modified": "2026-01-01T00:00:00Z"}
}
func rollingFile(name, hash string) map[string]any {
	return map[string]any{"name": name, "is_dir": false, "size": 123, "modified": "2026-01-02T00:00:00Z", "hash_info": map[string]string{"sha1": hash}}
}

func (h *rollingReceiver) serve(w http.ResponseWriter, r *http.Request) {
	e := rollingWire{HTTP: r.URL.Path, Started: time.Now().UTC(), Status: 200}
	defer func() {
		e.Finished = time.Now().UTC()
		h.mu.Lock()
		h.wire = append(h.wire, e)
		if h.encoder != nil {
			if err := h.encoder.Encode(map[string]any{"type": "receiver_http", "event": e}); err != nil {
				h.writeError = err
			}
		}
		h.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/api/me" {
		io.WriteString(w, `{"code":200,"data":{"base_path":"/drive/library"}}`)
		return
	}
	if r.URL.Path == "/api/admin/storage/list" {
		io.WriteString(w, `{"code":200,"data":{"total":1,"content":[{"id":1,"mount_path":"/drive","driver":"115 Open","status":"work","disabled":false}]}}`)
		return
	}
	if r.URL.Path != "/api/fs/list" {
		e.Status = 404
		http.NotFound(w, r)
		return
	}
	var req struct {
		Path    string
		Refresh bool
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || !req.Refresh {
		e.Status = 400
		http.Error(w, "fixture request", 400)
		return
	}
	e.Scope = req.Path
	work, err := h.st.GetWork(r.Context(), h.root, req.Path)
	if err == nil && work.ClaimedSignalSeq != nil && work.LastAttemptStartedAt != nil && work.ClaimedFirstSeenAt != nil {
		key := fmt.Sprintf("%s:%d:%d", req.Path, *work.ClaimedSignalSeq, work.AttemptCount)
		h.mu.Lock()
		if !h.claimSeen[key] {
			h.claimSeen[key] = true
			c := rollingClaim{req.Path, *work.ClaimedSignalSeq, *work.LastAttemptStartedAt, *work.ClaimedFirstSeenAt}
			h.claims = append(h.claims, c)
			if h.encoder != nil {
				if err := h.encoder.Encode(map[string]any{"type": "claim", "event": c}); err != nil {
					h.writeError = err
				}
			}
		}
		h.mu.Unlock()
	}
	if strings.HasPrefix(req.Path, "/d") && h.oldLists.Add(1) == 6 {
		h.mu.Lock()
		h.injected = time.Now().UTC()
		h.mu.Unlock()
		h.inject.Store(true)
	}
	entries := []map[string]any{}
	switch {
	case req.Path == "/":
		for i := 0; i < 401; i++ {
			entries = append(entries, rollingDir(fmt.Sprintf("d%06d", i)))
		}
		for i := 0; i < h.groups; i++ {
			entries = append(entries, rollingDir(fmt.Sprintf("g%04d", i)))
		}
		if h.inject.Load() {
			for i := 0; i < 12; i++ {
				entries = append(entries, rollingDir(fmt.Sprintf("fresh_%02d", i)))
			}
		}
	case strings.HasPrefix(req.Path, "/fresh_"):
		entries = append(entries, rollingFile("arrived.bin", "fresh:"+req.Path))
	case strings.HasPrefix(req.Path, "/g") && !strings.Contains(strings.TrimPrefix(req.Path, "/"), "/"):
		var group int
		fmt.Sscanf(req.Path, "/g%d", &group)
		for i := group * 100; i < (group+1)*100 && i < h.leaves; i++ {
			entries = append(entries, rollingDir(fmt.Sprintf("n%05d", i)))
		}
	case req.Path == "/d000000":
		h.mu.Lock()
		started := h.start
		h.mu.Unlock()
		if time.Since(started) >= 90*time.Second {
			entries = append(entries, rollingFile("late.bin", "late:known-leaf"))
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": len(entries), "content": entries}})
}

// A finite120s collection window, not a successful120s freshness SLA. Both
// policies use the same20K SQL history,401 API-enqueued items, HOT Root, injection
// rule, Reader1 and200ms total HTTP budgets. Initial history is not20K source reads.
func TestIssue134RollingRuntimeEvidence(t *testing.T) {
	if os.Getenv("INDEXCORE_F4_REPLAY") != "1" {
		t.Skip("explicit owned PG18 actual120s replay required")
	}
	mode := os.Getenv("INDEXCORE_F4_MODE")
	if mode != "legacy" && mode != "rolling" {
		t.Fatal("choose legacy or rolling")
	}
	st, _, _, roots := poolFixture(t, 1, 401)
	ctx := context.Background()
	root := roots[0]
	pool := st.Pool()
	const population = 20000
	groups := (population - 401 + 100) / 101
	leaves := population - 401 - groups
	for _, statement := range []string{
		`INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,name,is_dir,size,mtime,updated_at) SELECT md5($1||':group:'||g)::uuid,$1::uuid,0,0,'PRESENT','NONE','/g'||lpad(g::text,4,'0'),'g'||lpad(g::text,4,'0'),true,0,'2026-01-01T00:00:00Z',now()-interval '1 hour' FROM generate_series(0,$2::int-1) g`,
		`INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,parent_resource_id,name,is_dir,size,mtime,updated_at) SELECT md5($1||':leaf:'||g)::uuid,$1::uuid,0,0,'PRESENT','NONE','/g'||lpad((g/100)::text,4,'0')||'/n'||lpad(g::text,5,'0'),md5($1||':group:'||(g/100))::uuid,'n'||lpad(g::text,5,'0'),true,0,'2026-01-01T00:00:00Z',now()-interval '1 hour' FROM generate_series(0,$2::int-1) g`,
	} {
		n := groups
		if strings.Contains(statement, ":leaf:") {
			n = leaves
		}
		if _, err := pool.Exec(ctx, statement, root, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE index_canonical_resource SET updated_at=now()-interval '1 hour' WHERE root_id=$1`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE index_dirty_scope_work SET work_state='VERIFIED',pending_source_set='{}',pending_reason_set='{}',pending_priority=NULL,pending_first_seen_at=NULL,pending_not_before=NULL,attempt_count=1,last_verified_signal_seq=signal_seq,last_verified_at=now()-interval '1 hour' WHERE root_id=$1`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO index_dirty_scope_work(root_id,scope_key,work_state,signal_seq,attempt_count,last_verified_at,last_verified_signal_seq,last_seen_at) SELECT root_id,canonical_path,'VERIFIED',1,1,now()-interval '1 hour',1,now()-interval '1 hour' FROM index_canonical_resource WHERE root_id=$1 ON CONFLICT DO NOTHING`, root); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 401; i++ {
		if _, err := st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: fmt.Sprintf("/d%06d", i), Source: state.SourceRecovery, Reason: state.ReasonPossibleChange, Priority: state.PriorityNormal, SeenAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO index_scope_watch_state(root_id,scope_key,watch_state,cadence_class,effective_interval_seconds,source_set,priority_class,next_due_at,last_success_at) SELECT root_id,canonical_path,'WARM','MIRROR_REVISIT',120,ARRAY['BACKSTOP_ENROLL'],'NORMAL',now(),now()-interval '1 hour' FROM index_canonical_resource WHERE root_id=$1`, root); err != nil {
		t.Fatal(err)
	}
	h := &rollingReceiver{st: st, root: root, groups: groups, leaves: leaves, claimSeen: map[string]bool{}}
	if dir := os.Getenv("INDEXCORE_F4_EVIDENCE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(filepath.Join(dir, "rolling-"+mode+".ndjson"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		h.encoder = json.NewEncoder(f)
	}
	srv := httptest.NewServer(http.HandlerFunc(h.serve))
	defer srv.Close()
	raw, _ := json.Marshal(map[string]any{"base_url": srv.URL, "path": "/", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_token_env": "ISSUE138_POOL_TOPOLOGY"})
	if err := st.UpsertAdapterConfig(ctx, root, postgres.AdapterConfig{CollectorKind: "openlist", Config: raw}); err != nil {
		t.Fatal(err)
	}
	seconds := int64(30)
	due := time.Now().UTC()
	if _, err := st.CreateWatch(ctx, state.ScopeWatchState{RootID: root, ScopeKey: "/", WatchState: state.WatchHot, CadenceClass: "SOURCE_MIRROR", EffectiveIntervalSeconds: &seconds, SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy}, Priority: state.PriorityHigh, NextDueAt: &due}); err != nil {
		t.Fatal(err)
	}
	costs := postgres.NewQueryCosts()
	runtimePool, err := postgres.OpenWithQueryCosts(ctx, testutil.TestDSN(), costs)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimePool.Close()
	runtimeStore := postgres.New(runtimePool)
	lock, err := runtimeStore.AcquireWriterLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release(context.Background())
	cfg := poolConfig(1)
	cfg.ScanTimeout = 30 * time.Second
	cfg.IncrementalMirrorRevisit = 2 * time.Minute
	cfg.SourceGlobalInterval = 200 * time.Millisecond
	cfg.SourceRootInterval = 200 * time.Millisecond
	cfg.SourceProviderInterval = 200 * time.Millisecond
	if mode == "rolling" {
		cfg.MirrorRollingPercent = 25
	}
	cycle, err := buildIncrementalCycle(runtimeStore, cfg, poolLogger())
	if err != nil {
		t.Fatal(err)
	}
	rt, err := incrementalruntime.New(runtimeStore, cycle, runtimeConfig(cfg), poolLogger(), nil)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	h.mu.Lock()
	h.start = time.Now().UTC()
	h.mu.Unlock()
	go func() { done <- rt.Run(runCtx) }()
	// Always join the actor before releasing writer ownership, including failure.
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("runtime did not join")
			}
		}
	}()
	var samples []rollingSample
	visible := map[string]*rollingVisibility{}
	for i := 0; i < 12; i++ {
		scope := fmt.Sprintf("/fresh_%02d", i)
		visible[scope] = &rollingVisibility{Scope: scope}
	}
	qr := postgres.NewQueryReader(pool)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(120 * time.Second)
	defer deadline.Stop()
loop:
	for {
		select {
		case err := <-done:
			joined = true
			t.Fatal("runtime exited before finite window", err)
		case <-deadline.C:
			break loop
		case <-ticker.C:
			now := time.Now().UTC()
			s, e := st.MirrorRollingStats(ctx, now)
			if e != nil {
				t.Fatal(e)
			}
			var polls int
			if e = pool.QueryRow(ctx, `SELECT count(*) FROM index_dirty_scope_work k JOIN index_scope_watch_state w USING(root_id,scope_key) WHERE k.root_id=$1 AND w.cadence_class='MIRROR_REVISIT' AND (k.pending_source_set@>ARRAY['POLL_SCHEDULE'] OR k.claimed_source_set@>ARRAY['POLL_SCHEDULE'])`, root).Scan(&polls); e != nil {
				t.Fatal(e)
			}
			samples = append(samples, rollingSample{now, s, polls})
			if mode == "rolling" && (s.OutstandingRoots > 1 || polls > 1) {
				t.Fatal("unbounded managed materialization", s, polls)
			}
			for scope, v := range visible {
				for _, file := range []bool{false, true} {
					path := scope
					if file {
						path += "/arrived.bin"
					}
					resolved, e := qr.ResolvePath(ctx, root, path, query.ReadOptions{})
					if e != nil {
						t.Fatal(e)
					}
					if len(resolved.Matches) == 1 {
						stamp := time.Now().UTC()
						if file {
							if v.FileVisible == nil {
								events, e := qr.ReadJournal(ctx, root, 0, 1000)
								if e != nil {
									t.Fatal(e)
								}
								found := false
								for _, ev := range events {
									if ev.ResourceID != nil && *ev.ResourceID == resolved.Matches[0].ResourceID {
										found = true
										break
									}
								}
								if !found {
									t.Fatal("visible Q5 file missing from Q8", scope)
								}
								stamp = time.Now().UTC()
								v.FileVisible = &stamp
								v.ResourceID = resolved.Matches[0].ResourceID
							} else if v.ResourceID != resolved.Matches[0].ResourceID {
								t.Fatal("Q5 identity changed", scope)
							}
						} else if v.DirectoryVisible == nil {
							v.DirectoryVisible = &stamp
						}
					}
				}
			}
		}
	}
	cancel()
	select {
	case <-done:
		joined = true
	case <-time.After(10 * time.Second):
		t.Fatal("runtime shutdown timeout")
	}
	srv.Close() // drain receiver callbacks before recording final HTTP/claim totals
	actorStopped := time.Now().UTC()
	var invalid, removed int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE s.snapshot_id IS NULL OR s.lifecycle_state<>'RECONCILED'),count(*) FILTER(WHERE j.event_type='resource-removed') FROM index_journal_event j LEFT JOIN index_generation g USING(root_id,generation_number) LEFT JOIN index_snapshot s ON s.snapshot_id=g.produced_by_snapshot_id WHERE j.root_id=$1`, root).Scan(&invalid, &removed); err != nil || invalid != 0 || removed != 0 {
		t.Fatal("Journal/Generation or false removal", invalid, removed, err)
	}
	journal, err := qr.ReadJournal(ctx, root, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range journal {
		if e.EventSeq != int64(i+1) || (i > 0 && e.GenerationNumber < journal[i-1].GenerationNumber) {
			t.Fatal("Journal ordering", e)
		}
	}
	h.mu.Lock()
	wire := append([]rollingWire(nil), h.wire...)
	claims := append([]rollingClaim(nil), h.claims...)
	injected := h.injected
	h.mu.Unlock()
	if injected.IsZero() {
		t.Fatal("mid-run source injection not reached")
	}
	for _, c := range claims {
		if v := visible[c.Scope]; v != nil && v.Claimed == nil {
			stamp := c.Claimed
			v.Claimed = &stamp
		}
	}
	for scope, v := range visible {
		var observed time.Time
		e := pool.QueryRow(ctx, `SELECT s.observed_at FROM index_canonical_resource c JOIN index_generation g ON g.root_id=c.root_id AND g.generation_number=c.introduced_at_generation JOIN index_snapshot s ON s.snapshot_id=g.produced_by_snapshot_id WHERE c.root_id=$1 AND c.canonical_path=$2 AND c.resource_presence='PRESENT'`, root, scope).Scan(&observed)
		if e == nil {
			v.Discovered = &observed
		} else if !errors.Is(e, pgx.ErrNoRows) {
			t.Fatal(e)
		}
		if v.Claimed == nil {
			if work, e := st.GetWork(ctx, root, scope); e == nil && work.LastAttemptStartedAt != nil {
				v.Claimed = work.LastAttemptStartedAt
			}
		}
	}
	verdict := "NOT_VERIFIED"
	if len(samples) > 1 && samples[len(samples)-1].Stats.FreshnessDebtSeconds > samples[0].Stats.FreshnessDebtSeconds {
		verdict = "NO-GO"
	}
	var totalWatches int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM index_scope_watch_state WHERE root_id=$1`, root).Scan(&totalWatches); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"mode": mode, "duration_seconds": 120, "actual_runtime_seconds": actorStopped.Sub(h.start).Seconds(), "population": population, "initial_pending": 401, "reader": 1, "total_spacing_ms": 200, "background_percent": cfg.MirrorRollingPercent, "runtime_start": h.start, "source_injected": injected, "wire_requests": len(wire), "total_watch_rows": totalWatches, "samples": samples, "visibility": visible, "query_costs": costs.Snapshot(), "capacity_verdict": verdict, "exact_wire_start": "NOT_TRACKED", "client_timeouts": "NOT_TRACKED", "history": "SQL Canonical/verified Watch history; 401 live API enqueues; not20K traversals", "normal_wait": "claim minus persisted API pending epoch; claims reaching list, censored remainder", "q5_visibility": "polling upper bounds with Q8 identity check; producing Snapshot audited", "observer": "receiver claim reads and Q5/Stats polling included in database resource use, excluded from Runtime query tracer"}
	if h.writeError != nil {
		t.Fatal("evidence write failed", h.writeError)
	}
	if h.encoder != nil {
		if err = h.encoder.Encode(map[string]any{"type": "result", "result": result}); err != nil {
			t.Fatal(err)
		}
	}
	encoded, _ := json.Marshal(result)
	t.Log("F4_EVIDENCE " + string(encoded))
}
