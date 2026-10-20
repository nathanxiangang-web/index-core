package postgres_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func TestDiscoveryCompletionAtomicRollbackAndStaleClaim(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	w, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/cloud", state.SourceManualOperator, state.ReasonManualVerify, now))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := st.ClaimWork(ctx, w.RootID, w.ScopeKey, w.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	// Force a failure on the parent write, AFTER a child was inserted in the txn.
	_, err = st.Pool().Exec(ctx, `CREATE FUNCTION fail_discovery_parent() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.scope_key='/cloud' AND NEW.work_state='VERIFIED' THEN RAISE EXCEPTION 'completion failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_discovery BEFORE UPDATE ON index_dirty_scope_work FOR EACH ROW EXECUTE FUNCTION fail_discovery_parent()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteSuccessWithPrioritizedChildren(ctx, w.RootID, w.ScopeKey, *claim.ClaimedSignalSeq, []string{"/cloud/a"}, "/cloud", now); err == nil {
		t.Fatal("expected rollback")
	}
	var parent string
	var children int
	if err := st.Pool().QueryRow(ctx, `SELECT work_state FROM index_dirty_scope_work WHERE scope_key='/cloud'`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_dirty_scope_work WHERE scope_key='/cloud/a'`).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if parent != "IN_FLIGHT" || children != 0 {
		t.Fatalf("partial commit %s %d", parent, children)
	}
	if _, err := st.Pool().Exec(ctx, `DROP TRIGGER fail_discovery ON index_dirty_scope_work; DROP FUNCTION fail_discovery_parent()`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteSuccessWithChildren(ctx, w.RootID, w.ScopeKey, *claim.ClaimedSignalSeq+1, []string{"/cloud/a"}, now); !errors.Is(err, postgres.ErrStaleClaim) {
		t.Fatal(err)
	}
	if _, err := st.CompleteSuccessWithChildren(ctx, w.RootID, w.ScopeKey, *claim.ClaimedSignalSeq, []string{"/outside/a"}, now); err == nil {
		t.Fatal("accepted wrong parent")
	}
	if _, err := st.CompleteSuccessWithChildren(ctx, w.RootID, w.ScopeKey, *claim.ClaimedSignalSeq, []string{"/cloud/a"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT work_state FROM index_dirty_scope_work WHERE scope_key='/cloud/a'`).Scan(&parent); err != nil || parent != "PENDING" {
		t.Fatalf("child not durable: %s %v", parent, err)
	}
}

func TestDiscoveryPrioritySubtreeBeatsBackgroundBacklog(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	// Reproduce the production backlog without touching a provider or Canonical.
	for i := 0; i < 400; i++ {
		if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, fmt.Sprintf("/library/%03d", i), state.SourceRecovery, state.ReasonPossibleChange, now)); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Minute)
	w, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/", state.SourceRecovery, state.ReasonPossibleChange, now))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := st.ClaimWork(ctx, w.RootID, w.ScopeKey, w.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CompleteSuccessWithPrioritizedChildren(ctx, w.RootID, w.ScopeKey, *claim.ClaimedSignalSeq, []string{"/cloud", "/cloud-other", "/other"}, "/cloud", now); err != nil {
		t.Fatal(err)
	}
	selected, found, err := st.NextEligiblePendingWork(ctx, now)
	if err != nil || !found || selected.ScopeKey != "/cloud" || selected.PendingPriority == nil || *selected.PendingPriority != state.PriorityHigh {
		t.Fatalf("download subtree behind background: %+v %v", selected, err)
	}
	claim, err = st.ClaimWork(ctx, selected.RootID, selected.ScopeKey, selected.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	// The priority survives recursive discovery and existing NORMAL pending work.
	if _, err = st.MergeSignal(ctx, p3Signal(p3RootActive, "/cloud/new", state.SourceRecovery, state.ReasonPossibleChange, now)); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CompleteSuccessWithPrioritizedChildren(ctx, claim.RootID, claim.ScopeKey, *claim.ClaimedSignalSeq, []string{"/cloud/new"}, "/cloud", now); err != nil {
		t.Fatal(err)
	}
	selected, found, err = st.NextEligiblePendingWork(ctx, now)
	if err != nil || !found || selected.ScopeKey != "/cloud/new" || *selected.PendingPriority != state.PriorityHigh {
		t.Fatalf("child lost priority: %+v %v", selected, err)
	}
	for _, scope := range []string{"/cloud-other", "/other"} {
		child, err := st.GetWork(ctx, p3RootActive, scope)
		if err != nil || child.PendingPriority == nil || *child.PendingPriority != state.PriorityNormal {
			t.Fatalf("promoted unrelated scope: %+v %v", child, err)
		}
	}
	claim, err = st.ClaimWork(ctx, selected.RootID, selected.ScopeKey, selected.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CompleteSuccess(ctx, claim.RootID, claim.ScopeKey, *claim.ClaimedSignalSeq, now); err != nil {
		t.Fatal(err)
	}
	selected, found, err = st.NextEligiblePendingWork(ctx, now)
	if err != nil || !found || selected.ScopeKey != "/library/000" {
		t.Fatalf("background did not resume: %+v %v", selected, err)
	}
}

func TestDiscoveryPriorityDisabledRootAndValidation(t *testing.T) {
	for _, priorityScope := range []string{"", "/", "/cloud-other", "/cloud/../bad"} {
		t.Run(priorityScope, func(t *testing.T) {
			st, ctx := p3Store(t)
			now := p3Time()
			signal := p3Signal(p3RootActive, "/cloud", state.SourceMutationHint, state.ReasonPossibleChange, now)
			signal.Priority = state.PriorityHigh
			w, err := st.MergeSignal(ctx, signal)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := st.ClaimWork(ctx, w.RootID, w.ScopeKey, w.Version, now)
			if err != nil {
				t.Fatal(err)
			}
			_, err = st.CompleteSuccessWithPrioritizedChildren(ctx, w.RootID, w.ScopeKey, *claim.ClaimedSignalSeq, []string{"/cloud/new"}, priorityScope, now)
			if priorityScope == "/cloud/../bad" {
				if err == nil {
					t.Fatal("accepted invalid priority scope")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			child, err := st.GetWork(ctx, w.RootID, "/cloud/new")
			if err != nil || *child.PendingPriority != state.PriorityNormal {
				t.Fatalf("priority escaped explicit subtree: %+v %v", child, err)
			}
		})
	}
}
