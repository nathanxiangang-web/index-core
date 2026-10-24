package postgres_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func mirrorPolicyFixture(t *testing.T, scopes []string) (*postgres.Store, context.Context, time.Time) {
	t.Helper()
	st, ctx := p3Store(t)
	now := p3Time()
	if err := st.UpsertAdapterConfig(ctx, p3RootActive, postgres.AdapterConfig{CollectorKind: "openlist", Config: []byte(`{"path":"/source","mirror_presence":true}`)}); err != nil {
		t.Fatal(err)
	}
	for i, scope := range scopes {
		id := fmt.Sprintf("13420000-0000-4000-8000-%012d", i+1)
		if _, err := st.Pool().Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,is_dir,name,updated_at) VALUES($1,$2,0,0,'PRESENT','NONE',$3,true,$4,$5)`, id, p3RootActive, "/source"+scope, scope, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	return st, ctx, now
}

func mirrorParent(t *testing.T, st *postgres.Store, ctx context.Context, now time.Time, children []string, priority string) state.MirrorDiscoveryStats {
	t.Helper()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/", state.SourceManualOperator, state.ReasonManualVerify, now)); err != nil {
		t.Fatal(err)
	}
	p, err := p3Claim(t, st, ctx, p3RootActive, "/", now)
	if err != nil {
		t.Fatal(err)
	}
	_, stats, err := st.CompleteSuccessWithMirrorChildren(ctx, p3RootActive, "/", *p.ClaimedSignalSeq, children, priority, now, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func TestIssue134MirrorDecisionMatrixAndEpochPreservation(t *testing.T) {
	children := []string{"/new", "/verified", "/pending", "/changed", "/retry", "/blocked", "/inflight", "/unknown", "/high", "/disabled"}
	st, ctx, now := mirrorPolicyFixture(t, children)
	for _, scope := range children[1:] {
		if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, scope, state.SourceRecovery, state.ReasonPossibleChange, now.Add(-time.Minute))); err != nil {
			t.Fatal(err)
		}
		if scope == "/pending" {
			continue
		}
		c, err := p3Claim(t, st, ctx, p3RootActive, scope, now.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		switch scope {
		case "/inflight":
			if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, scope, state.SourceMutationHint, state.ReasonDeleteHint, now)); err != nil {
				t.Fatal(err)
			}
		case "/retry", "/blocked":
			class := state.ErrorTransientProvider
			barrier := now.Add(time.Minute)
			if scope == "/blocked" {
				class = state.ErrorAuthOrPermission
			}
			if _, err := st.CompleteFailure(ctx, p3RootActive, scope, *c.ClaimedSignalSeq, class, &barrier, now.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
		default:
			if _, err := st.CompleteSuccess(ctx, p3RootActive, scope, *c.ClaimedSignalSeq, now.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE index_canonical_resource SET updated_at=$1 WHERE canonical_path='/source/changed'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE index_canonical_resource SET canonical_path='/other' WHERE canonical_path='/source/unknown'`); err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateWatch(ctx, state.ScopeWatchState{RootID: p3RootActive, ScopeKey: "/disabled", WatchState: state.WatchDisabled, CadenceClass: "OPERATOR", SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy}, Priority: state.PriorityNormal})
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]state.DirtyScopeWork{}
	for _, scope := range children[1:] {
		before[scope], err = st.GetWork(ctx, p3RootActive, scope)
		if err != nil {
			t.Fatal(err)
		}
	}
	stats := mirrorParent(t, st, ctx, now, children, "/high")
	if stats.NewChildren != 1 || stats.VerifiedReused != 1 || stats.AlreadyQueued != 1 || stats.ChangedChildren != 1 || stats.ProtectedChildren != 3 || stats.UncertainChildren != 2 || stats.ExplicitChildren != 1 || stats.SignalsMerged != 8 {
		t.Fatal(stats)
	}
	for _, scope := range []string{"/verified", "/pending"} {
		after, err := st.GetWork(ctx, p3RootActive, scope)
		if err != nil || !reflect.DeepEqual(before[scope], after) {
			t.Fatal("redundant signal rewrote epoch", scope, err)
		}
	}
	afterWatch, err := st.GetWatch(ctx, p3RootActive, "/disabled")
	if err != nil || !reflect.DeepEqual(w, afterWatch) {
		t.Fatal("operator policy overwritten", err)
	}
	for _, scope := range []string{"/retry", "/blocked", "/inflight"} {
		after, err := st.GetWork(ctx, p3RootActive, scope)
		b := before[scope]
		if err != nil || after.WorkState != b.WorkState || !reflect.DeepEqual(after.PendingNotBefore, b.PendingNotBefore) || !reflect.DeepEqual(after.ClaimedSignalSeq, b.ClaimedSignalSeq) || after.ConsecutiveFailures != b.ConsecutiveFailures || after.AttemptCount != b.AttemptCount {
			t.Fatal("protected state changed", scope, err)
		}
	}
	// Parent rescans do not postpone the durable deadline, or mutate verified Work.
	watch, _ := st.GetWatch(ctx, p3RootActive, "/verified")
	for i := 1; i <= 3; i++ {
		mirrorParent(t, st, ctx, now.Add(time.Duration(i)*30*time.Second), []string{"/verified"}, "")
	}
	afterWatch, _ = st.GetWatch(ctx, p3RootActive, "/verified")
	if !reflect.DeepEqual(watch, afterWatch) {
		t.Fatal("hot parent postponed rolling revisit")
	}
}

func TestIssue134MirrorDuePollHintRestartAndCAS(t *testing.T) {
	st, ctx, now := mirrorPolicyFixture(t, []string{"/old"})
	mirrorParent(t, st, ctx, now, []string{"/old"}, "")
	c, err := p3Claim(t, st, ctx, p3RootActive, "/old", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteSuccess(ctx, p3RootActive, "/old", *c.ClaimedSignalSeq, now); err != nil {
		t.Fatal(err)
	}
	mirrorParent(t, st, ctx, now.Add(time.Second), []string{"/old"}, "")
	if due, err := st.ListDueWatches(ctx, now.Add(119*time.Second), 10); err != nil || len(due) != 0 {
		t.Fatal(due, err)
	}
	// A new Store instance uses only persisted state, no volatile debounce.
	restarted := postgres.New(st.Pool())
	due, err := restarted.ListDueWatches(ctx, now.Add(120*time.Second), 10)
	if err != nil || len(due) != 1 {
		t.Fatal(due, err)
	}
	if _, err := restarted.EmitDuePoll(ctx, p3RootActive, "/old", due[0].Version, now.Add(120*time.Second)); err != nil {
		t.Fatal(err)
	}
	w, _ := st.GetWork(ctx, p3RootActive, "/old")
	if w.WorkState != state.WorkPending || w.PendingNotBefore != nil || !state.ContainsTriggerSource(w.PendingSourceSet, state.SourcePollSchedule) {
		t.Fatal(w)
	}
	if _, err := restarted.EmitDuePoll(ctx, p3RootActive, "/old", due[0].Version, now.Add(120*time.Second)); err != postgres.ErrStateCASConflict {
		t.Fatal("stale watch replay accepted", err)
	}
	// A formal manual/recovery signal is never swallowed by discovery coalescing.
	oldSeq := w.SignalSeq
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/old", state.SourceManualOperator, state.ReasonManualVerify, now.Add(121*time.Second))); err != nil {
		t.Fatal(err)
	}
	w, _ = st.GetWork(ctx, p3RootActive, "/old")
	if w.SignalSeq != oldSeq+1 {
		t.Fatal("manual intent dropped")
	}
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/old", state.SourceRecovery, state.ReasonRetry, now.Add(121*time.Second))); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RecoverStaleInflight(ctx, p3RootActive, now.Add(122*time.Second)); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestIssue134MirrorRollbackStaleAndWatchBounds(t *testing.T) {
	st, ctx, now := mirrorPolicyFixture(t, []string{"/fresh"})
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/", state.SourceManualOperator, state.ReasonManualVerify, now)); err != nil {
		t.Fatal(err)
	}
	c, err := p3Claim(t, st, ctx, p3RootActive, "/", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, stats, err := st.CompleteSuccessWithMirrorChildren(ctx, p3RootActive, "/", *c.ClaimedSignalSeq+1, []string{"/fresh"}, "", now, time.Minute); err != postgres.ErrStaleClaim || stats.WatchEnrolled != 0 {
		t.Fatal(stats, err)
	}
	if _, err := st.Pool().Exec(ctx, `CREATE FUNCTION fail_f2_parent() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.scope_key='/' AND NEW.work_state='VERIFIED' THEN RAISE EXCEPTION 'F2 rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_f2 BEFORE UPDATE ON index_dirty_scope_work FOR EACH ROW EXECUTE FUNCTION fail_f2_parent()`); err != nil {
		t.Fatal(err)
	}
	if _, stats, err := st.CompleteSuccessWithMirrorChildren(ctx, p3RootActive, "/", *c.ClaimedSignalSeq, []string{"/fresh"}, "", now, time.Minute); err == nil || stats.WatchEnrolled != 0 {
		t.Fatal("uncommitted stats", stats, err)
	}
	var count int
	if err := st.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM index_scope_watch_state)+(SELECT count(*) FROM index_dirty_scope_work WHERE scope_key='/fresh')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial transaction", count, err)
	}
	if _, err := st.Pool().Exec(ctx, `DROP TRIGGER fail_f2 ON index_dirty_scope_work; DROP FUNCTION fail_f2_parent()`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompleteSuccessWithMirrorChildren(ctx, p3RootActive, "/", *c.ClaimedSignalSeq, []string{"/fresh"}, "", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	fresh, err := p3Claim(t, st, ctx, p3RootActive, "/fresh", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteSuccess(ctx, p3RootActive, "/fresh", *fresh.ClaimedSignalSeq, now); err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWatch(ctx, p3RootActive, "/fresh")
	if err != nil {
		t.Fatal(err)
	}
	huge := int64(9223372036854775807)
	w.EffectiveIntervalSeconds = &huge
	w, err = st.CASUpdateWatch(ctx, w, w.Version)
	if err != nil {
		t.Fatal(err)
	}
	stats := mirrorParent(t, st, ctx, now.Add(time.Second), []string{"/fresh"}, "")
	if stats.UncertainChildren != 1 || stats.SignalsMerged != 1 {
		t.Fatal("overflow watch accepted as coverage", stats)
	}
	after, _ := st.GetWatch(ctx, p3RootActive, "/fresh")
	if !reflect.DeepEqual(w, after) {
		t.Fatal("watch bounds repair was implicit")
	}
}

func TestIssue134MirrorDueFairnessVersusLegacyFIFO(t *testing.T) {
	st, ctx, now := mirrorPolicyFixture(t, nil)
	interval := int64(120)
	for i := 0; i < 20; i++ {
		due := now.Add(-time.Minute)
		if _, err := st.CreateWatch(ctx, state.ScopeWatchState{RootID: p3RootActive, ScopeKey: fmt.Sprintf("/old-%02d", i), WatchState: state.WatchWarm, CadenceClass: "MIRROR_REVISIT", SourceSet: []state.WatchSource{state.WatchSourceBackstopEnroll}, Priority: state.PriorityNormal, EffectiveIntervalSeconds: &interval, NextDueAt: &due}); err != nil {
			t.Fatal(err)
		}
	}
	due := now
	if _, err := st.CreateWatch(ctx, state.ScopeWatchState{RootID: p3RootActive, ScopeKey: "/", WatchState: state.WatchHot, CadenceClass: "SOURCE_MIRROR", SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy}, Priority: state.PriorityHigh, EffectiveIntervalSeconds: &interval, NextDueAt: &due}); err != nil {
		t.Fatal(err)
	}
	legacy, err := st.ListDueWatches(ctx, now, 6)
	if err != nil || len(legacy) != 6 {
		t.Fatal(err)
	}
	for _, w := range legacy {
		if w.ScopeKey == "/" {
			t.Fatal("baseline starvation not reproduced")
		}
	}
	fair, err := st.ListDueWatchesWithMirrorFairness(ctx, now, 6)
	if err != nil || len(fair) != 6 || fair[0].ScopeKey != "/" || fair[1].ScopeKey != "/old-00" {
		t.Fatal(fair, err)
	}
	for i := 1; i < len(fair); i++ {
		if fair[i].ScopeKey != fmt.Sprintf("/old-%02d", i-1) {
			t.Fatal("cohort FIFO lost", fair)
		}
	}
	// Reader does not advance schedules; unchanged version CAS still owns writes.
	if _, err := st.EmitDuePoll(ctx, p3RootActive, "/", fair[0].Version, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EmitDuePoll(ctx, p3RootActive, "/", fair[0].Version, now); err != postgres.ErrStateCASConflict {
		t.Fatal("CAS replay bypass", err)
	}
	for _, limit := range []int{0, 1, 2, 7, 100} {
		if _, err := st.ListDueWatchesWithMirrorFairness(ctx, now, limit); err == nil {
			t.Fatal("unbounded selector", limit)
		}
	}
}
