package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// TestIssue138MirrorRootRegistrationSchema8To9Upgrade proves the schema-8
// search_path defect and the 0009 repair on the real 0001..0008 files: a
// non-owner LOGIN owning a same-named TEMP relation registers a Root, and the
// persisted budget table must receive exactly one row and the TEMP relation none
// only after the upgrade.
func TestIssue138MirrorRootRegistrationSchema8To9Upgrade(t *testing.T) {
	owner := testutil.Issue138Pool(t)
	testutil.ResetSchema(t, owner)
	ctx := context.Background()

	names, err := migrationNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 9 || names[8] != "0009_mirror_root_registration_search_path.sql" {
		t.Fatalf("unexpected migration order: %v", names)
	}
	if _, err := owner.Exec(ctx, `CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		schema9Fatal(t, "create schema_migrations", err)
	}
	for _, name := range names[:8] {
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyOne(ctx, owner, name, string(body)); err != nil {
			schema9Fatal(t, "apply "+name, err)
		}
	}

	limited, role := testutil.Issue138RestrictedLogin(t, owner)
	ident := pgx.Identifier{role}.Sanitize()
	database := pgx.Identifier{owner.Config().ConnConfig.Database}.Sanitize()
	for _, grant := range []string{
		`GRANT TEMP ON DATABASE ` + database + ` TO ` + ident,
		`GRANT USAGE ON SCHEMA public TO ` + ident,
		`GRANT INSERT ON public.index_root TO ` + ident,
		`GRANT INSERT ON public.index_mirror_root_budget TO ` + ident,
	} {
		if _, err := owner.Exec(ctx, grant); err != nil {
			schema9Fatal(t, grant, err)
		}
	}

	status, err := SchemaStatus(ctx, owner)
	if err != nil {
		schema9Fatal(t, "schema-8 status", err)
	}
	if status.Applied != 8 || status.Required != 9 ||
		len(status.Missing) != 1 || status.Missing[0] != "0009_mirror_root_registration_search_path.sql" ||
		status.Compatible() {
		t.Fatalf("schema-8 state: %+v", status)
	}

	conn, err := limited.Acquire(ctx)
	if err != nil {
		schema9Fatal(t, "acquire pinned session", err)
	}
	defer conn.Release()
	// The TEMP relation is session-local, so it must be dropped on the pinned
	// connection even when an assertion above already failed.
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(clean, `DROP TABLE IF EXISTS pg_temp.index_mirror_root_budget`); err != nil {
			t.Error("temporary relation cleanup failed", err)
		}
	}()
	if _, err := conn.Exec(ctx, `CREATE TEMP TABLE index_mirror_root_budget(root_id uuid PRIMARY KEY)`); err != nil {
		schema9Fatal(t, "create TEMP shadow", err)
	}

	const shadowRoot = "13470000-0000-4000-8000-00000000000a"
	const upgradedRoot = "13470000-0000-4000-8000-000000000001"
	if _, err := conn.Exec(ctx, `INSERT INTO public.index_root(root_id,scope_descriptor,lifecycle_state) VALUES($1,'{}','ACTIVE')`, shadowRoot); err != nil {
		schema9Fatal(t, "schema-8 Root insert", err)
	}
	persisted := schema9PersistedCount(t, ctx, owner)
	shadowed := schema9TempCount(t, ctx, conn)
	if persisted != 0 || shadowed != 1 {
		t.Fatalf("schema-8 shadowing not reproduced: persisted %d temp %d", persisted, shadowed)
	}

	if err := Migrate(ctx, owner); err != nil {
		schema9Fatal(t, "apply 0009", err)
	}
	status, err = SchemaStatus(ctx, owner)
	if err != nil {
		schema9Fatal(t, "schema-9 status", err)
	}
	if status.Required != 9 || status.Applied != 9 || status.Missing != nil || status.Unexpected != nil || !status.Compatible() {
		t.Fatalf("schema-9 state: %+v", status)
	}

	var prosecdef bool
	var proconfig []string
	var definition string
	if err := owner.QueryRow(ctx, `SELECT p.prosecdef, coalesce(p.proconfig,'{}'::text[]), pg_get_functiondef(p.oid)
		FROM pg_proc p WHERE p.oid='public.index_register_mirror_root()'::regprocedure`).Scan(&prosecdef, &proconfig, &definition); err != nil {
		schema9Fatal(t, "function metadata", err)
	}
	if prosecdef {
		t.Fatal("function must stay SECURITY INVOKER")
	}
	if len(proconfig) != 1 || !strings.HasPrefix(proconfig[0], "search_path=") ||
		!strings.Contains(proconfig[0], "public") || strings.Contains(proconfig[0], "pg_temp") {
		t.Fatalf("unexpected function proconfig: %v", proconfig)
	}
	if !strings.Contains(definition, "public.index_mirror_root_budget") {
		t.Fatalf("function definition is not schema-qualified: %s", definition)
	}

	if _, err := conn.Exec(ctx, `TRUNCATE pg_temp.index_mirror_root_budget`); err != nil {
		schema9Fatal(t, "truncate TEMP shadow", err)
	}
	if _, err := owner.Exec(ctx, `DELETE FROM public.index_root WHERE root_id=$1`, shadowRoot); err != nil {
		schema9Fatal(t, "clear pre-upgrade Root", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO public.index_root(root_id,scope_descriptor,lifecycle_state) VALUES($1,'{}','ACTIVE')`, upgradedRoot); err != nil {
		schema9Fatal(t, "schema-9 Root insert", err)
	}
	persisted = schema9PersistedCount(t, ctx, owner)
	shadowed = schema9TempCount(t, ctx, conn)
	if persisted != 1 || shadowed != 0 {
		t.Fatalf("schema-9 registration wrote persisted %d temp %d", persisted, shadowed)
	}
}

func schema9PersistedCount(t *testing.T, ctx context.Context, owner *pgxpool.Pool) int {
	t.Helper()
	var persisted int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM public.index_mirror_root_budget`).Scan(&persisted); err != nil {
		schema9Fatal(t, "persisted budget count", err)
	}
	return persisted
}

func schema9TempCount(t *testing.T, ctx context.Context, conn *pgxpool.Conn) int {
	t.Helper()
	var shadowed int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_temp.index_mirror_root_budget`).Scan(&shadowed); err != nil {
		schema9Fatal(t, "temp budget count", err)
	}
	return shadowed
}

func schema9Fatal(t *testing.T, what string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("%s: SQLSTATE %s: %s", what, pgErr.Code, pgErr.Message)
	}
	t.Fatalf("%s: %v", what, err)
}
