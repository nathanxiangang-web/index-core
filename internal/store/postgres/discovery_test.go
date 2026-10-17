package postgres_test

import (
	"errors"
	"testing"

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
	if _, err := st.CompleteSuccessWithChildren(ctx, w.RootID, w.ScopeKey, *claim.ClaimedSignalSeq, []string{"/cloud/a"}, now); err == nil {
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
