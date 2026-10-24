package postgres_test

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Backend termination is permitted only in this explicitly marked, owned,
// loopback PG18 database. Verify the target BEFORE p3Store resets its schema.
func TestIssue134DiscoveryCrashRollback(t *testing.T) {
	issue134DiscoveryCrash(t, false)
}

func TestIssue134MirrorEnrollmentCrashRollback(t *testing.T) {
	issue134DiscoveryCrash(t, true)
}

func issue134DiscoveryCrash(t *testing.T, mirror bool) {
	pool := testutil.Issue134Pool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, _ := p3Store(t)
	now := p3Time()
	if mirror {
		if err := st.UpsertAdapterConfig(ctx, p3RootActive, postgres.AdapterConfig{CollectorKind: "openlist", Config: []byte(`{"path":"/source","mirror_presence":true}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,is_dir,name,updated_at) VALUES('13420000-0000-4000-8000-000000000001',$1,0,0,'PRESENT','NONE','/source/fresh',true,'fresh',$2)`, p3RootActive, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
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
		var err error
		if mirror {
			_, _, err = st.CompleteSuccessWithMirrorChildren(ctx, p3RootActive, "/", *claim.ClaimedSignalSeq, []string{"/fresh"}, "", now, 2*time.Minute)
		} else {
			_, err = st.CompleteSuccessWithChildren(ctx, p3RootActive, "/", *claim.ClaimedSignalSeq, []string{"/fresh"}, now)
		}
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
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_scope_watch_state WHERE scope_key='/fresh'`).Scan(&children); err != nil || children != 0 {
		t.Fatal("crash leaked watch enrollment", children, err)
	}
	if mirror {
		if _, err := pool.Exec(ctx, `DROP TRIGGER pause_issue134 ON index_dirty_scope_work; DROP FUNCTION pause_issue134_child()`); err != nil {
			t.Fatal(err)
		}
		restartPool, err := pgxpool.New(ctx, testutil.TestDSN())
		if err != nil {
			t.Fatal(err)
		}
		defer restartPool.Close()
		restarted := postgres.New(restartPool)
		if count, err := restarted.RecoverStaleInflight(ctx, p3RootActive, now.Add(time.Second)); err != nil || count != 1 {
			t.Fatal("startup recovery", count, err)
		}
		p, err := p3Claim(t, restarted, ctx, p3RootActive, "/", now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, stats, err := restarted.CompleteSuccessWithMirrorChildren(ctx, p3RootActive, "/", *p.ClaimedSignalSeq, []string{"/fresh"}, "", now.Add(time.Second), 2*time.Minute); err != nil || stats.NewChildren != 1 || stats.WatchEnrolled != 1 {
			t.Fatal("restart replay", stats, err)
		}
		if err := restartPool.QueryRow(ctx, `SELECT count(*) FROM index_scope_watch_state WHERE scope_key='/fresh'`).Scan(&children); err != nil || children != 1 {
			t.Fatal("restart lost/duplicated watch", children, err)
		}
	}
}
