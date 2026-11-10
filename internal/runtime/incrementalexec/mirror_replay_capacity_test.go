package incrementalexec_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/sourcebudget"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Bounded F2-only capacity rejection experiment. SQL-seeded 20K inventory is
// explicitly not a full traversal. No target relaxation or production scheduler.
func TestIssue134MirrorReplay20K(t *testing.T) {
	if os.Getenv("INDEXCORE_ISSUE134_REPLAY_20K") != "1" {
		t.Skip("opt-in owned PG18 F2-only20K capacity evidence")
	}
	testutil.Issue134Pool(t)
	f := f2Setup(t, 2*time.Minute)
	f3Seed(t, f, 20000)
	h := f3AttachHTTP(t, f, 1250*time.Millisecond)
	h.qualified.Store(true)
	ctx := context.Background()
	lock, e := f.st.AcquireWriterLock(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Release(ctx)
	dir := os.Getenv("INDEXCORE_REPLAY_EVIDENCE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	out, e := os.OpenFile(filepath.Join(dir, "f2-20k.ndjson"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer out.Close()
	var mu sync.Mutex
	var writeErr error
	calls := 0
	write := func(v map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		v["at"] = time.Now().UTC()
		if e := json.NewEncoder(out).Encode(v); e != nil && writeErr == nil {
			writeErr = e
		}
	}
	a, e := f.st.GetAdapterConfig(ctx, f.root)
	if e != nil {
		t.Fatal(e)
	}
	var raw map[string]any
	if e = json.Unmarshal(a.Config, &raw); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(raw["base_url"].(string))
	if e != nil {
		t.Fatal(e)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ModifyResponse = func(r *http.Response) error {
		write(map[string]any{"kind": "http_response", "status": r.StatusCode, "endpoint": r.Request.URL.Path})
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		write(map[string]any{"kind": "http_start", "endpoint": r.URL.Path})
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	raw["base_url"] = server.URL
	a.Config, e = json.Marshal(raw)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.st.UpsertAdapterConfig(ctx, f.root, a); e != nil {
		t.Fatal(e)
	}
	b := sourcebudget.Defaults()
	b.Global = 200 * time.Millisecond
	b.Root = b.Global
	b.Provider = b.Global
	budget, e := sourcebudget.New(b)
	if e != nil {
		t.Fatal(e)
	}
	scanner := scan.New(f.st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil))).WithSourceBudget(budget)
	f.offset = 121 * time.Second
	cfg := p4Config()
	cfg.DiscoveryScope = ""
	cfg.FirstDiscoveryEnabled = false
	cfg.MirrorRevisitInterval = 2 * time.Minute
	ex, e := incrementalexec.New(f.st, scan.NewClaimedScanner(scanner), cfg, f.now)
	if e != nil {
		t.Fatal(e)
	}
	cycle, e := incrementalexec.NewCycleRunner(ex)
	if e != nil {
		t.Fatal(e)
	}
	orch, e := incrementalorch.NewRunner(f.st, cycle, f.now)
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	var initialDebt, lastDebt float64
	var firstCount, lastCount int
	sample := func(round int) {
		var count, due, pending, locks int
		var debt float64
		var bytes, temp int64
		if e = f.st.Pool().QueryRow(ctx, `SELECT count(*),count(*)FILTER(WHERE next_due_at<=$1),coalesce(sum(greatest(0,extract(epoch FROM($1-next_due_at)))),0) FROM index_scope_watch_state`, f.now()).Scan(&count, &due, &debt); e != nil {
			t.Fatal(e)
		}
		if e = f.st.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM index_dirty_scope_work WHERE work_state='PENDING'),(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'),pg_database_size(current_database()),temp_bytes FROM pg_stat_database WHERE datname=current_database()`).Scan(&pending, &locks, &bytes, &temp); e != nil {
			t.Fatal(e)
		}
		if round == 0 {
			initialDebt = debt
			firstCount = count
		}
		lastDebt = debt
		lastCount = count
		write(map[string]any{"kind": "sample", "round": round, "elapsed_seconds": time.Since(start).Seconds(), "watches": count, "due_watches": due, "freshness_debt_overdue_seconds": debt, "pending": pending, "lock_waiters": locks, "db_bytes": bytes, "temp_bytes": temp})
	}
	write(map[string]any{"kind": "workload", "inventory_seeded": 20000, "f1": false, "f2_interval_seconds": 120, "spacing_ms": 200, "reader": 1, "logical_due_gate_offset_seconds": 121, "real_duration_min_seconds": 125, "fake_list_delay_ms": 1250})
	sample(0)
	round := 0
	for time.Since(start) < 125*time.Second {
		res, e := orch.RunCycle(ctx, incrementalorch.Config{MirrorRevisitEnabled: true, MaxDueWatchAttempts: 5, MaxExecuteItems: 5, MaxWallTime: 60 * time.Second})
		if e != nil {
			t.Fatal(e)
		}
		round++
		write(map[string]any{"kind": "cycle", "round": round, "due_emitted": res.DueEmitted, "succeeded": res.Executor.Succeeded})
		sample(round)
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if h.calls.Load() == 0 || calls < int(h.calls.Load()) || lastCount < firstCount {
		t.Fatal("invalid/no-source capacity evidence")
	}
	verdict := "NOT_VERIFIED"
	if lastDebt > initialDebt {
		verdict = "NO-GO"
	}
	result := map[string]any{"kind": "summary", "verdict": verdict, "f1": false, "actual_lists": h.calls.Load(), "actual_http_requests": calls, "wall_seconds": time.Since(start).Seconds(), "initial_watches": firstCount, "final_watches": lastCount, "retired": 0, "automatic_retirement": "ABSENT", "initial_debt_seconds": initialDebt, "final_debt_seconds": lastDebt, "full_traversal": false}
	write(result)
	bytes, _ := json.Marshal(result)
	t.Log("ISSUE134_REPLAY_20K " + string(bytes))
}
