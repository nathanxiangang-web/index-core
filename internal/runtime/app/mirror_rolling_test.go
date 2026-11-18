package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Actual app-composed P6/P5, HTTP transport, admission, Kernel and Q5/Q8.
// The small functional sample is distinct from20K capacity or latency acceptance.
func TestIssue134RollingRuntimeLateFileAndRestart(t *testing.T) {
	st, h, _, roots := poolFixture(t, 1, 2)
	ctx := context.Background()
	h.visible.Store(false)
	cfg := poolConfig(1)
	cfg.IncrementalMirrorRevisit = 2 * time.Minute
	cfg.MirrorRollingPercent = 25
	cfg.SourceGlobalInterval = 200 * time.Millisecond
	cfg.SourceRootInterval = 200 * time.Millisecond
	cfg.SourceProviderInterval = 200 * time.Millisecond
	cycle, err := buildIncrementalCycle(st, cfg, poolLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	due := now.Add(-time.Second)
	interval := int64(120)
	if _, err = st.CreateWatch(ctx, state.ScopeWatchState{RootID: roots[0], ScopeKey: "/d000000", WatchState: state.WatchWarm, CadenceClass: "MIRROR_REVISIT", EffectiveIntervalSeconds: &interval, SourceSet: []state.WatchSource{state.WatchSourceBackstopEnroll}, Priority: state.PriorityNormal, NextDueAt: &due}); err != nil {
		t.Fatal(err)
	}
	// Same running actor prepared with no managed Watch; do not rebuild it.
	h.visible.Store(true)
	before := h.api.Load()
	started := time.Now()
	result, err := cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle)
	if err != nil || result.DueEmitted != 1 || result.Executor.Succeeded != 1 {
		t.Fatal(result, err)
	}
	if time.Since(started) < 4*800*time.Millisecond {
		t.Fatal("actual background HTTP share bypassed")
	}
	qr := postgres.NewQueryReader(st.Pool())
	visible, err := qr.ResolvePath(ctx, roots[0], "/d000000/late.bin", query.ReadOptions{})
	if err != nil || len(visible.Matches) != 1 {
		t.Fatal("late file Q5", visible, err)
	}
	journal, err := qr.ReadJournal(ctx, roots[0], 0, 100)
	if err != nil || len(journal) != 1 || journal[0].ResourceID == nil || *journal[0].ResourceID != visible.Matches[0].ResourceID {
		t.Fatal("late file Q8", journal, err)
	}
	if h.api.Load()-before != 5 {
		t.Fatal("actual safe HTTP observation cost changed", h.api.Load()-before)
	}
	// Reconstruct the app chain: durable ledger/binding must survive process state.
	cycle, err = buildIncrementalCycle(st, cfg, poolLogger())
	if err != nil {
		t.Fatal(err)
	}
	if result, err = cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle); err != nil || result.DueEmitted != 0 {
		t.Fatal("restart emitted a duplicate", result, err)
	}
	stats, err := st.MirrorRollingStats(ctx, time.Now())
	if err != nil || stats.OutstandingRoots != 0 || stats.Emitted != 1 || stats.Renewals != 1 || stats.RetirementEvents != 0 {
		t.Fatal(stats, err)
	}
}

func TestIssue134RollingManualCommandRejectedBeforeIO(t *testing.T) {
	cfg := poolConfig(1)
	cfg.DatabaseURL = "postgres://unused"
	cfg.IncrementalMirrorRevisit = 2 * time.Minute
	cfg.MirrorRollingPercent = 25
	err := RunIncremental(context.Background(), cfg, poolLogger(), nil)
	if err == nil || !strings.Contains(err.Error(), "only supported by serve hybrid") {
		t.Fatal("manual command silently bypassed rolling policy", err)
	}
}

func TestIssue134RollingPoolMixedForeground(t *testing.T) {
	st, h, _, roots := poolFixture(t, 1, 2)
	ctx := context.Background()
	h.visible.Store(false)
	cfg := poolConfig(4)
	cfg.IncrementalMirrorRevisit = 2 * time.Minute
	cycle, err := buildIncrementalCycle(st, cfg, poolLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(-time.Second)
	interval := int64(120)
	if _, err = st.CreateWatch(ctx, state.ScopeWatchState{RootID: roots[0], ScopeKey: "/d000000", WatchState: state.WatchWarm, CadenceClass: "MIRROR_REVISIT", EffectiveIntervalSeconds: &interval, SourceSet: []state.WatchSource{state.WatchSourceBackstopEnroll}, Priority: state.PriorityNormal, NextDueAt: &due}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.MergeSignal(ctx, state.DirtySignal{RootID: roots[0], ScopeKey: "/d000001", Source: state.SourceMutationHint, Reason: state.ReasonPossibleChange, Priority: state.PriorityHigh, SeenAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	cfg.MirrorRollingPercent = 25
	cfg.SourceGlobalInterval = 200 * time.Millisecond
	cfg.SourceRootInterval = 200 * time.Millisecond
	cfg.SourceProviderInterval = 200 * time.Millisecond
	cycle, err = buildIncrementalCycle(st, cfg, poolLogger())
	if err != nil {
		t.Fatal(err)
	}
	before := h.api.Load()
	h.visible.Store(true)
	result, err := cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle)
	if err != nil || result.Executor.Succeeded != 2 {
		t.Fatal(result, err)
	}
	if h.api.Load()-before != 10 {
		t.Fatal("mixed pool lost or duplicated HTTP", h.api.Load()-before)
	}
	qr := postgres.NewQueryReader(st.Pool())
	events, err := qr.ReadJournal(ctx, roots[0], 0, 100)
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	for i, e := range events {
		if e.EventSeq != int64(i+1) || e.GenerationNumber != int64(i+1) {
			t.Fatal("mixed pool broke serial order", events)
		}
	}
	for _, path := range []string{"/d000000/late.bin", "/d000001/late.bin"} {
		r, e := qr.ResolvePath(ctx, roots[0], path, query.ReadOptions{})
		if e != nil || len(r.Matches) != 1 {
			t.Fatal("mixed foreground Q5", r, e)
		}
	}
	stats, err := st.MirrorRollingStats(ctx, time.Now())
	if err != nil || stats.Emitted != 1 || stats.OutstandingRoots != 0 || stats.Renewals != 1 {
		t.Fatal(stats, err)
	}
}

func TestIssue134RollingRuntimeFailureBackpressure(t *testing.T) {
	for _, kind := range []string{"429", "api429", "503", "api503", "403", "timeout", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			st, h, _, roots := poolFixture(t, 1, 2)
			ctx := context.Background()
			h.visible.Store(false)
			cfg := poolConfig(1)
			cfg.IncrementalMirrorRevisit = 2 * time.Minute
			cycle, err := buildIncrementalCycle(st, cfg, poolLogger())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle); err != nil {
				t.Fatal(err)
			}
			due := time.Now().Add(-time.Second)
			interval := int64(120)
			if _, err = st.CreateWatch(ctx, state.ScopeWatchState{RootID: roots[0], ScopeKey: "/d000000", WatchState: state.WatchWarm, CadenceClass: "MIRROR_REVISIT", EffectiveIntervalSeconds: &interval, SourceSet: []state.WatchSource{state.WatchSourceBackstopEnroll}, Priority: state.PriorityNormal, NextDueAt: &due}); err != nil {
				t.Fatal(err)
			}
			//1ms is a functional fault fixture, never a performance comparison.
			cfg.MirrorRollingPercent = 25
			cfg.ScanTimeout = 200 * time.Millisecond
			h.bad = "/d000000"
			h.visible.Store(true)
			switch kind {
			case "429":
				h.http429 = true
				h.code.Store(429)
			case "api429":
				h.code.Store(429)
			case "503":
				h.httpStatus.Store(503)
			case "api503":
				h.code.Store(503)
			case "403":
				h.code.Store(403)
			case "timeout":
				h.delay.Store(int64(time.Second))
			case "cancel":
				h.block = true
			}
			cycle, err = buildIncrementalCycle(st, cfg, poolLogger())
			if err != nil {
				t.Fatal(err)
			}
			if kind == "cancel" {
				runCtx, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				go func() { defer close(done); cycle.RunCycle(runCtx, runtimeConfig(cfg).Cycle) }()
				select {
				case <-h.started:
				case <-time.After(3 * time.Second):
					cancel()
					t.Fatal("source did not start")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("canceled actor did not join")
				}
				if n, e := st.RecoverStaleInflight(ctx, roots[0], time.Now()); e != nil || n != 1 {
					t.Fatal("lost canceled claim", n, e)
				}
			} else {
				if _, err = cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle); err != nil {
					t.Fatal(err)
				}
			}
			work, err := st.GetWork(ctx, roots[0], "/d000000")
			if err != nil || work.WorkState == state.WorkVerified {
				t.Fatal("failed source acknowledged", work, err)
			}
			if kind == "cancel" {
				if work.LastErrorClass != nil {
					t.Fatal("cancellation charged as provider failure", work)
				}
			} else {
				expected := state.ErrorTransientProvider
				if kind == "429" || kind == "api429" {
					expected = state.ErrorThrottled
				}
				if kind == "403" {
					expected = state.ErrorAuthOrPermission
				}
				if work.LastErrorClass == nil || *work.LastErrorClass != expected {
					t.Fatal("incorrect failure classification", kind, work)
				}
			}
			stats, err := st.MirrorRollingStats(ctx, time.Now())
			if err != nil || stats.Emitted != 1 || stats.OutstandingRoots != 1 || stats.RetirementEvents != 0 || stats.Renewals != 0 {
				t.Fatal("failed reservation silently freed", stats, err)
			}
			var files int
			if err = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_journal_event WHERE root_id=$1`, roots[0]).Scan(&files); err != nil || files != 0 {
				t.Fatal("failure committed partial canonical history", files, err)
			}
			if kind == "403" {
				if _, err = st.MergeSignal(ctx, state.DirtySignal{RootID: roots[0], ScopeKey: "/d000001", Source: state.SourceMutationHint, Reason: state.ReasonPossibleChange, Priority: state.PriorityHigh, SeenAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				if _, err = cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle); err != nil {
					t.Fatal(err)
				}
				peer, err := st.GetWork(ctx, roots[0], "/d000001")
				if err != nil || peer.WorkState != state.WorkVerified {
					t.Fatal("background failure blocked foreground peer", peer, err)
				}
			}
		})
	}
}
