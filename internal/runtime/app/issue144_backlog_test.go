package app

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Finite manual P6 safety/fairness check, NOT a P10 throughput/SLA test. Every
// normal-priority scope is read exactly once; fixture-only1ms source spacing is
// deliberately not the production200ms policy. Existing P10 proof stays separate.
func TestIssue144N1Backlog400(t *testing.T) {
	if os.Getenv("INDEXCORE_ISSUE144_BACKLOG") != "400" {
		t.Skip("opt-in owned 400-scope safety proof")
	}
	st, h, _, roots := poolFixture(t, 1, 400)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := poolConfig(1)
	cycle, e := buildIncrementalCycle(st, cfg, poolLogger())
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	var verified int
	for i := 0; i < 100; i++ {
		if _, e = cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle); e != nil {
			t.Fatal(e)
		}
		if e = st.Pool().QueryRow(ctx, `SELECT count(*)FROM index_dirty_scope_work WHERE work_state='VERIFIED'`).Scan(&verified); e != nil {
			t.Fatal(e)
		}
		if verified == 400 {
			break
		}
	}
	if verified != 400 || h.calls.Load() != 400 || h.api.Load() != 2000 || h.duplicates.Load() != 0 || h.peak.Load() != 1 {
		t.Fatal("finite backlog not drained exactly once", verified, h.calls.Load(), h.api.Load(), h.duplicates.Load(), h.peak.Load())
	}
	rows, e := st.Pool().Query(ctx, `SELECT scope_key,attempt_count FROM index_dirty_scope_work ORDER BY last_attempt_started_at,scope_key`)
	if e != nil {
		t.Fatal(e)
	}
	i := 0
	for rows.Next() {
		var scope string
		var attempt int64
		if e = rows.Scan(&scope, &attempt); e != nil {
			t.Fatal(e)
		}
		if scope != fmt.Sprintf("/d%06d", i) || attempt != 1 {
			t.Fatal("older normal work starved/repeated", i, scope, attempt)
		}
		i++
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	var p95 float64
	if e = st.Pool().QueryRow(ctx, `SELECT percentile_cont(.95)WITHIN GROUP(ORDER BY extract(epoch FROM(last_attempt_started_at-$1::timestamptz)))FROM index_dirty_scope_work`, start).Scan(&p95); e != nil {
		t.Fatal(e)
	}
	var gaps int
	var count int
	if e = st.Pool().QueryRow(ctx, `SELECT count(*),count(*)FILTER(WHERE event_seq!=generation_number)FROM index_journal_event`).Scan(&count, &gaps); e != nil || count != 400 || gaps != 0 {
		t.Fatal("Q8 order", count, gaps, e)
	}
	qr := postgres.NewQueryReader(st.Pool())
	for _, n := range []int{0, 199, 399} {
		r, e := qr.ResolvePath(ctx, roots[0], fmt.Sprintf("/d%06d/late.bin", n), query.ReadOptions{})
		if e != nil || r.Ambiguous || len(r.Matches) != 1 {
			t.Fatal("Q5 missed finite backlog", n, r, e)
		}
	}
	t.Logf("FINITE_P6_SAFETY scopes=400 HTTP=2000 readers=1 wall_s=%.3f additional_claim_delay_p95_s=%.3f no_production_SLA_claim=true", time.Since(start).Seconds(), p95)
}
