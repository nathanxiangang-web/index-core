package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// TestP3DBFailClosedCompleteness covers the Round-1 DB constraint gaps: a
// closed-enum last_error_class and no source-only pending/claimed half states.
func TestP3DBFailClosedCompleteness(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()

	// Invalid last_error_class is rejected.
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_dirty_scope_work (root_id, scope_key, work_state, signal_seq,
			pending_source_set, pending_reason_set, pending_priority, pending_first_seen_at,
			last_seen_at, last_error_class)
		VALUES ($1,'/err','PENDING',1,'{MUTATION_HINT}','{POSSIBLE_CHANGE}','NORMAL',$2,$2,'BOGUS')`,
		p3RootActive, now); err == nil {
		t.Fatal("DB must reject a non-enum last_error_class")
	}

	// Pending bucket with a source but no reason is rejected.
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_dirty_scope_work (root_id, scope_key, work_state, signal_seq,
			pending_source_set, pending_reason_set, pending_priority, pending_first_seen_at, last_seen_at)
		VALUES ($1,'/half','PENDING',1,'{MUTATION_HINT}','{}','NORMAL',$2,$2)`,
		p3RootActive, now); err == nil {
		t.Fatal("DB must reject a pending-source-only half state (no reason)")
	}
	// Pending bucket with a source but no priority is rejected.
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_dirty_scope_work (root_id, scope_key, work_state, signal_seq,
			pending_source_set, pending_reason_set, pending_priority, pending_first_seen_at, last_seen_at)
		VALUES ($1,'/half2','PENDING',1,'{MUTATION_HINT}','{POSSIBLE_CHANGE}',NULL,$2,$2)`,
		p3RootActive, now); err == nil {
		t.Fatal("DB must reject a pending-source-only half state (no priority)")
	}
	// IN_FLIGHT with an empty claimed reason set is rejected.
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_dirty_scope_work (root_id, scope_key, work_state, signal_seq,
			claimed_signal_seq, claimed_source_set, claimed_reason_set, claimed_priority, claimed_first_seen_at,
			pending_source_set, pending_reason_set, last_seen_at)
		VALUES ($1,'/half3','IN_FLIGHT',1,1,'{POLL_SCHEDULE}','{}','NORMAL',$2,'{}','{}',$2)`,
		p3RootActive, now); err == nil {
		t.Fatal("DB must reject IN_FLIGHT with an empty claimed reason set")
	}
}

// TestP3ReadsAreNormalized proves Store reads return deterministic normalized
// sets, not raw DB array order / duplicates (P3 Sec 6).
func TestP3ReadsAreNormalized(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	// Insert deliberately unsorted + duplicated sets via raw SQL.
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO index_dirty_scope_work (root_id, scope_key, work_state, signal_seq,
			pending_source_set, pending_reason_set, pending_priority, pending_first_seen_at,
			last_seen_at)
		VALUES ($1,'/norm','PENDING',1,
			'{MUTATION_HINT,POLL_SCHEDULE,MUTATION_HINT}',
			'{RETRY,POSSIBLE_CHANGE,RETRY}','NORMAL',$2,$2)`, p3RootActive, now); err != nil {
		t.Fatalf("seed unsorted row: %v", err)
	}
	wk, err := st.GetWork(ctx, p3RootActive, "/norm")
	if err != nil {
		t.Fatal(err)
	}
	wantS := []state.TriggerSource{state.SourceMutationHint, state.SourcePollSchedule}
	if len(wk.PendingSourceSet) != len(wantS) {
		t.Fatalf("read must dedupe, got %v", wk.PendingSourceSet)
	}
	for i, w := range wantS {
		if wk.PendingSourceSet[i] != w {
			t.Fatalf("read must sort deterministically, got %v", wk.PendingSourceSet)
		}
	}
	if len(wk.PendingReasonSet) != 2 || wk.PendingReasonSet[0] != state.ReasonPossibleChange || wk.PendingReasonSet[1] != state.ReasonRetry {
		t.Fatalf("reason set must be sorted/deduped, got %v", wk.PendingReasonSet)
	}
}

// TestP3CompleteFailureRejectsImmediateRetry enforces no-tight-retry.
func TestP3CompleteFailureRejectsImmediateRetry(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/nr", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	cl, err := p3Claim(t, st, ctx, p3RootActive, "/nr", now)
	if err != nil {
		t.Fatal(err)
	}
	immediate := now
	if _, err := st.CompleteFailure(ctx, p3RootActive, "/nr", *cl.ClaimedSignalSeq, state.ErrorTransientProvider, &immediate, now); err == nil {
		t.Fatal("immediate retry eligibility must be rejected")
	}
	past := now.Add(-time.Minute)
	if _, err := st.CompleteFailure(ctx, p3RootActive, "/nr", *cl.ClaimedSignalSeq, state.ErrorTransientProvider, &past, now); err == nil {
		t.Fatal("past retry eligibility must be rejected")
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/nr")
	if wk.WorkState != state.WorkInFlight {
		t.Fatalf("rejected retry must not mutate the row, got %s", wk.WorkState)
	}
}

// TestP3RecoveryPreservesEarliestEligibility proves a post-claim future
// not_before does not delay an already-due claimed signal after recovery.
func TestP3RecoveryPreservesEarliestEligibility(t *testing.T) {
	st, ctx := p3Store(t)
	t0 := p3Time()
	// claimed signal is due at t0.
	first := p3Signal(p3RootActive, "/elig", state.SourceMutationHint, state.ReasonPossibleChange, t0)
	first.NotBefore = &t0
	if _, err := st.MergeSignal(ctx, first); err != nil {
		t.Fatal(err)
	}
	cl, err := p3Claim(t, st, ctx, p3RootActive, "/elig", t0)
	if err != nil {
		t.Fatal(err)
	}
	_ = cl
	// post-claim signal with a FUTURE eligibility.
	future := t0.Add(60 * time.Minute)
	second := p3Signal(p3RootActive, "/elig", state.SourceManualOperator, state.ReasonManualVerify, t0)
	second.NotBefore = &future
	if _, err := st.MergeSignal(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecoverStaleInflight(ctx, p3RootActive, t0.Add(time.Minute)); err != nil {
		t.Fatalf("recover: %v", err)
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/elig")
	if wk.WorkState != state.WorkPending {
		t.Fatalf("expected PENDING, got %s", wk.WorkState)
	}
	if wk.PendingNotBefore == nil || !wk.PendingNotBefore.Equal(t0) {
		t.Fatalf("recovery must keep the earliest (already-due) eligibility, got %v", wk.PendingNotBefore)
	}
}

// TestP3InactiveClaimKeepsEligibility proves suspending on an inactive root does
// not drop pending_not_before.
func TestP3InactiveClaimKeepsEligibility(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	future := now.Add(30 * time.Minute)
	sig := p3Signal(p3RootActive, "/suspend-elig", state.SourceMutationHint, state.ReasonPossibleChange, now)
	sig.NotBefore = &future
	if _, err := st.MergeSignal(ctx, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE index_root SET lifecycle_state='DEPRECATED' WHERE root_id=$1`, p3RootActive); err != nil {
		t.Fatal(err)
	}
	if _, err := p3Claim(t, st, ctx, p3RootActive, "/suspend-elig", now); err == nil {
		t.Fatal("claim on inactive root must fail closed")
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/suspend-elig")
	if wk.WorkState != state.WorkSuspended {
		t.Fatalf("expected SUSPENDED, got %s", wk.WorkState)
	}
	if wk.PendingNotBefore == nil || !wk.PendingNotBefore.Equal(future) {
		t.Fatalf("suspension must preserve pending_not_before, got %v", wk.PendingNotBefore)
	}
}

var _ = errors.Is
