package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/rolling"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func registrationStore(t *testing.T) (*postgres.Store, context.Context) {
	t.Helper()
	st, ctx := p3Store(t)
	st, err := st.WithMirrorRolling(rolling.Policy{SharePercent: 25, Spacing: time.Millisecond, Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

func TestIssue134RollingFlagOffDrainAndReenable(t *testing.T) {
	st, ctx, now := rollingFixture(t)
	due := now.Add(3 * time.Minute)
	w, err := st.GetWatch(ctx, p3RootActive, "/a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EmitRollingPoll(ctx, p3RootActive, "/a", w.Version, due); err != nil {
		t.Fatal(err)
	}
	plain := postgres.New(st.Pool()) // F4 disabled, same schema and durable work.
	old, err := p3Claim(t, plain, ctx, p3RootActive, "/a", due)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.RecoverStaleInflight(ctx, p3RootActive, due); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.CompleteSuccess(ctx, p3RootActive, "/a", *old.ClaimedSignalSeq, due); !errors.Is(err, postgres.ErrStaleClaim) {
		t.Fatal("lost claim released reservation", err)
	}
	stats, err := st.MirrorRollingStats(ctx, due)
	if err != nil || stats.OutstandingRoots != 1 {
		t.Fatal(stats, err)
	}
	c, err := p3Claim(t, plain, ctx, p3RootActive, "/a", due)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.MergeSignal(ctx, p3Signal(p3RootActive, "/a", state.SourceMutationHint, state.ReasonPossibleChange, due)); err != nil {
		t.Fatal(err)
	}
	var renewals int64
	var freshness time.Time
	if err := st.Pool().QueryRow(ctx, `SELECT renewals,freshness_at FROM index_mirror_watch_binding WHERE root_id=$1 AND scope_key='/a'`, p3RootActive).Scan(&renewals, &freshness); err != nil {
		t.Fatal(err)
	}
	work, err := plain.CompleteSuccess(ctx, p3RootActive, "/a", *c.ClaimedSignalSeq, due.Add(time.Second))
	if err != nil || work.WorkState != state.WorkPending || len(work.PendingSourceSet) != 1 || work.PendingSourceSet[0] != state.SourceMutationHint {
		t.Fatal("new Hint lost", work, err)
	}
	stats, err = st.MirrorRollingStats(ctx, due)
	if err != nil || stats.OutstandingRoots != 0 {
		t.Fatal("off drain stranded Root", stats, err)
	}
	var afterRenewals int64
	var afterFreshness time.Time
	if err := st.Pool().QueryRow(ctx, `SELECT renewals,freshness_at FROM index_mirror_watch_binding WHERE root_id=$1 AND scope_key='/a'`, p3RootActive).Scan(&afterRenewals, &afterFreshness); err != nil || afterRenewals != renewals || !afterFreshness.Equal(freshness) {
		t.Fatal("flag-off changed binding", err)
	}
	if list, err := st.ListDueWatchesRolling(ctx, due.Add(2*time.Second), 6); err != nil || len(list) != 0 {
		t.Fatal("global budget reset on reenable", list, err)
	}
	list, err := st.ListDueWatchesRolling(ctx, due.Add(8*time.Second), 6)
	if err != nil || len(list) != 1 {
		t.Fatal("reenabled Root stuck", list, err)
	}
	if _, err := st.EmitRollingPoll(ctx, list[0].RootID, list[0].ScopeKey, list[0].Version, due.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func registrationWatch(t *testing.T, st *postgres.Store, ctx context.Context, root, scope string) {
	t.Helper()
	now := p3Time()
	_, err := st.CreateWatch(ctx, state.ScopeWatchState{RootID: root, ScopeKey: scope, WatchState: state.WatchWarm, CadenceClass: "MIRROR_REVISIT", EffectiveIntervalSeconds: p3Interval(120), SourceSet: []state.WatchSource{state.WatchSourceBackstopEnroll}, Priority: state.PriorityNormal, NextDueAt: &now})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIssue134RollingRegistrationAfterEmptyStartup(t *testing.T) {
	st, ctx := registrationStore(t)
	if err := st.PrepareMirrorRolling(ctx); err != nil {
		t.Fatal(err)
	}
	if due, err := st.ListDueWatchesRolling(ctx, p3Time(), 6); err != nil || len(due) != 0 {
		t.Fatal(due, err)
	}
	// No second Prepare and no new actor: first Watch on an existing empty Root.
	registrationWatch(t, st, ctx, p3RootActive, "/first")
	if due, err := st.ListDueWatchesRolling(ctx, p3Time(), 6); err != nil || len(due) != 1 || due[0].ScopeKey != "/first" {
		t.Fatal(due, err)
	}
	// New Root and Watch after startup, including creation while F4 is disabled.
	const other = "00000000-0000-0000-0000-00000000b001"
	if _, err := st.Pool().Exec(ctx, `INSERT INTO index_root(root_id,scope_descriptor,lifecycle_state) VALUES($1,'{}','ACTIVE')`, other); err != nil {
		t.Fatal(err)
	}
	registrationWatch(t, postgres.New(st.Pool()), ctx, other, "/later")
	if _, err := st.Pool().Exec(ctx, `UPDATE index_mirror_budget SET root_cursor=$1`, p3RootActive); err != nil {
		t.Fatal(err)
	}
	if due, err := st.ListDueWatchesRolling(ctx, p3Time(), 6); err != nil || len(due) != 1 || due[0].RootID != other {
		t.Fatal("new Root not selected", due, err)
	}
}

func TestIssue134RollingRegistrationConcurrentAndRestart(t *testing.T) {
	st, ctx := registrationStore(t)
	if err := st.PrepareMirrorRolling(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			root := fmt.Sprintf("00000000-0000-0000-0000-%012d", i+100)
			if _, err := st.Pool().Exec(ctx, `INSERT INTO index_root(root_id,scope_descriptor,lifecycle_state) VALUES($1,'{}','ACTIVE')`, root); err != nil {
				t.Error(err)
				return
			}
			now := p3Time()
			if _, err := st.CreateWatch(ctx, state.ScopeWatchState{RootID: root, ScopeKey: "/new", WatchState: state.WatchWarm, CadenceClass: "MIRROR_REVISIT", EffectiveIntervalSeconds: p3Interval(120), SourceSet: []state.WatchSource{state.WatchSourceBackstopEnroll}, Priority: state.PriorityNormal, NextDueAt: &now}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	var count int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_mirror_root_budget`).Scan(&count); err != nil || count != 10 {
		t.Fatal(count, err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE index_mirror_root_budget SET scope_cursor='/kept',outstanding_scope='/kept',outstanding_seq=17 WHERE root_id=$1`, p3RootActive); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO index_root(root_id,scope_descriptor,lifecycle_state) VALUES($1,'{}','ACTIVE') ON CONFLICT(root_id) DO UPDATE SET updated_at=now()`, p3RootActive); err != nil {
		t.Fatal(err)
	}
	restarted, err := postgres.New(st.Pool()).WithMirrorRolling(rolling.Policy{SharePercent: 25, Spacing: time.Millisecond, Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := restarted.PrepareMirrorRolling(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var cursor, scope string
	var seq int64
	if err := st.Pool().QueryRow(ctx, `SELECT scope_cursor,outstanding_scope,outstanding_seq FROM index_mirror_root_budget WHERE root_id=$1`, p3RootActive).Scan(&cursor, &scope, &seq); err != nil || cursor != "/kept" || scope != "/kept" || seq != 17 {
		t.Fatal(cursor, scope, seq, err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_mirror_root_budget`).Scan(&count); err != nil || count != 10 {
		t.Fatal("duplicate registration", count, err)
	}
}

func TestIssue134RollingRegistrationFirstDiscovery(t *testing.T) {
	st, ctx, now := mirrorPolicyFixture(t, []string{"/a"})
	st, err := st.WithMirrorRolling(rolling.Policy{SharePercent: 25, Spacing: time.Millisecond, Lease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PrepareMirrorRolling(ctx); err != nil {
		t.Fatal(err)
	}
	mirrorParent(t, st, ctx, now, []string{"/a"}, "")
	due, err := st.ListDueWatchesRolling(ctx, now.Add(3*time.Minute), 6)
	if err != nil || len(due) != 1 || due[0].ScopeKey != "/a" {
		t.Fatal("discovery not scheduled without restart", due, err)
	}
}

func TestIssue134RollingRegistrationFailureAtomicRetry(t *testing.T) {
	st, ctx := registrationStore(t)
	if err := st.PrepareMirrorRolling(ctx); err != nil {
		t.Fatal(err)
	}
	const root = "00000000-0000-0000-0000-00000000b001"
	if _, err := st.Pool().Exec(ctx, `CREATE FUNCTION registration_abort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned registration failure'; END $$; CREATE TRIGGER registration_abort BEFORE INSERT ON index_mirror_root_budget FOR EACH ROW EXECUTE FUNCTION registration_abort()`); err != nil {
		t.Fatal(err)
	}
	// Root and Watch in one caller transaction: failure cannot commit either.
	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO index_root(root_id,scope_descriptor,lifecycle_state) VALUES($1,'{}','ACTIVE')`, root)
	if err == nil {
		t.Fatal("failure injection missed")
	}
	_ = tx.Rollback(ctx)
	var count int
	if err := st.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM index_root WHERE root_id=$1)+(SELECT count(*) FROM index_mirror_root_budget WHERE root_id=$1)+(SELECT count(*) FROM index_scope_watch_state WHERE root_id=$1)`, root).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial registration", count, err)
	}
	if _, err := st.Pool().Exec(ctx, `DROP TRIGGER registration_abort ON index_mirror_root_budget; DROP FUNCTION registration_abort()`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO index_root(root_id,scope_descriptor,lifecycle_state) VALUES($1,'{}','ACTIVE')`, root); err != nil {
		t.Fatal(err)
	}
	registrationWatch(t, st, ctx, root, "/retry")
	if due, err := st.ListDueWatchesRolling(ctx, p3Time(), 6); err != nil || len(due) != 1 || due[0].RootID != root {
		t.Fatal("retry needs restart", due, err)
	}
}
