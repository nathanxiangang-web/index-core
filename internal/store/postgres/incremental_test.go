package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

const (
	p3RootActive   = "00000000-0000-0000-0000-00000000a001"
	p3RootInactive = "00000000-0000-0000-0000-00000000a002"
)

func p3Store(t *testing.T) (*postgres.Store, context.Context) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, seed := range []struct{ id, lifecycle string }{
		{p3RootActive, "ACTIVE"},
		{p3RootInactive, "DEPRECATED"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO index_root(root_id, scope_descriptor, lifecycle_state)
			 VALUES ($1, '{}'::jsonb, $2)`, seed.id, seed.lifecycle); err != nil {
			t.Fatalf("seed root %s: %v", seed.id, err)
		}
	}
	return postgres.New(pool), ctx
}

func p3Time() time.Time {
	return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
}

func p3Interval(sec int64) *int64 { return &sec }

func p3HotWatch(rootID, scopeKey string, dueAt time.Time) state.ScopeWatchState {
	d := dueAt
	return state.ScopeWatchState{
		RootID:                   rootID,
		ScopeKey:                 scopeKey,
		WatchState:               state.WatchHot,
		CadenceClass:             "HOT_120",
		EffectiveIntervalSeconds: p3Interval(120),
		SourceSet:                []state.WatchSource{state.WatchSourceOperatorPolicy},
		Priority:                 state.PriorityNormal,
		NextDueAt:                &d,
	}
}

func p3Signal(rootID, scopeKey string, src state.TriggerSource, reason state.TriggerReason, at time.Time) state.DirtySignal {
	return state.DirtySignal{
		RootID: rootID, ScopeKey: scopeKey, Source: src, Reason: reason,
		Priority: state.PriorityNormal, SeenAt: at,
	}
}

// p3Claim reads the current work row (its version) and claims it under that
// version, mirroring the executor's select -> version -> claim sequence.
func p3Claim(t *testing.T, st *postgres.Store, ctx context.Context, rootID, scopeKey string, now time.Time) (state.DirtyScopeWork, error) {
	t.Helper()
	w, err := st.GetWork(ctx, rootID, scopeKey)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	return st.ClaimWork(ctx, rootID, scopeKey, w.Version, now)
}

// --- Migration ---------------------------------------------------------------

func TestP3MigrationAddsOnlyOperationalTables(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, table := range []string{"index_scope_watch_state", "index_dirty_scope_work"} {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.tables
			  WHERE table_schema='public' AND table_name=$1`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("migration 0005 must create %s", table)
		}
	}
	// Idempotent rerun.
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("idempotent rerun: %v", err)
	}
	// Upgrade from a pre-0005 (0001..0004) schema must succeed and recreate only
	// the operational tables.
	if _, err := pool.Exec(ctx,
		`DROP TABLE index_dirty_scope_work; DROP TABLE index_scope_watch_state;
		 DELETE FROM schema_migrations WHERE version LIKE '0005%'`); err != nil {
		t.Fatalf("simulate pre-0005 schema: %v", err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("upgrade from pre-0005 schema: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		  WHERE table_schema='public' AND table_name='index_scope_watch_state'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("upgrade must recreate operational tables, n=%d err=%v", n, err)
	}
}

// --- Watch CRUD / CAS --------------------------------------------------------

func TestP3WatchCreateReadAndCASConflict(t *testing.T) {
	st, ctx := p3Store(t)
	w := p3HotWatch(p3RootActive, "/a", p3Time().Add(-time.Minute))
	created, err := st.CreateWatch(ctx, w)
	if err != nil {
		t.Fatalf("create watch: %v", err)
	}
	if created.Version != 1 {
		t.Fatalf("version must start at 1, got %d", created.Version)
	}
	got, err := st.GetWatch(ctx, p3RootActive, "/a")
	if err != nil || got.ScopeKey != "/a" {
		t.Fatalf("get watch: %v %+v", err, got)
	}

	// Stale CAS must fail with ErrStateCASConflict and leave the row unchanged.
	if _, err := st.CASUpdateWatch(ctx, got, created.Version+99); !errors.Is(err, postgres.ErrStateCASConflict) {
		t.Fatalf("stale CAS must conflict, got %v", err)
	}
	after, _ := st.GetWatch(ctx, p3RootActive, "/a")
	if after.Version != created.Version || after.ConsecutiveFailures != 0 {
		t.Fatal("stale CAS must not partially mutate")
	}

	// Valid CAS bumps version once.
	got.ConsecutiveFailures = 3
	upd, err := st.CASUpdateWatch(ctx, got, created.Version)
	if err != nil {
		t.Fatalf("cas update: %v", err)
	}
	if upd.Version != created.Version+1 || upd.ConsecutiveFailures != 3 {
		t.Fatalf("cas update result %+v", upd)
	}
}

func TestP3DueWatchSelection(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()

	due := p3HotWatch(p3RootActive, "/due", now.Add(-time.Second))
	if _, err := st.CreateWatch(ctx, due); err != nil {
		t.Fatal(err)
	}
	notDue := p3HotWatch(p3RootActive, "/notdue", now.Add(time.Hour))
	if _, err := st.CreateWatch(ctx, notDue); err != nil {
		t.Fatal(err)
	}
	cold := state.ScopeWatchState{RootID: p3RootActive, ScopeKey: "/cold", WatchState: state.WatchCold,
		CadenceClass: "COLD_OFF", Priority: state.PriorityLow}
	if _, err := st.CreateWatch(ctx, cold); err != nil {
		t.Fatal(err)
	}
	deferred := p3HotWatch(p3RootActive, "/deferred", now.Add(-time.Second))
	d := now.Add(time.Hour)
	deferred.DeferredUntil = &d
	if _, err := st.CreateWatch(ctx, deferred); err != nil {
		t.Fatal(err)
	}
	inactive := p3HotWatch(p3RootInactive, "/inactive", now.Add(-time.Second))
	if _, err := st.CreateWatch(ctx, inactive); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListDueWatches(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(got) != 1 || got[0].ScopeKey != "/due" {
		t.Fatalf("due selection must return only ACTIVE HOT/WARM non-deferred due, got %+v", got)
	}
	// Deterministic limit/order.
	if _, err := st.CreateWatch(ctx, p3HotWatch(p3RootActive, "/due2", now.Add(-2*time.Second))); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListDueWatches(ctx, now, 1)
	if len(got) != 1 || got[0].ScopeKey != "/due2" {
		t.Fatalf("limit/order must be earliest next_due first, got %+v", got)
	}
}

// --- Signal merge ------------------------------------------------------------

func TestP3SignalMergeRules(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()

	// absent -> PENDING signal_seq=1
	merged, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/m", state.SourceMutationHint, state.ReasonMetadataUncertain, now))
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	if merged.WorkState != state.WorkPending || merged.SignalSeq != 1 {
		t.Fatalf("absent merge must create PENDING seq=1, got %+v", merged)
	}

	// PENDING union + min eligibility
	nb := now.Add(time.Minute)
	sig := p3Signal(p3RootActive, "/m", state.SourceManualOperator, state.ReasonManualVerify, now)
	sig.NotBefore = &nb
	merged, err = st.MergeSignal(ctx, sig)
	if err != nil {
		t.Fatal(err)
	}
	if merged.SignalSeq != 2 || len(merged.PendingSourceSet) != 2 {
		t.Fatalf("pending merge must union and increment, got %+v", merged)
	}

	// invalid inputs rejected
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/a/", state.SourceMutationHint, state.ReasonPossibleChange, now)); err == nil {
		t.Fatal("invalid scope key must be rejected")
	}
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/m", "BOGUS", state.ReasonPossibleChange, now)); err == nil {
		t.Fatal("invalid source must be rejected")
	}

	// inactive root -> SUSPENDED on creation
	susp, err := st.MergeSignal(ctx, p3Signal(p3RootInactive, "/s", state.SourceMutationHint, state.ReasonPossibleChange, now))
	if err != nil {
		t.Fatal(err)
	}
	if susp.WorkState != state.WorkSuspended {
		t.Fatalf("inactive root merge must create SUSPENDED, got %s", susp.WorkState)
	}
}

func TestP3VerifiedOpensNewEpoch(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()

	// Reach VERIFIED: create PENDING, claim, succeed.
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/e", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	cl, err := p3Claim(t, st, ctx, p3RootActive, "/e", now)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := st.CompleteSuccess(ctx, p3RootActive, "/e", *cl.ClaimedSignalSeq, now)
	if err != nil {
		t.Fatal(err)
	}
	if ver.WorkState != state.WorkVerified {
		t.Fatalf("expected VERIFIED, got %s", ver.WorkState)
	}

	// New trigger opens a new epoch (signal_seq never resets).
	next, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/e", state.SourcePollSchedule, state.ReasonPossibleChange, now))
	if err != nil {
		t.Fatal(err)
	}
	if next.WorkState != state.WorkPending || next.SignalSeq != 2 {
		t.Fatalf("verified merge must open PENDING new epoch seq=2, got %+v", next)
	}
	if len(next.PendingSourceSet) != 1 || next.PendingSourceSet[0] != state.SourcePollSchedule {
		t.Fatalf("new epoch must contain only the new trigger, got %+v", next.PendingSourceSet)
	}
}

func TestP3ConcurrentSignalMergeExactCount(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	const n = 25

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := state.SourceMutationHint
			if i%2 == 0 {
				src = state.SourceManualOperator
			}
			if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/c", src, state.ReasonPossibleChange, now)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent merge error: %v", err)
	}

	wk, err := st.GetWork(ctx, p3RootActive, "/c")
	if err != nil {
		t.Fatal(err)
	}
	if wk.SignalSeq != n {
		t.Fatalf("concurrent merges must not lose signals: want %d, got %d", n, wk.SignalSeq)
	}
	var rows int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM index_dirty_scope_work WHERE root_id=$1 AND scope_key='/c'`, p3RootActive).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("concurrent merges must produce one physical row, got %d", rows)
	}
}

// --- Atomic due poll ---------------------------------------------------------

func TestP3EmitDuePollAtomic(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	w, err := st.CreateWatch(ctx, p3HotWatch(p3RootActive, "/p", now.Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}

	wk, err := st.EmitDuePoll(ctx, p3RootActive, "/p", w.Version, now)
	if err != nil {
		t.Fatalf("emit due poll: %v", err)
	}
	if wk.SignalSeq != 1 || len(wk.PendingSourceSet) != 1 || wk.PendingSourceSet[0] != state.SourcePollSchedule {
		t.Fatalf("due poll must merge one POLL_SCHEDULE, got %+v", wk)
	}
	// Watch advanced and version bumped in the same commit.
	after, _ := st.GetWatch(ctx, p3RootActive, "/p")
	if after.Version != w.Version+1 || after.LastDueAt == nil || after.NextDueAt == nil {
		t.Fatalf("watch must be advanced in the same transaction, got %+v", after)
	}
	if !after.NextDueAt.After(now) {
		t.Fatalf("next_due_at must advance, got %v", after.NextDueAt)
	}

	// Reusing the stale watch version must not emit a second poll signal.
	if _, err := st.EmitDuePoll(ctx, p3RootActive, "/p", w.Version, now); !errors.Is(err, postgres.ErrStateCASConflict) {
		t.Fatalf("stale watch version must conflict, got %v", err)
	}
	wk2, _ := st.GetWork(ctx, p3RootActive, "/p")
	if wk2.SignalSeq != 1 {
		t.Fatalf("stale emit must not add a signal, got %d", wk2.SignalSeq)
	}
}

func TestP3EmitDuePollConcurrentSameVersion(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	w, err := st.CreateWatch(ctx, p3HotWatch(p3RootActive, "/pc", now.Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	ok := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.EmitDuePoll(ctx, p3RootActive, "/pc", w.Version, now); err == nil {
				ok <- true
			}
		}()
	}
	wg.Wait()
	close(ok)
	successes := 0
	for range ok {
		successes++
	}
	if successes != 1 {
		t.Fatalf("exactly one concurrent emit must succeed, got %d", successes)
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/pc")
	if wk.SignalSeq != 1 {
		t.Fatalf("exactly one poll signal expected, got %d", wk.SignalSeq)
	}
}

// --- Claim / provenance ------------------------------------------------------

func TestP3ClaimMovesBucketAndIsolatesClaim(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/cl", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	cl, err := p3Claim(t, st, ctx, p3RootActive, "/cl", now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if cl.WorkState != state.WorkInFlight || cl.ClaimedSignalSeq == nil || *cl.ClaimedSignalSeq != 1 {
		t.Fatalf("claim must snapshot pending->claimed, got %+v", cl)
	}
	if len(cl.PendingSourceSet) != 0 || cl.PendingFirstSeenAt != nil {
		t.Fatalf("claim must clear the pending bucket, got %+v", cl)
	}

	// A post-claim signal must not modify claimed_*.
	post, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/cl", state.SourceManualOperator, state.ReasonManualVerify, now))
	if err != nil {
		t.Fatal(err)
	}
	if len(post.ClaimedSourceSet) != 1 || post.ClaimedSourceSet[0] != state.SourceMutationHint {
		t.Fatalf("post-claim merge must not modify claimed_*, got %+v", post.ClaimedSourceSet)
	}
	if len(post.PendingSourceSet) != 1 || post.PendingSourceSet[0] != state.SourceManualOperator {
		t.Fatalf("post-claim merge must go to pending, got %+v", post.PendingSourceSet)
	}
}

func TestP3ClaimMissingWatchRollsBack(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	// POLL_SCHEDULE signal with no matching watch row.
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/nowatch", state.SourcePollSchedule, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	if _, err := p3Claim(t, st, ctx, p3RootActive, "/nowatch", now); err == nil {
		t.Fatal("claim of POLL_SCHEDULE without a watch must fail closed")
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/nowatch")
	if wk.WorkState != state.WorkPending {
		t.Fatalf("failed claim must leave the row PENDING, got %s", wk.WorkState)
	}
}

func TestP3ClaimOnInactiveRootSuspends(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	// Create a runnable row while ACTIVE, then make the root non-ACTIVE.
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/sus", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE index_root SET lifecycle_state='DEPRECATED' WHERE root_id=$1`, p3RootActive); err != nil {
		t.Fatal(err)
	}
	if _, err := p3Claim(t, st, ctx, p3RootActive, "/sus", now); err == nil {
		t.Fatal("claim on inactive root must fail closed")
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/sus")
	if wk.WorkState != state.WorkSuspended {
		t.Fatalf("claim on inactive root must suspend the work, got %s", wk.WorkState)
	}
}

// --- Success -----------------------------------------------------------------

func TestP3PartialSuccessWatermarkAndPendingIsolation(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	// signal 7 claimed; signal 8 arrives during the attempt.
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/s78", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	cl, err := p3Claim(t, st, ctx, p3RootActive, "/s78", now)
	if err != nil {
		t.Fatal(err)
	}
	claimed := *cl.ClaimedSignalSeq // 1 here
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/s78", state.SourceManualOperator, state.ReasonManualVerify, now)); err != nil {
		t.Fatal(err)
	}
	done, err := st.CompleteSuccess(ctx, p3RootActive, "/s78", claimed, now)
	if err != nil {
		t.Fatalf("complete success: %v", err)
	}
	if done.WorkState != state.WorkPending {
		t.Fatalf("partial success must return to PENDING, got %s", done.WorkState)
	}
	if done.LastVerifiedSignalSeq == nil || *done.LastVerifiedSignalSeq != claimed {
		t.Fatalf("partial success must record watermark=%d, got %v", claimed, done.LastVerifiedSignalSeq)
	}
	if len(done.PendingSourceSet) != 1 || done.PendingSourceSet[0] != state.SourceManualOperator {
		t.Fatalf("only the post-claim signal must remain pending, got %+v", done.PendingSourceSet)
	}
	if done.ClaimedSignalSeq != nil {
		t.Fatal("success must release the claim")
	}
}

func TestP3StaleClaimCannotComplete(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/stale", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	cl, err := p3Claim(t, st, ctx, p3RootActive, "/stale", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteSuccess(ctx, p3RootActive, "/stale", *cl.ClaimedSignalSeq+100, now); !errors.Is(err, postgres.ErrStaleClaim) {
		t.Fatalf("stale claim completion must be rejected, got %v", err)
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/stale")
	if wk.WorkState != state.WorkInFlight {
		t.Fatalf("stale completion must not mutate the row, got %s", wk.WorkState)
	}
}

func TestP3WatchAttributionSuccess(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	w, err := st.CreateWatch(ctx, p3HotWatch(p3RootActive, "/att", now.Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	// hint-only attempt must NOT touch the watch.
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/att", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	cl, _ := p3Claim(t, st, ctx, p3RootActive, "/att", now)
	if _, err := st.CompleteSuccess(ctx, p3RootActive, "/att", *cl.ClaimedSignalSeq, now); err != nil {
		t.Fatal(err)
	}
	wAfterHint, _ := st.GetWatch(ctx, p3RootActive, "/att")
	if wAfterHint.Version != w.Version || wAfterHint.LastSuccessAt != nil {
		t.Fatalf("hint-only success must not update the watch, got %+v", wAfterHint)
	}

	// claimed POLL_SCHEDULE success MUST update the watch.
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/att", state.SourcePollSchedule, state.ReasonPossibleChange, now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	cl2, _ := p3Claim(t, st, ctx, p3RootActive, "/att", now.Add(time.Second))
	if _, err := st.CompleteSuccess(ctx, p3RootActive, "/att", *cl2.ClaimedSignalSeq, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	wAfterPoll, _ := st.GetWatch(ctx, p3RootActive, "/att")
	if wAfterPoll.Version != wAfterHint.Version+2 || wAfterPoll.LastSuccessAt == nil {
		t.Fatalf("claimed poll success must update the watch (attempt-start + success), got %+v", wAfterPoll)
	}
}

// --- Failure -----------------------------------------------------------------

func TestP3FailureMappingAndRecoalesce(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()

	// transient -> RETRY_WAIT with provenance re-coalesced and oldest age kept.
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/f", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	cl, _ := p3Claim(t, st, ctx, p3RootActive, "/f", now)
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/f", state.SourceManualOperator, state.ReasonManualVerify, now)); err != nil {
		t.Fatal(err)
	}
	rb := now.Add(10 * time.Minute)
	failed, err := st.CompleteFailure(ctx, p3RootActive, "/f", *cl.ClaimedSignalSeq, state.ErrorTransientProvider, &rb, now)
	if err != nil {
		t.Fatalf("complete failure: %v", err)
	}
	if failed.WorkState != state.WorkRetryWait || failed.PendingNotBefore == nil || !failed.PendingNotBefore.Equal(rb) {
		t.Fatalf("transient must map to RETRY_WAIT with eligibility, got %+v", failed)
	}
	if len(failed.PendingSourceSet) != 2 {
		t.Fatalf("failure must re-coalesce claimed+pending provenance, got %+v", failed.PendingSourceSet)
	}
	if failed.PendingFirstSeenAt == nil || !failed.PendingFirstSeenAt.Equal(now) {
		t.Fatalf("failure must preserve the oldest first_seen, got %v", failed.PendingFirstSeenAt)
	}
	if failed.ConsecutiveFailures != 1 {
		t.Fatalf("provider failure must increment counters, got %d", failed.ConsecutiveFailures)
	}

	// RETRY_WAIT must not cancel/extend backoff on a later ordinary merge.
	merged, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/f", state.SourceMutationHint, state.ReasonPossibleChange, now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if merged.PendingNotBefore == nil || !merged.PendingNotBefore.Equal(rb) {
		t.Fatalf("retry backoff must be preserved across merges, got %v", merged.PendingNotBefore)
	}
	if merged.WorkState != state.WorkRetryWait {
		t.Fatalf("retry merge must not change state, got %s", merged.WorkState)
	}

	// auth -> BLOCKED.
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/b", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	clb, _ := p3Claim(t, st, ctx, p3RootActive, "/b", now)
	blocked, err := st.CompleteFailure(ctx, p3RootActive, "/b", *clb.ClaimedSignalSeq, state.ErrorAuthOrPermission, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.WorkState != state.WorkBlocked {
		t.Fatalf("auth must map to BLOCKED, got %s", blocked.WorkState)
	}
}

func TestP3RootInactiveFailureDoesNotCountProviderFailure(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/ri", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	cl, _ := p3Claim(t, st, ctx, p3RootActive, "/ri", now)
	out, err := st.CompleteFailure(ctx, p3RootActive, "/ri", *cl.ClaimedSignalSeq, state.ErrorRootInactive, nil, now)
	if err != nil {
		t.Fatalf("root-inactive failure: %v", err)
	}
	if out.WorkState != state.WorkSuspended {
		t.Fatalf("root inactive must map to SUSPENDED, got %s", out.WorkState)
	}
	if out.ConsecutiveFailures != 0 {
		t.Fatalf("root inactive must not count as provider failure, got %d", out.ConsecutiveFailures)
	}
}

// --- Budget defer / transitions ---------------------------------------------

func TestP3BudgetDeferAndTransitions(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/bd", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/bd")
	later := now.Add(30 * time.Minute)
	if err := st.BudgetDefer(ctx, p3RootActive, "/bd", wk.Version, later); err != nil {
		t.Fatalf("budget defer: %v", err)
	}
	after, _ := st.GetWork(ctx, p3RootActive, "/bd")
	if after.WorkState != state.WorkPending || after.AttemptCount != 0 || after.ConsecutiveFailures != 0 || after.LastErrorClass != nil {
		t.Fatalf("budget defer must not count attempt/failure, got %+v", after)
	}
	if after.PendingNotBefore == nil || !after.PendingNotBefore.Equal(later) {
		t.Fatalf("budget defer must move eligibility later, got %v", after.PendingNotBefore)
	}
}

// --- Recovery ----------------------------------------------------------------

func TestP3RecoveryIsDeterministicAndIdempotent(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/rec", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	cl, _ := p3Claim(t, st, ctx, p3RootActive, "/rec", now)
	// post-claim signal, then "crash" (no completion).
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/rec", state.SourceManualOperator, state.ReasonManualVerify, now)); err != nil {
		t.Fatal(err)
	}
	seqBefore := cl.SignalSeq + 1

	n, err := st.RecoverStaleInflight(ctx, p3RootActive, now)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected one recovered row, got %d", n)
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/rec")
	if wk.WorkState != state.WorkPending {
		t.Fatalf("ACTIVE root recovery must yield PENDING, got %s", wk.WorkState)
	}
	if wk.SignalSeq != seqBefore {
		t.Fatalf("recovery must preserve signal_seq: want %d got %d", seqBefore, wk.SignalSeq)
	}
	if len(wk.PendingSourceSet) != 2 {
		t.Fatalf("recovery must re-coalesce claimed+pending, got %+v", wk.PendingSourceSet)
	}
	if wk.ConsecutiveFailures != 0 {
		t.Fatalf("crash must not count as provider failure, got %d", wk.ConsecutiveFailures)
	}

	// Idempotent: second run is a no-op.
	n2, err := st.RecoverStaleInflight(ctx, p3RootActive, now)
	if err != nil || n2 != 0 {
		t.Fatalf("second recovery must be a no-op, got n=%d err=%v", n2, err)
	}
}

func TestP3RecoveryOnInactiveRootSuspends(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/rec2", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	if _, err := p3Claim(t, st, ctx, p3RootActive, "/rec2", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE index_root SET lifecycle_state='DEPRECATED' WHERE root_id=$1`, p3RootActive); err != nil {
		t.Fatal(err)
	}
	n, err := st.RecoverStaleInflight(ctx, p3RootActive, now)
	if err != nil || n != 1 {
		t.Fatalf("recover inactive: n=%d err=%v", n, err)
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/rec2")
	if wk.WorkState != state.WorkSuspended {
		t.Fatalf("non-ACTIVE root recovery must yield SUSPENDED, got %s", wk.WorkState)
	}
}

// --- DB constraints ----------------------------------------------------------

func TestP3DBConstraintsRejectInvalidRows(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()

	// PENDING with an empty pending bucket is forbidden by the DB.
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_dirty_scope_work (root_id, scope_key, work_state, signal_seq,
			pending_source_set, pending_reason_set, last_seen_at)
		VALUES ($1,'/bad','PENDING',1,'{}','{}',$2)`, p3RootActive, now); err == nil {
		t.Fatal("DB must reject PENDING with empty pending bucket")
	}

	// IN_FLIGHT without a claimed group is forbidden.
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_dirty_scope_work (root_id, scope_key, work_state, signal_seq,
			pending_source_set, pending_reason_set, last_seen_at)
		VALUES ($1,'/bad2','IN_FLIGHT',1,'{}','{}',$2)`, p3RootActive, now); err == nil {
		t.Fatal("DB must reject IN_FLIGHT without a claimed group")
	}

	// RETRY_WAIT without pending_not_before is forbidden.
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_dirty_scope_work (root_id, scope_key, work_state, signal_seq,
			pending_source_set, pending_reason_set, pending_first_seen_at, last_seen_at)
		VALUES ($1,'/bad3','RETRY_WAIT',1,'{POLL_SCHEDULE}','{POSSIBLE_CHANGE}',$2,$2)`, p3RootActive, now); err == nil {
		t.Fatal("DB must reject RETRY_WAIT without pending_not_before")
	}

	// Invalid scope key is forbidden by the DB.
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_scope_watch_state (root_id, scope_key, watch_state, cadence_class,
			priority_class)
		VALUES ($1,'/a/','COLD','COLD_OFF','LOW')`, p3RootActive); err == nil {
		t.Fatal("DB must reject a trailing-slash scope key")
	}
}
