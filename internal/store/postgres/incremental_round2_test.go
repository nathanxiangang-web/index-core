package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// seedWork inserts a DirtyScopeWork row in an arbitrary state (constraints must hold).
func seedWork(t *testing.T, st *postgres.Store, rootID, scopeKey, workState string, notBefore *time.Time, at time.Time) {
	t.Helper()
	ctx := ctxBG()
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_dirty_scope_work (root_id, scope_key, work_state, signal_seq,
			pending_source_set, pending_reason_set, pending_priority, pending_first_seen_at,
			pending_not_before, last_seen_at)
		VALUES ($1,$2,$3,1,'{MUTATION_HINT}','{POSSIBLE_CHANGE}','NORMAL',$4,$5,$4)`,
		rootID, scopeKey, workState, at, notBefore); err != nil {
		t.Fatalf("seed work %s: %v", workState, err)
	}
}

// TestP3RecoveryNullClaimEligibilityNotDelayed proves a claimed signal with no
// barrier (claimed_not_before NULL) stays immediately runnable after recovery,
// even if a post-claim signal carries a future not_before.
func TestP3RecoveryNullClaimEligibilityNotDelayed(t *testing.T) {
	st, ctx := p3Store(t)
	t0 := p3Time()
	// first (claimed) signal has NO barrier.
	first := p3Signal(p3RootActive, "/null-elig", state.SourceMutationHint, state.ReasonPossibleChange, t0)
	first.NotBefore = nil
	if _, err := st.MergeSignal(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := p3Claim(t, st, ctx, p3RootActive, "/null-elig", t0); err != nil {
		t.Fatal(err)
	}
	// post-claim signal with a FUTURE barrier.
	future := t0.Add(60 * time.Minute)
	second := p3Signal(p3RootActive, "/null-elig", state.SourceManualOperator, state.ReasonManualVerify, t0)
	second.NotBefore = &future
	if _, err := st.MergeSignal(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecoverStaleInflight(ctx, p3RootActive, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/null-elig")
	if wk.WorkState != state.WorkPending {
		t.Fatalf("expected PENDING, got %s", wk.WorkState)
	}
	if wk.PendingNotBefore != nil {
		t.Fatalf("a no-barrier claim must stay immediately runnable, got %v", wk.PendingNotBefore)
	}
}

// TestP3LifecycleGateOnRunnableTransitions proves RetryReady / ResumeSuspended /
// RepairBlocked fail closed when the root is not ACTIVE.
func TestP3LifecycleGateOnRunnableTransitions(t *testing.T) {
	cases := []struct {
		name      string
		workState string
		notBefore *time.Time
		call      func(*postgres.Store, string) error
	}{
		{"RetryReady", "RETRY_WAIT", ptrTime(p3Time().Add(time.Minute)), func(st *postgres.Store, key string) error {
			_, err := st.RetryReady(ctxBG(), p3RootActive, key, 1, p3Time().Add(time.Hour))
			return err
		}},
		{"ResumeSuspended", "SUSPENDED", nil, func(st *postgres.Store, key string) error {
			_, err := st.ResumeSuspended(ctxBG(), p3RootActive, key, 1, p3Time())
			return err
		}},
		{"RepairBlocked", "BLOCKED", nil, func(st *postgres.Store, key string) error {
			_, err := st.RepairBlocked(ctxBG(), p3RootActive, key, 1, p3Time())
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ctx := p3Store(t)
			key := "/gate-" + strings.ToLower(tc.name)
			seedWork(t, st, p3RootActive, key, tc.workState, tc.notBefore, p3Time())
			if _, err := st.Pool().Exec(ctx,
				`UPDATE index_root SET lifecycle_state='DEPRECATED' WHERE root_id=$1`, p3RootActive); err != nil {
				t.Fatal(err)
			}
			if err := tc.call(st, key); !errors.Is(err, postgres.ErrStateCASConflict) {
				t.Fatalf("inactive root must fail closed, got %v", err)
			}
			wk, _ := st.GetWork(ctx, p3RootActive, key)
			if string(wk.WorkState) != tc.workState {
				t.Fatalf("rejected transition must not mutate state, got %s", wk.WorkState)
			}
		})
	}
}

// TestP3RunnableTransitionsSucceedOnActiveRoot covers the positive paths.
func TestP3RunnableTransitionsSucceedOnActiveRoot(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()

	seedWork(t, st, p3RootActive, "/ok-retry", "RETRY_WAIT", ptrTime(now.Add(-time.Minute)), now)
	if out, err := st.RetryReady(ctx, p3RootActive, "/ok-retry", 1, now); err != nil || out.WorkState != state.WorkPending {
		t.Fatalf("RetryReady: %v %+v", err, out)
	}

	seedWork(t, st, p3RootActive, "/ok-susp", "SUSPENDED", nil, now)
	if out, err := st.ResumeSuspended(ctx, p3RootActive, "/ok-susp", 1, now); err != nil || out.WorkState != state.WorkPending {
		t.Fatalf("ResumeSuspended: %v %+v", err, out)
	}

	seedWork(t, st, p3RootActive, "/ok-block", "BLOCKED", nil, now)
	if out, err := st.RepairBlocked(ctx, p3RootActive, "/ok-block", 1, now); err != nil || out.WorkState != state.WorkPending {
		t.Fatalf("RepairBlocked: %v %+v", err, out)
	}
}

// TestP3CompleteFailureKeepsLaterBarrier proves failure backoff never moves an
// existing post-claim barrier earlier.
func TestP3CompleteFailureKeepsLaterBarrier(t *testing.T) {
	st, ctx := p3Store(t)
	t0 := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/barrier", state.SourceMutationHint, state.ReasonPossibleChange, t0)); err != nil {
		t.Fatal(err)
	}
	cl, err := p3Claim(t, st, ctx, p3RootActive, "/barrier", t0)
	if err != nil {
		t.Fatal(err)
	}
	future := t0.Add(60 * time.Minute)
	post := p3Signal(p3RootActive, "/barrier", state.SourceManualOperator, state.ReasonManualVerify, t0)
	post.NotBefore = &future
	if _, err := st.MergeSignal(ctx, post); err != nil {
		t.Fatal(err)
	}
	retry := t0.Add(10 * time.Minute)
	out, err := st.CompleteFailure(ctx, p3RootActive, "/barrier", *cl.ClaimedSignalSeq, state.ErrorTransientProvider, &retry, t0)
	if err != nil {
		t.Fatal(err)
	}
	if out.PendingNotBefore == nil || !out.PendingNotBefore.Equal(future) {
		t.Fatalf("failure must keep the later barrier %v, got %v", future, out.PendingNotBefore)
	}
}

// TestP3LastSeenAtNeverRegresses proves same-epoch merges keep the latest signal.
func TestP3LastSeenAtNeverRegresses(t *testing.T) {
	st, ctx := p3Store(t)
	t0 := p3Time()
	later := t0.Add(10 * time.Minute)
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/ls", state.SourceMutationHint, state.ReasonPossibleChange, later)); err != nil {
		t.Fatal(err)
	}
	// An older signal must not move last_seen_at backwards.
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/ls", state.SourceManualOperator, state.ReasonManualVerify, t0)); err != nil {
		t.Fatal(err)
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/ls")
	if !wk.LastSeenAt.Equal(later) {
		t.Fatalf("last_seen_at must not regress, got %v want %v", wk.LastSeenAt, later)
	}
}

// TestP3DBVerifiedWatermarkLowerBound covers the missing >= 1 constraint.
func TestP3DBVerifiedWatermarkLowerBound(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_dirty_scope_work (root_id, scope_key, work_state, signal_seq,
			last_verified_signal_seq, pending_source_set, pending_reason_set, last_seen_at)
		VALUES ($1,'/wm','VERIFIED',1,0,'{}','{}',$2)`, p3RootActive, now); err == nil {
		t.Fatal("DB must reject last_verified_signal_seq < 1")
	}
}

// TestP3EmitDuePollVsClaimNoDeadlock runs the two multi-row transactions
// concurrently; the frozen Root -> Watch -> Work order must avoid deadlock.
func TestP3EmitDuePollVsClaimNoDeadlock(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	w, err := st.CreateWatch(ctx, p3HotWatch(p3RootActive, "/dl", now.Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/dl", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := st.EmitDuePoll(ctx, p3RootActive, "/dl", w.Version, now)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := p3Claim(t, st, ctx, p3RootActive, "/dl", now)
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "deadlock") {
			t.Fatalf("deadlock detected: %v", err)
		}
	}
}

// TestP3TransitionRootLifecycleRaceNoDeadlock runs a root lifecycle change
// concurrently with a claim; root lifecycle must serialize with state changes.
func TestP3TransitionRootLifecycleRaceNoDeadlock(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/lifecycle", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := p3Claim(t, st, ctx, p3RootActive, "/lifecycle", now)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := st.TransitionRootLifecycle(ctx, p3RootActive, domain.RootDeprecated)
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "deadlock") {
			t.Fatalf("deadlock detected: %v", err)
		}
	}
}

// TestP3PreMigrationUpgradePreservesData proves upgrading from a pre-0005
// schema keeps existing data.
func TestP3PreMigrationUpgradePreservesData(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := ctxBG()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO index_root(root_id, scope_descriptor, lifecycle_state)
		 VALUES ($1, '{}'::jsonb, 'ACTIVE')`, p3RootActive); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO index_root_adapter_config(root_id, collector_kind, config)
		 VALUES ($1, 'rclone', '{}'::jsonb)`, p3RootActive); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	// Simulate a pre-0005 database.
	if _, err := pool.Exec(ctx,
		`DROP TABLE index_dirty_scope_work; DROP TABLE index_scope_watch_state;
		 DELETE FROM schema_migrations WHERE version LIKE '0005%'`); err != nil {
		t.Fatalf("simulate pre-0005: %v", err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("upgrade migrate: %v", err)
	}
	var roots, cfgs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_root WHERE root_id=$1`, p3RootActive).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_root_adapter_config WHERE root_id=$1`, p3RootActive).Scan(&cfgs); err != nil {
		t.Fatal(err)
	}
	if roots != 1 || cfgs != 1 {
		t.Fatalf("upgrade must preserve existing data, roots=%d cfgs=%d", roots, cfgs)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
func ctxBG() context.Context         { return context.Background() }
