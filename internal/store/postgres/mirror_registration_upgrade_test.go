package postgres

import (
	"context"
	"testing"

	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestIssue134RollingRegistrationUpgradeRetry(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	// Apply the actual immutable 0001..0006 files, not a hand-made schema.
	names, err := migrationNames()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, name := range names[:6] {
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyOne(ctx, pool, name, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO index_root(root_id,scope_descriptor,lifecycle_state) VALUES
 ('13440000-0000-4000-8000-000000000001','{}','ACTIVE'),('13440000-0000-4000-8000-000000000002','{}','DEPRECATED');
 INSERT INTO index_mirror_root_budget(root_id,scope_cursor,outstanding_scope,outstanding_seq) VALUES('13440000-0000-4000-8000-000000000001','/kept','/kept',7);
 INSERT INTO index_scope_watch_state(root_id,scope_key,watch_state,cadence_class,effective_interval_seconds,source_set,priority_class,next_due_at)
 VALUES('13440000-0000-4000-8000-000000000001','/kept','WARM','MIRROR_REVISIT',120,ARRAY['BACKSTOP_ENROLL'],'NORMAL',now());
 CREATE FUNCTION registration_upgrade_abort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.root_id='13440000-0000-4000-8000-000000000002' THEN RAISE EXCEPTION 'owned upgrade failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER registration_upgrade_abort BEFORE INSERT ON index_mirror_root_budget FOR EACH ROW EXECUTE FUNCTION registration_upgrade_abort()`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err == nil {
		t.Fatal("expected atomic migration failure")
	}
	var installed bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='index_register_mirror_root')`).Scan(&installed); err != nil || installed {
		t.Fatal("partial migration trigger", installed, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER registration_upgrade_abort ON index_mirror_root_budget; DROP FUNCTION registration_upgrade_abort()`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var roots, watches int
	var cursor, scope string
	var seq int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_mirror_root_budget`).Scan(&roots); err != nil || roots != 2 {
		t.Fatal(roots, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_scope_watch_state`).Scan(&watches); err != nil || watches != 1 {
		t.Fatal(watches, err)
	}
	if err := pool.QueryRow(ctx, `SELECT scope_cursor,outstanding_scope,outstanding_seq FROM index_mirror_root_budget WHERE root_id='13440000-0000-4000-8000-000000000001'`).Scan(&cursor, &scope, &seq); err != nil || cursor != "/kept" || scope != "/kept" || seq != 7 {
		t.Fatal(cursor, scope, seq, err)
	}
	status, err := SchemaStatus(ctx, pool)
	if err != nil || !status.Compatible() || status.Required != 8 {
		t.Fatal(status, err)
	}
}
