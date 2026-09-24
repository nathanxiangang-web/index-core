package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

const p4RootActive2 = "00000000-0000-0000-0000-00000000a003"

func p4Signal(rootID, scopeKey string, p state.Priority, src state.TriggerSource, reason state.TriggerReason, seenAt time.Time, notBefore *time.Time) state.DirtySignal {
	return state.DirtySignal{
		RootID: rootID, ScopeKey: scopeKey, Source: src, Reason: reason,
		Priority: p, SeenAt: seenAt, NotBefore: notBefore,
	}
}

func p4AddActiveRoot(t *testing.T, st *postgres.Store, ctx context.Context, id string) {
	t.Helper()
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO index_root(root_id, scope_descriptor, lifecycle_state) VALUES ($1, '{}'::jsonb, 'ACTIVE')`,
		id); err != nil {
		t.Fatalf("seed second active root: %v", err)
	}
}

// p4SelectAndPark selects one eligible row and parks it as VERIFIED so the next
// selection sees the following candidate.
func p4SelectAndPark(t *testing.T, st *postgres.Store, ctx context.Context, now time.Time) state.DirtyScopeWork {
	t.Helper()
	wk, found, err := st.NextEligiblePendingWork(ctx, now)
	if err != nil {
		t.Fatalf("select eligible work: %v", err)
	}
	if !found {
		t.Fatalf("expected an eligible row")
	}
	if _, err := st.Pool().Exec(ctx, `
		UPDATE index_dirty_scope_work
		   SET work_state='VERIFIED', pending_source_set='{}'::text[], pending_reason_set='{}'::text[],
		       pending_priority=NULL, pending_first_seen_at=NULL, pending_not_before=NULL
		 WHERE root_id=$1::uuid AND scope_key=$2`, wk.RootID, wk.ScopeKey); err != nil {
		t.Fatalf("park selected work: %v", err)
	}
	return wk
}

func TestP4SelectorNoEligibleWork(t *testing.T) {
	st, ctx := p3Store(t)
	_, found, err := st.NextEligiblePendingWork(ctx, p3Time())
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if found {
		t.Fatal("empty table must yield found=false")
	}
}

func TestP4SelectorExcludesFutureNotBefore(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	future := now.Add(2 * time.Hour)
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/later", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now, &future)); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, found, err := st.NextEligiblePendingWork(ctx, now); err != nil || found {
		t.Fatalf("future pending_not_before must be excluded, found=%v err=%v", found, err)
	}
	wk, found, err := st.NextEligiblePendingWork(ctx, future.Add(time.Second))
	if err != nil || !found {
		t.Fatalf("work must become eligible once due, found=%v err=%v", found, err)
	}
	if wk.ScopeKey != "/later" {
		t.Fatalf("unexpected scope %q", wk.ScopeKey)
	}
}

func TestP4SelectorExcludesInactiveRoot(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/a", state.PriorityUrgent,
		state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if _, found, err := st.NextEligiblePendingWork(ctx, now); err != nil || !found {
		t.Fatalf("ACTIVE root PENDING must be eligible, found=%v err=%v", found, err)
	}
	if _, err := st.TransitionRootLifecycle(ctx, p3RootActive, "DEPRECATED"); err != nil {
		t.Fatalf("deprecate root: %v", err)
	}
	if _, found, err := st.NextEligiblePendingWork(ctx, now); err != nil || found {
		t.Fatalf("non-ACTIVE root must be excluded, found=%v err=%v", found, err)
	}
}

func TestP4SelectorExcludesNonPendingStates(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()

	// RETRY_WAIT requires a future eligibility instant.
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/retry", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		UPDATE index_dirty_scope_work SET work_state='RETRY_WAIT', pending_not_before=$2
		 WHERE root_id=$1::uuid AND scope_key='/retry'`, p3RootActive, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// BLOCKED / SUSPENDED keep a non-empty pending bucket.
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/blocked", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/suspended", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
		t.Fatal(err)
	}
	// VERIFIED must have an empty pending bucket.
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/verified", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		UPDATE index_dirty_scope_work SET work_state='BLOCKED' WHERE root_id=$1::uuid AND scope_key='/blocked'`,
		p3RootActive); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		UPDATE index_dirty_scope_work SET work_state='SUSPENDED' WHERE root_id=$1::uuid AND scope_key='/suspended'`,
		p3RootActive); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		UPDATE index_dirty_scope_work
		   SET work_state='VERIFIED', pending_source_set='{}'::text[], pending_reason_set='{}'::text[],
		       pending_priority=NULL, pending_first_seen_at=NULL, pending_not_before=NULL
		 WHERE root_id=$1::uuid AND scope_key='/verified'`, p3RootActive); err != nil {
		t.Fatal(err)
	}

	if _, found, err := st.NextEligiblePendingWork(ctx, now); err != nil || found {
		t.Fatalf("non-PENDING states must be excluded, found=%v err=%v", found, err)
	}

	// Only an eligible PENDING row is selected, and it is the intended one.
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/pending", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
		t.Fatal(err)
	}
	wk, found, err := st.NextEligiblePendingWork(ctx, now)
	if err != nil || !found {
		t.Fatalf("PENDING row must be eligible, found=%v err=%v", found, err)
	}
	if wk.WorkState != state.WorkPending || wk.ScopeKey != "/pending" {
		t.Fatalf("unexpected selected row %+v", wk)
	}
}

func TestP4SelectorDeterministicPriorityOrder(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	for _, s := range []struct {
		scope string
		p     state.Priority
	}{
		{"/low", state.PriorityLow},
		{"/normal", state.PriorityNormal},
		{"/urgent", state.PriorityUrgent},
		{"/high", state.PriorityHigh},
	} {
		if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, s.scope, s.p,
			state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
			t.Fatalf("merge %s: %v", s.scope, err)
		}
	}

	var got []string
	for i := 0; i < 4; i++ {
		got = append(got, p4SelectAndPark(t, st, ctx, now).ScopeKey)
	}
	want := []string{"/urgent", "/high", "/normal", "/low"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("priority order = %v, want %v", got, want)
		}
	}
}

func TestP4SelectorOldestFirstSeenWins(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	// /z is older than /a, so first_seen must win over lexical scope order.
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/z", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now.Add(-2*time.Hour), nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/a", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
		t.Fatal(err)
	}
	if got := p4SelectAndPark(t, st, ctx, now).ScopeKey; got != "/z" {
		t.Fatalf("oldest pending_first_seen_at must win, got %q", got)
	}
	if got := p4SelectAndPark(t, st, ctx, now).ScopeKey; got != "/a" {
		t.Fatalf("second selection = %q, want /a", got)
	}
}

func TestP4SelectorFinalTieDeterministic(t *testing.T) {
	st, ctx := p3Store(t)
	p4AddActiveRoot(t, st, ctx, p4RootActive2)
	now := p3Time()
	// Same priority and same first_seen: root_id then scope_key break the tie.
	if _, err := st.MergeSignal(ctx, p4Signal(p4RootActive2, "/a", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/zz", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
		t.Fatal(err)
	}
	first := p4SelectAndPark(t, st, ctx, now)
	second := p4SelectAndPark(t, st, ctx, now)
	// p3RootActive ("...a001") sorts before p4RootActive2 ("...a003").
	if first.RootID != p3RootActive || second.RootID != p4RootActive2 {
		t.Fatalf("final tie order = [%s %s], want [%s %s]",
			first.RootID, second.RootID, p3RootActive, p4RootActive2)
	}
}

func TestP4SelectorReadOnlyAndVersionFeedsClaim(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, "/a", state.PriorityNormal,
		state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
		t.Fatal(err)
	}
	before, err := st.GetWork(ctx, p3RootActive, "/a")
	if err != nil {
		t.Fatal(err)
	}

	selected, found, err := st.NextEligiblePendingWork(ctx, now)
	if err != nil || !found {
		t.Fatalf("select: found=%v err=%v", found, err)
	}
	if selected.Version != before.Version {
		t.Fatalf("selector must return the persisted version %d, got %d", before.Version, selected.Version)
	}
	after, err := st.GetWork(ctx, p3RootActive, "/a")
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != before.Version || after.WorkState != state.WorkPending {
		t.Fatalf("selection must be read-only, work changed to %+v", after)
	}

	claimed, err := st.ClaimWork(ctx, selected.RootID, selected.ScopeKey, selected.Version, now)
	if err != nil {
		t.Fatalf("selected version must feed ClaimWork: %v", err)
	}
	if claimed.WorkState != state.WorkInFlight || claimed.ClaimedSignalSeq == nil {
		t.Fatalf("claim result %+v", claimed)
	}
}
