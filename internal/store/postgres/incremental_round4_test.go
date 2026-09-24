package postgres_test

import (
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// TestP3BlockedKeepsPostClaimBarrier: AUTH failure -> BLOCKED -> RepairBlocked
// must preserve the post-claim future barrier, and an early claim must fail.
func TestP3BlockedKeepsPostClaimBarrier(t *testing.T) {
	st, ctx := p3Store(t)
	t0 := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/auth-bar", state.SourceMutationHint, state.ReasonPossibleChange, t0)); err != nil {
		t.Fatal(err)
	}
	cl, err := p3Claim(t, st, ctx, p3RootActive, "/auth-bar", t0)
	if err != nil {
		t.Fatal(err)
	}
	future := t0.Add(60 * time.Minute)
	post := p3Signal(p3RootActive, "/auth-bar", state.SourceManualOperator, state.ReasonManualVerify, t0)
	post.NotBefore = &future
	if _, err := st.MergeSignal(ctx, post); err != nil {
		t.Fatal(err)
	}
	blocked, err := st.CompleteFailure(ctx, p3RootActive, "/auth-bar", *cl.ClaimedSignalSeq, state.ErrorAuthOrPermission, nil, t0)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.WorkState != state.WorkBlocked {
		t.Fatalf("expected BLOCKED, got %s", blocked.WorkState)
	}
	if blocked.PendingNotBefore == nil || !blocked.PendingNotBefore.Equal(future) {
		t.Fatalf("BLOCKED must preserve the post-claim barrier %v, got %v", future, blocked.PendingNotBefore)
	}
	pending, err := st.RepairBlocked(ctx, p3RootActive, "/auth-bar", blocked.Version, t0)
	if err != nil {
		t.Fatal(err)
	}
	if pending.WorkState != state.WorkPending || pending.PendingNotBefore == nil || !pending.PendingNotBefore.Equal(future) {
		t.Fatalf("RepairBlocked must keep the barrier, got %+v", pending)
	}
	if _, err := st.ClaimWork(ctx, p3RootActive, "/auth-bar", pending.Version, t0); err == nil {
		t.Fatal("claiming before the preserved barrier must fail")
	}
}

// TestP3SuspendedKeepsPostClaimBarrier: ROOT_INACTIVE -> SUSPENDED ->
// ResumeSuspended must preserve the post-claim future barrier.
func TestP3SuspendedKeepsPostClaimBarrier(t *testing.T) {
	st, ctx := p3Store(t)
	t0 := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/sus-bar", state.SourceMutationHint, state.ReasonPossibleChange, t0)); err != nil {
		t.Fatal(err)
	}
	cl, err := p3Claim(t, st, ctx, p3RootActive, "/sus-bar", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE index_root SET lifecycle_state='DEPRECATED' WHERE root_id=$1`, p3RootActive); err != nil {
		t.Fatal(err)
	}
	future := t0.Add(60 * time.Minute)
	post := p3Signal(p3RootActive, "/sus-bar", state.SourceManualOperator, state.ReasonManualVerify, t0)
	post.NotBefore = &future
	if _, err := st.MergeSignal(ctx, post); err != nil {
		t.Fatal(err)
	}
	susp, err := st.CompleteFailure(ctx, p3RootActive, "/sus-bar", *cl.ClaimedSignalSeq, state.ErrorRootInactive, nil, t0)
	if err != nil {
		t.Fatal(err)
	}
	if susp.WorkState != state.WorkSuspended {
		t.Fatalf("expected SUSPENDED, got %s", susp.WorkState)
	}
	if susp.PendingNotBefore == nil || !susp.PendingNotBefore.Equal(future) {
		t.Fatalf("SUSPENDED must preserve the post-claim barrier %v, got %v", future, susp.PendingNotBefore)
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE index_root SET lifecycle_state='ACTIVE' WHERE root_id=$1`, p3RootActive); err != nil {
		t.Fatal(err)
	}
	resumed, err := st.ResumeSuspended(ctx, p3RootActive, "/sus-bar", susp.Version, t0)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.WorkState != state.WorkPending || resumed.PendingNotBefore == nil || !resumed.PendingNotBefore.Equal(future) {
		t.Fatalf("ResumeSuspended must keep the barrier, got %+v", resumed)
	}
	if _, err := st.ClaimWork(ctx, p3RootActive, "/sus-bar", resumed.Version, t0); err == nil {
		t.Fatal("claiming before the preserved barrier must fail")
	}
}
