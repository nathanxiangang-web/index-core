package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestIssue134RollingMigrationLocksRegistrationWindow(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	names, err := migrationNames()
	if err != nil {
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
	const old = "13450000-0000-4000-8000-000000000001"
	const later = "13450000-0000-4000-8000-000000000002"
	if _, err := pool.Exec(ctx, `INSERT INTO index_root(root_id,scope_descriptor,lifecycle_state) VALUES($1,'{}','ACTIVE');`, old); err != nil {
		t.Fatal(err)
	}
	install, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer install.Rollback(context.Background())
	body, err := migrationsFS.ReadFile("migrations/0007_mirror_root_registration.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := install.Exec(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := install.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES('0007_mirror_root_registration.sql')`); err != nil {
		t.Fatal(err)
	}
	writer, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Release()
	done := make(chan error, 1)
	go func() {
		_, err := writer.Exec(ctx, `INSERT INTO index_root(root_id,scope_descriptor,lifecycle_state) VALUES($1,'{}','ACTIVE')`, later)
		done <- err
	}()
	// Observe the actual relation-lock wait. No timing-only claim that the
	// concurrent Root happened to fall between trigger installation/backfill.
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT coalesce(bool_or(wait_event_type='Lock' AND wait_event='relation'),false) FROM pg_stat_activity WHERE pid=$1`, writer.Conn().PgConn().PID()).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatal("Root passed migration fence", err)
		case <-ctx.Done():
			t.Fatal("no observed DDL lock wait")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := install.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_root r JOIN index_mirror_root_budget b USING(root_id)`).Scan(&n); err != nil || n != 2 {
		t.Fatal("old/new Root registration gap", n, err)
	}
	// A committed0007 plus failed0008 is a supported resumable upgrade state.
	if _, err := pool.Exec(ctx, `CREATE INDEX idx_identity_resource_latest ON index_root(root_id)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err == nil {
		t.Fatal("index failure injection missed")
	}
	status, err := SchemaStatus(ctx, pool)
	if err != nil || len(status.Missing) != 1 || status.Missing[0] != "0008_latest_identity_observation.sql" {
		t.Fatal(status, err)
	}
	if _, err := pool.Exec(ctx, `DROP INDEX idx_identity_resource_latest`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_mirror_root_budget`).Scan(&n); err != nil || n != 2 {
		t.Fatal("retry lost/duplicated budgets", n, err)
	}
}
