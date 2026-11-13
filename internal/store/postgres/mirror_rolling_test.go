package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/rolling"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func rollingFixture(t *testing.T) (*postgres.Store, context.Context, time.Time) {
	t.Helper()
	st, ctx, now := mirrorPolicyFixture(t, []string{"/a", "/b", "/c"})
	var err error
	st, err = st.WithMirrorRolling(rolling.Policy{SharePercent: 25, Spacing: 200 * time.Millisecond, Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	mirrorParent(t, st, ctx, now, []string{"/a", "/b", "/c"}, "")
	for _, scope := range []string{"/a", "/b", "/c"} {
		rollingVerify(t, st, ctx, scope, now)
	}
	if err = st.PrepareMirrorRolling(ctx); err != nil {
		t.Fatal(err)
	}
	return st, ctx, now
}

func TestIssue134RollingMultiRootClaimLossAndAtomicGlobalBudget(t *testing.T) {
	st, ctx, now := rollingFixture(t)
	const other = "00000000-0000-0000-0000-00000000b001"
	if _, err := st.Pool().Exec(ctx, `INSERT INTO index_root(root_id,scope_descriptor,lifecycle_state) VALUES($1,'{}','ACTIVE');`, other); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAdapterConfig(ctx, other, postgres.AdapterConfig{CollectorKind: "openlist", Config: []byte(`{"path":"/source","mirror_presence":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,is_dir,name,updated_at) VALUES('13430000-0000-4000-8000-000000000001',$1,0,0,'PRESENT','NONE','/source/x',true,'x',$2)`, other, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	w := state.ScopeWatchState{RootID: other, ScopeKey: "/x", WatchState: state.WatchWarm, CadenceClass: "MIRROR_REVISIT", EffectiveIntervalSeconds: p3Interval(120), SourceSet: []state.WatchSource{state.WatchSourceBackstopEnroll}, Priority: state.PriorityNormal, NextDueAt: &now}
	if _, err := st.CreateWatch(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := st.PrepareMirrorRolling(ctx); err != nil {
		t.Fatal(err)
	}
	due := now.Add(3 * time.Minute)
	a, _ := st.GetWatch(ctx, p3RootActive, "/a")
	if _, err := st.EmitRollingPoll(ctx, p3RootActive, "/a", a.Version, due); err != nil {
		t.Fatal(err)
	}
	old, err := p3Claim(t, st, ctx, p3RootActive, "/a", due)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.RecoverStaleInflight(ctx, p3RootActive, due); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err = st.CompleteSuccess(ctx, p3RootActive, "/a", *old.ClaimedSignalSeq, due); !errors.Is(err, postgres.ErrStaleClaim) {
		t.Fatal("lost claim completion accepted", err)
	}
	list, err := st.ListDueWatchesRolling(ctx, due.Add(7*time.Second), 6)
	if err != nil || len(list) != 1 || list[0].RootID != other {
		t.Fatal("one blocked Root hides another", list, err)
	}
	if _, err = st.EmitRollingPoll(ctx, other, "/x", list[0].Version, due.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	rollingVerify(t, st, ctx, "/a", due.Add(8*time.Second))
	claim, err := p3Claim(t, st, ctx, other, "/x", due.Add(8*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.CompleteSuccessWithMirrorChildren(ctx, other, "/x", *claim.ClaimedSignalSeq, nil, "", due.Add(8*time.Second), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetWatch(ctx, p3RootActive, "/b")
	w, _ = st.GetWatch(ctx, other, "/x")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for _, candidate := range []state.ScopeWatchState{a, w} {
		wg.Add(1)
		go func(c state.ScopeWatchState) {
			defer wg.Done()
			_, e := st.EmitRollingPoll(ctx, c.RootID, c.ScopeKey, c.Version, due.Add(200*time.Second))
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				ok++
			} else if !errors.Is(e, postgres.ErrMirrorDeferred) {
				t.Error(e)
			}
		}(candidate)
	}
	wg.Wait()
	stats, err := st.MirrorRollingStats(ctx, due.Add(200*time.Second))
	if err != nil || ok != 1 || stats.Emitted != 3 || stats.OutstandingRoots != 1 {
		t.Fatal("global reservation race", stats, ok, err)
	}
}

func TestIssue134RollingEmissionRollbackAndUntrustedRemoval(t *testing.T) {
	st, ctx, now := rollingFixture(t)
	due := now.Add(3 * time.Minute)
	w, _ := st.GetWatch(ctx, p3RootActive, "/a")
	before, _ := st.GetWork(ctx, p3RootActive, "/a")
	if _, err := st.Pool().Exec(ctx, `CREATE FUNCTION budget_abort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.emitted>OLD.emitted THEN RAISE EXCEPTION 'owned budget rollback'; END IF; RETURN NEW; END $$;CREATE TRIGGER budget_abort BEFORE UPDATE ON index_mirror_budget FOR EACH ROW EXECUTE FUNCTION budget_abort()`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EmitRollingPoll(ctx, p3RootActive, "/a", w.Version, due); err == nil {
		t.Fatal("rollback injection missed")
	}
	after, _ := st.GetWork(ctx, p3RootActive, "/a")
	watch, _ := st.GetWatch(ctx, p3RootActive, "/a")
	stats, err := st.MirrorRollingStats(ctx, due)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(w, watch) || stats.Emitted != 0 || stats.OutstandingRoots != 0 {
		t.Fatal("partial emission", stats, err)
	}
	if _, err = st.Pool().Exec(ctx, `DROP TRIGGER budget_abort ON index_mirror_budget;DROP FUNCTION budget_abort();UPDATE index_canonical_resource SET resource_presence='REMOVED',removal_evidence_state='REMOVAL_CANDIDATE' WHERE canonical_path='/source/a'`); err != nil {
		t.Fatal(err)
	}
	result, err := st.MaintainMirrorWatch(ctx, p3RootActive, "/a", w.Version, 1, now.Add(2*time.Hour))
	if err != nil || result == "RETIRED" {
		t.Fatal("bare tombstone without Journal/Snapshot anchor retired", result, err)
	}
}

func TestIssue134RollingDebtClock(t *testing.T) {
	t.Run("binding-and-adapter-do-not-verify-content", func(t *testing.T) {
		plain, ctx, now := mirrorPolicyFixture(t, []string{"/a"})
		st, err := plain.WithMirrorRolling(rolling.Policy{SharePercent: 25, Spacing: 200 * time.Millisecond, Lease: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		old := now.Add(-time.Hour)
		due := now
		if _, err = st.CreateWatch(ctx, state.ScopeWatchState{RootID: p3RootActive, ScopeKey: "/a", WatchState: state.WatchWarm, CadenceClass: "MIRROR_REVISIT", EffectiveIntervalSeconds: p3Interval(120), SourceSet: []state.WatchSource{state.WatchSourceBackstopEnroll}, Priority: state.PriorityNormal, NextDueAt: &due, LastSuccessAt: &old}); err != nil {
			t.Fatal(err)
		}
		if _, err = plain.MergeSignal(ctx, p3Signal(p3RootActive, "/a", state.SourceRecovery, state.ReasonPossibleChange, old)); err != nil {
			t.Fatal(err)
		}
		c, err := p3Claim(t, plain, ctx, p3RootActive, "/a", old)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = plain.CompleteSuccess(ctx, p3RootActive, "/a", *c.ClaimedSignalSeq, old); err != nil {
			t.Fatal(err)
		}
		if _, err = st.Pool().Exec(ctx, `UPDATE index_canonical_resource SET updated_at=$1 WHERE canonical_path='/source/a'`, now); err != nil {
			t.Fatal(err)
		}
		before, err := st.MirrorRollingStats(ctx, now.Add(3*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		mirrorParent(t, st, ctx, now, []string{"/a"}, "")
		after, err := st.MirrorRollingStats(ctx, now.Add(3*time.Minute))
		if err != nil || after.FreshnessDebtSeconds != before.FreshnessDebtSeconds {
			t.Fatal("parent binding reset content debt", before, after, err)
		}
		if err = st.UpsertAdapterConfig(ctx, p3RootActive, postgres.AdapterConfig{CollectorKind: "openlist", Config: []byte(`{"path":"/source","mirror_presence":true}`)}); err != nil {
			t.Fatal(err)
		}
		mirrorParent(t, st, ctx, now.Add(time.Minute), []string{"/a"}, "")
		after, err = st.MirrorRollingStats(ctx, now.Add(3*time.Minute))
		if err != nil || after.FreshnessDebtSeconds != before.FreshnessDebtSeconds {
			t.Fatal("adapter rebind reset content debt", before, after, err)
		}
		rollingVerify(t, st, ctx, "/a", now.Add(3*time.Minute))
		after, err = st.MirrorRollingStats(ctx, now.Add(4*time.Minute))
		if err != nil || after.FreshnessDebtSeconds != 0 {
			t.Fatal("actual scope verification did not pay debt", after, err)
		}
	})
	t.Run("all-successful-verification-counts-for-legacy", func(t *testing.T) {
		st, ctx, now := rollingFixture(t)
		if _, err := st.Pool().Exec(ctx, `DELETE FROM index_mirror_watch_binding`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `UPDATE index_scope_watch_state SET last_success_at=$1`, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		stats, err := st.MirrorRollingStats(ctx, now.Add(3*time.Minute))
		if err != nil || stats.FreshnessDebtSeconds != 180 {
			t.Fatal("legacy Recovery success ignored in debt", stats, err)
		}
	})
}

func rollingVerify(t *testing.T, st *postgres.Store, ctx context.Context, scope string, now time.Time) {
	t.Helper()
	c, err := p3Claim(t, st, ctx, p3RootActive, scope, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.CompleteSuccessWithMirrorChildren(ctx, p3RootActive, scope, *c.ClaimedSignalSeq, nil, "", now, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestIssue134RollingDurableBudgetAndBackpressure(t *testing.T) {
	st, ctx, now := rollingFixture(t)
	due := now.Add(121 * time.Second)
	list, err := st.ListDueWatchesRolling(ctx, due, 6)
	if err != nil || len(list) != 1 || list[0].ScopeKey != "/a" {
		t.Fatal(list, err)
	}
	wk, err := st.EmitRollingPoll(ctx, p3RootActive, "/a", list[0].Version, due)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetWatch(ctx, p3RootActive, "/a")
	if _, err = st.EmitRollingPoll(ctx, p3RootActive, "/a", list[0].Version, due); !errors.Is(err, postgres.ErrStateCASConflict) {
		t.Fatal("old CAS accepted", err)
	}
	if _, err = st.CreateWatch(ctx, p3HotWatch(p3RootActive, "/", due)); err != nil {
		t.Fatal(err)
	}
	restarted, err := postgres.New(st.Pool()).WithMirrorRolling(rolling.Policy{SharePercent: 25, Spacing: 200 * time.Millisecond, Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.PrepareMirrorRolling(ctx); err != nil {
		t.Fatal(err)
	}
	list, err = restarted.ListDueWatchesRolling(ctx, due.Add(time.Second), 6)
	if err != nil || len(list) != 1 || list[0].ScopeKey != "/" {
		t.Fatal("reservation lost or HOT hidden", list, err)
	}
	stats, err := st.MirrorRollingStats(ctx, due.Add(time.Second))
	if err != nil || stats.OutstandingRoots != 1 || stats.Emitted != 1 || stats.FreshnessDebtSeconds <= 0 {
		t.Fatal(stats, err)
	}
	claim, err := p3Claim(t, st, ctx, p3RootActive, "/a", due.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	bg, err := st.IsMirrorBackground(ctx, claim)
	if err != nil || !bg {
		t.Fatal("poll epoch not classified", bg, err)
	}
	if _, err = st.MergeSignal(ctx, p3Signal(p3RootActive, "/a", state.SourceMutationHint, state.ReasonPossibleChange, due.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.CompleteSuccessWithMirrorChildren(ctx, p3RootActive, "/a", *claim.ClaimedSignalSeq, nil, "", due.Add(2*time.Second), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	current, _ := st.GetWork(ctx, p3RootActive, "/a")
	if current.WorkState != state.WorkPending || current.SignalSeq <= wk.SignalSeq {
		t.Fatal("newer Hint acknowledged", current)
	}
	list, err = st.ListDueWatchesRolling(ctx, due.Add(2*time.Second), 6)
	if err != nil || len(list) != 1 {
		t.Fatal("restart burst bypassed durable budget", list, err)
	}
	list, err = st.ListDueWatchesRolling(ctx, due.Add(7*time.Second), 6)
	if err != nil || len(list) != 2 || list[1].ScopeKey != "/b" {
		t.Fatal("round did not advance", list, err)
	}
	ctxCanceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = st.EmitRollingPoll(ctxCanceled, p3RootActive, "/b", list[1].Version, due.Add(7*time.Second)); err == nil {
		t.Fatal("canceled admission succeeded")
	}
	unchanged, _ := st.GetWatch(ctx, p3RootActive, "/a")
	if unchanged.NextDueAt == nil || !unchanged.NextDueAt.Equal(*after.NextDueAt) {
		t.Fatal("denial changed schedule")
	}
}

func TestIssue134RollingExpiryAdapterABAAndProtectedWork(t *testing.T) {
	st, ctx, now := rollingFixture(t)
	w, _ := st.GetWatch(ctx, p3RootActive, "/a")
	before, _ := st.GetWork(ctx, p3RootActive, "/a")
	result, err := st.MaintainMirrorWatch(ctx, p3RootActive, "/a", w.Version, 1, now.Add(2*time.Hour))
	if err != nil || result != "EXPIRED" {
		t.Fatal(result, err)
	}
	after, _ := st.GetWatch(ctx, p3RootActive, "/a")
	if !reflect.DeepEqual(w, after) {
		t.Fatal("lease expiry changed schedule")
	}
	if err = st.UpsertAdapterConfig(ctx, p3RootActive, postgres.AdapterConfig{CollectorKind: "openlist", Config: []byte(`{"path":"/source","mirror_presence":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.EmitRollingPoll(ctx, p3RootActive, "/a", w.Version, now.Add(2*time.Hour)); !errors.Is(err, postgres.ErrMirrorDeferred) {
		t.Fatal("adapter ABA borrowed old binding", err)
	}
	stats := mirrorParent(t, st, ctx, now.Add(2*time.Hour), []string{"/a"}, "")
	if stats.SignalsMerged != 1 {
		t.Fatal("ABA omitted recovery", stats)
	}
	current, _ := st.GetWork(ctx, p3RootActive, "/a")
	if current.WorkState != state.WorkPending || current.SignalSeq <= before.SignalSeq || !current.LastVerifiedAt.Equal(*before.LastVerifiedAt) {
		t.Fatal("ABA reset or reused Work", current)
	}
	if _, err = st.MaintainMirrorWatch(ctx, p3RootActive, "/a", w.Version, 1, now.Add(2*time.Hour)); !errors.Is(err, postgres.ErrStateCASConflict) {
		t.Fatal("binding epoch ABA accepted", err)
	}
	rollingVerify(t, st, ctx, "/a", now.Add(2*time.Hour))
	s, err := st.MirrorRollingStats(ctx, now.Add(2*time.Hour))
	if err != nil || s.Rebindings != 1 || s.ExpirationEvents != 1 || s.Renewals != 4 || s.RetirementEvents != 0 {
		t.Fatal(s, err)
	}
}

func TestIssue134RollingProtectedScopeRotatesWithoutDebtReset(t *testing.T) {
	st, ctx, now := rollingFixture(t)
	due := now.Add(3 * time.Minute)
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/a", state.SourceMutationHint, state.ReasonPossibleChange, due)); err != nil {
		t.Fatal(err)
	}
	w, _ := st.GetWatch(ctx, p3RootActive, "/a")
	before, _ := st.MirrorRollingStats(ctx, due)
	if _, err := st.EmitRollingPoll(ctx, p3RootActive, "/a", w.Version, due); !errors.Is(err, postgres.ErrMirrorDeferred) {
		t.Fatal(err)
	}
	list, err := st.ListDueWatchesRolling(ctx, due, 6)
	if err != nil || len(list) != 1 || list[0].ScopeKey != "/b" {
		t.Fatal("protected scope monopolizes round", list, err)
	}
	after, err := st.MirrorRollingStats(ctx, due)
	if err != nil || after.FreshnessDebtSeconds != before.FreshnessDebtSeconds || after.OutstandingRoots != 0 || after.Deferred != before.Deferred+1 {
		t.Fatal(before, after, err)
	}
}
