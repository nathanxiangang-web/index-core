package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// TestP3ClaimStaleVersionCAS proves ClaimWork enforces the caller's expected
// work version: a stale version fails closed with no Work/Watch mutation.
func TestP3ClaimStaleVersionCAS(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/cas", state.SourceMutationHint, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWork(ctx, p3RootActive, "/cas")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimWork(ctx, p3RootActive, "/cas", w.Version+99, now); !errors.Is(err, postgres.ErrStateCASConflict) {
		t.Fatalf("stale work version must conflict, got %v", err)
	}
	after, _ := st.GetWork(ctx, p3RootActive, "/cas")
	if after.WorkState != state.WorkPending || after.Version != w.Version || after.AttemptCount != 0 {
		t.Fatalf("stale claim must not mutate the row, got %+v", after)
	}
	ok, err := st.ClaimWork(ctx, p3RootActive, "/cas", w.Version, now)
	if err != nil || ok.WorkState != state.WorkInFlight {
		t.Fatalf("claim with the correct version must succeed, got %v %+v", err, ok)
	}
}

// TestP3ClaimStaleVersionLeavesWatchUntouched proves a stale claim does not bump
// the matching watch (POLL_SCHEDULE path).
func TestP3ClaimStaleVersionLeavesWatchUntouched(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	w, err := st.CreateWatch(ctx, p3HotWatch(p3RootActive, "/cas-w", now.Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/cas-w", state.SourcePollSchedule, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	wk, _ := st.GetWork(ctx, p3RootActive, "/cas-w")
	if _, err := st.ClaimWork(ctx, p3RootActive, "/cas-w", wk.Version+7, now); !errors.Is(err, postgres.ErrStateCASConflict) {
		t.Fatalf("stale claim must conflict, got %v", err)
	}
	after, _ := st.GetWatch(ctx, p3RootActive, "/cas-w")
	if after.Version != w.Version || after.LastAttemptStartedAt != nil {
		t.Fatalf("stale claim must not touch the watch, got %+v", after)
	}
}

// TestP3NewEpochLastSeenNotInherited proves a VERIFIED -> new-epoch merge resets
// last_seen_at to the triggering signal (never inherits the previous epoch).
func TestP3NewEpochLastSeenNotInherited(t *testing.T) {
	st, ctx := p3Store(t)
	old := p3Time().Add(10 * time.Minute)
	newer := p3Time() // earlier wall time, but a NEW epoch

	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/ne", state.SourceMutationHint, state.ReasonPossibleChange, old)); err != nil {
		t.Fatal(err)
	}
	cl, err := p3Claim(t, st, ctx, p3RootActive, "/ne", old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteSuccess(ctx, p3RootActive, "/ne", *cl.ClaimedSignalSeq, old); err != nil {
		t.Fatal(err)
	}
	merged, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/ne", state.SourceManualOperator, state.ReasonManualVerify, newer))
	if err != nil {
		t.Fatal(err)
	}
	if !merged.LastSeenAt.Equal(newer) {
		t.Fatalf("new epoch must set last_seen_at to the new signal (%v), got %v", newer, merged.LastSeenAt)
	}
	if merged.WorkState != state.WorkPending {
		t.Fatalf("new epoch must open PENDING, got %s", merged.WorkState)
	}
}
