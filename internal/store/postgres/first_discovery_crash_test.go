package postgres_test

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Backend termination is permitted only in this explicitly marked, owned,
// loopback PG18 database. Verify the target BEFORE p3Store resets its schema.
func TestIssue134DiscoveryCrashRollback(t *testing.T) {
	if os.Getenv("INDEXCORE_ISSUE134_DISPOSABLE") == "" {
		t.Skip("requires purpose-marked disposable Issue134 PostgreSQL18")
	}
	u, err := url.Parse(os.Getenv("INDEXCORE_TEST_DATABASE_URL"))
	if err != nil || os.Getenv("INDEXCORE_ISSUE134_DISPOSABLE") != "owned-disposable-only" ||
		(u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/index134_test" ||
		(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.RawQuery != "sslmode=disable" || u.Fragment != "" || u.User == nil {
		t.Fatal("refuses ambiguous crash-test target")
	}
	pool := testutil.Pool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var marker, owner string
	var version int
	if err := pool.QueryRow(ctx, `SELECT coalesce(shobj_description(oid,'pg_database'),''),pg_get_userbyid(datdba),current_setting('server_version_num')::int FROM pg_database WHERE datname=current_database()`).Scan(&marker, &owner, &version); err != nil || marker != "indexcore-issue134-owned-disposable" || owner != pool.Config().ConnConfig.User || version < 180000 || version >= 190000 {
		t.Fatal("refuses unverified crash-test database")
	}
	st, _ := p3Store(t)
	now := p3Time()
	if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/", state.SourceManualOperator, state.ReasonManualVerify, now)); err != nil {
		t.Fatal(err)
	}
	claim, err := p3Claim(t, st, ctx, p3RootActive, "/", now)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Release()
	if _, err := controller.Exec(ctx, `SELECT pg_advisory_lock(134134)`); err != nil {
		t.Fatal(err)
	}
	defer controller.Exec(context.Background(), `SELECT pg_advisory_unlock(134134)`)
	// AFTER INSERT blocks after child publication but before parent completion.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_issue134_child() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.scope_key='/fresh' THEN PERFORM set_config('application_name','issue134-child-publication',true); PERFORM pg_advisory_xact_lock(134134); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_issue134 AFTER INSERT ON index_dirty_scope_work FOR EACH ROW EXECUTE FUNCTION pause_issue134_child()`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := st.CompleteSuccessWithChildren(ctx, p3RootActive, "/", *claim.ClaimedSignalSeq, []string{"/fresh"}, now)
		done <- err
	}()
	var backend int
	for backend == 0 {
		if err := pool.QueryRow(ctx, `SELECT coalesce((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND application_name='issue134-child-publication' AND wait_event_type='Lock' AND wait_event='advisory' AND pid<>pg_backend_pid() LIMIT 1),0)`).Scan(&backend); err != nil {
			t.Fatal(err)
		}
		if backend == 0 {
			select {
			case err := <-done:
				t.Fatalf("completion escaped crash barrier: %v", err)
			case <-ctx.Done():
				t.Fatal("crash barrier deadline")
			case <-time.After(time.Millisecond):
			}
		}
	}
	var children int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_dirty_scope_work WHERE scope_key='/fresh'`).Scan(&children); err != nil || children != 0 {
		t.Fatal("uncommitted child visible", children, err)
	}
	var killed bool
	if err := pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, backend).Scan(&killed); err != nil || !killed {
		t.Fatal("owned backend termination failed", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("crashed completion reported success")
		}
	case <-ctx.Done():
		t.Fatal("crashed completion did not return")
	}
	parent, err := st.GetWork(ctx, p3RootActive, "/")
	if err != nil || parent.WorkState != state.WorkInFlight || parent.ClaimedSignalSeq == nil || *parent.ClaimedSignalSeq != *claim.ClaimedSignalSeq {
		t.Fatal("parent partially committed", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_dirty_scope_work WHERE scope_key='/fresh'`).Scan(&children); err != nil || children != 0 {
		t.Fatal("crash leaked child publication", children, err)
	}
}
