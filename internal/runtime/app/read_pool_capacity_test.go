package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func TestIssue138PoolRuntimeLatency(t *testing.T) {
	raw := os.Getenv("INDEXCORE_ISSUE138_RUNTIME_FETCHES")
	if raw == "" {
		t.Skip("opt-in owned PG18 actual app/runtime delayed-source proof")
	}
	count, e := strconv.Atoi(raw)
	if e != nil || (count != 16 && count != 400) {
		t.Fatal("actual runtime reads must be16/400")
	}
	baseline := 0.0
	for _, n := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			st, h, _, roots := poolFixture(t, 1, count)
			root := roots[0]
			h.visible.Store(false)
			h.delay.Store(int64(1250 * time.Millisecond))
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
			defer cancel()
			if _, e := st.Pool().Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,name,is_dir,size,mtime)
    SELECT md5($1||':padding:'||g)::uuid,$1::uuid,0,0,'PRESENT','NONE','/padding'||g,'padding'||g,true,0,'2026-01-01T00:00:00Z' FROM generate_series($2::int,1999) g`, root, count); e != nil {
				t.Fatal(e)
			}
			lock, e := st.AcquireWriterLock(ctx)
			if e != nil {
				t.Fatal(e)
			}
			// Register before the actor cleanup below: LIFO cleanup joins the
			// runtime before releasing ownership, including assertion failures.
			t.Cleanup(func() { lock.Release(context.Background()) })
			cfg := poolConfig(n)
			cfg.IncrementalWakeInterval = time.Second
			cycle, e := buildIncrementalCycle(st, cfg, poolLogger())
			if e != nil {
				t.Fatal(e)
			}
			rt, e := incrementalruntime.New(st, cycle, runtimeConfig(cfg), poolLogger(), nil)
			if e != nil {
				t.Fatal(e)
			}
			if recovered, e := rt.RecoverStartupInflight(ctx); e != nil || recovered != 0 {
				t.Fatal("dirty runtime start", recovered, e)
			}
			done := make(chan struct{})
			var runErr error
			start := time.Now()
			h.visible.Store(true)
			go func() { runErr = rt.Run(ctx); close(done) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("runtime did not join")
				}
			})
			qr := postgres.NewQueryReader(st.Pool())
			seen := []float64{}
			seq := int64(0)
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for len(seen) < count {
				select {
				case <-ctx.Done():
					t.Fatal("runtime budget exhausted", len(seen), ctx.Err())
				case <-done:
					t.Fatalf("runtime exited early after%d: %v", len(seen), runErr)
				case <-ticker.C:
				}
				events, e := qr.ReadJournal(ctx, root, seq, 1000)
				if e != nil {
					t.Fatal(e)
				}
				for _, ev := range events {
					if ev.EventSeq != seq+1 || ev.GenerationNumber != seq+1 || ev.EventType != domain.EventResourceAdded || ev.ResourceID == nil {
						t.Fatal("runtime Q8 order/extra removal", ev)
					}
					var path, snapshot string
					var version, base int64
					if e := st.Pool().QueryRow(ctx, `SELECT c.canonical_path,g.produced_by_snapshot_id::text,(s.provenance->'scoped_fetch_fence'->>'version')::bigint,(s.provenance->'scoped_fetch_fence'->>'generation')::bigint FROM index_canonical_resource c JOIN index_generation g ON g.root_id=c.root_id AND g.generation_number=$2 JOIN index_snapshot s ON s.snapshot_id=g.produced_by_snapshot_id WHERE c.resource_id=$1`, *ev.ResourceID, ev.GenerationNumber).Scan(&path, &snapshot, &version, &base); e != nil {
						t.Fatal(e)
					}
					expectedVersion, expectedBase := int64(1), seq
					if n > 1 {
						expectedVersion = 2
						expectedBase = (seq / 8) * 8
					}
					if version != expectedVersion || base != expectedBase {
						t.Fatal("runtime lost original fenced source lineage", version, base, expectedVersion, expectedBase)
					}
					resolved, e := qr.ResolvePath(ctx, root, path, query.ReadOptions{})
					if e != nil || len(resolved.Matches) != 1 || resolved.Matches[0].ResourceID != *ev.ResourceID {
						t.Fatal("runtime Q5", resolved, e)
					}
					var producing string
					if e := st.Pool().QueryRow(ctx, `SELECT snapshot_id::text FROM index_admission WHERE root_id=$1 AND admission_seq=$2`, root, ev.GenerationNumber).Scan(&producing); e != nil || producing != snapshot {
						t.Fatal("producing input order", producing, snapshot, e)
					}
					seen = append(seen, time.Since(start).Seconds())
					seq = ev.EventSeq
				}
			}
			for {
				var verified int
				if e := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_dirty_scope_work WHERE work_state='VERIFIED'`).Scan(&verified); e != nil {
					t.Fatal(e)
				}
				if verified == count {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("work completion timeout")
				case <-time.After(time.Millisecond):
				}
			}
			cancel()
			select {
			case <-done:
				if runErr != nil {
					t.Fatal(runErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancel/drain timeout")
			}
			wall := time.Since(start).Seconds()
			if n == 1 {
				baseline = wall
			}
			if h.calls.Load() != int64(count) || h.api.Load() != int64(5*count) || h.duplicates.Load() != 0 || h.peak.Load() > int64(n) {
				t.Fatal("runtime I/O bounds", h.calls.Load(), h.api.Load(), h.duplicates.Load(), h.peak.Load())
			}
			if n == 4 && baseline/wall <= 1.5 {
				t.Error("NO-GO runtime four-reader speedup", baseline/wall)
			}
			sort.Float64s(seen)
			quant := func(q float64) float64 { return seen[int(math.Ceil(q*float64(len(seen))))-1] }
			receipt := map[string]any{"mode": "app_composed_P10_runtime", "workers": n, "roots": 1, "inventory_seeded": 2000, "actual_forced_lists": h.calls.Load(), "all_http_calls": h.api.Load(), "duplicate_lists": h.duplicates.Load(), "peak_source_inflight": h.peak.Load(), "cycle_item_cap": runtimeConfig(cfg).Cycle.MaxExecuteItems, "source_delay_ms": 1250, "global_spacing_ms": cfg.SourceGlobalInterval.Milliseconds(), "root_spacing_ms": cfg.SourceRootInterval.Milliseconds(), "group_spacing_ms": cfg.SourceProviderInterval.Milliseconds(), "source_release": "atomic_flag_before_runtime_run", "observer_period_ms": 50, "wall_seconds": wall, "speedup_vs1": baseline / wall, "q5q8_p50": quant(.5), "q5q8_p95": quant(.95), "q5q8_p99": quant(.99)}
			bytes, _ := json.Marshal(receipt)
			t.Log("ISSUE138_RUNTIME " + string(bytes))
		})
	}
}
