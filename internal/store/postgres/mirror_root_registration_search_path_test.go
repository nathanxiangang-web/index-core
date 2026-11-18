package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Probe row seeded into the session-local shadow relation; it must never reach
// the persisted budget table.
const issue138ShadowProbeRoot = "13460000-0000-4000-8000-000000000009"

func TestIssue138MirrorRootRegistrationTempShadowing(t *testing.T) {
	owner := testutil.Issue138Pool(t)
	testutil.ResetSchema(t, owner)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, owner); err != nil {
		t.Fatal(err)
	}
	limited, role := testutil.Issue138RestrictedLogin(t, owner)
	ident := pgx.Identifier{role}.Sanitize()
	database := pgx.Identifier{owner.Config().ConnConfig.Database}.Sanitize()
	for _, sql := range []string{
		`GRANT TEMP ON DATABASE ` + database + ` TO ` + ident,
		`GRANT USAGE ON SCHEMA public TO ` + ident,
		`GRANT INSERT ON public.index_root TO ` + ident,
		`GRANT INSERT ON public.index_mirror_root_budget TO ` + ident,
	} {
		if _, err := owner.Exec(ctx, sql); err != nil {
			fatalSQL(t, "grant failed: "+sql, err)
		}
	}

	// TEMP relations are session-local: the shadow table, the probe insert and
	// the Root registration must all run on one pinned connection, otherwise
	// the trigger would never see the shadowing relation at all.
	conn, err := limited.Acquire(ctx)
	if err != nil {
		fatalSQL(t, "restricted session unavailable", err)
	}
	defer func() {
		if _, err := conn.Exec(context.Background(), `DROP TABLE IF EXISTS pg_temp.index_mirror_root_budget`); err != nil {
			t.Error("temporary table cleanup failed:", err)
		}
		conn.Release()
	}()

	var prosecdef bool
	var proconfig []string
	var definition string
	if err := owner.QueryRow(ctx, `SELECT prosecdef, coalesce(proconfig, '{}'::text[]), pg_get_functiondef(oid)
 FROM pg_proc WHERE oid = 'public.index_register_mirror_root()'::regprocedure`).Scan(&prosecdef, &proconfig, &definition); err != nil {
		fatalSQL(t, "function metadata unavailable", err)
	}
	if prosecdef {
		t.Fatal("registration function is SECURITY DEFINER, expected SECURITY INVOKER")
	}
	if len(proconfig) != 1 {
		t.Fatalf("proconfig = %v, expected exactly one pinned setting", proconfig)
	}
	if !strings.HasPrefix(proconfig[0], "search_path=") {
		t.Fatalf("proconfig[0] = %q, expected search_path= prefix", proconfig[0])
	}
	if !strings.Contains(proconfig[0], "public") {
		t.Fatalf("proconfig[0] = %q, expected public in search_path", proconfig[0])
	}
	if strings.Contains(proconfig[0], "pg_temp") {
		t.Fatalf("proconfig[0] = %q, expected pg_temp excluded", proconfig[0])
	}
	if !strings.Contains(definition, "public.index_mirror_root_budget") {
		t.Fatalf("function definition is not schema-qualified: %s", definition)
	}

	if _, err := conn.Exec(ctx, `CREATE TEMP TABLE index_mirror_root_budget(root_id uuid)`); err != nil {
		fatalSQL(t, "temporary shadow relation unavailable", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO index_mirror_root_budget(root_id) VALUES($1)`, issue138ShadowProbeRoot); err != nil {
		fatalSQL(t, "shadow probe insert failed", err)
	}
	if n := countRows(t, conn, `SELECT count(*) FROM pg_temp.index_mirror_root_budget`); n != 1 {
		t.Fatalf("shadow premise false: pg_temp rows = %d, expected 1", n)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM public.index_mirror_root_budget`); n != 0 {
		t.Fatalf("shadow premise false: persisted rows = %d, expected 0", n)
	}

	const root = "13460000-0000-4000-8000-000000000001"
	st := postgres.New(limited)
	if err := st.CreateRoot(ctx, conn, root, []byte(`{}`), domain.RootActive); err != nil {
		fatalSQL(t, "Root registration in shadowed session failed", err)
	}
	if n := countRows(t, owner, `SELECT count(*) FROM public.index_mirror_root_budget WHERE root_id = $1`, root); n != 1 {
		t.Fatalf("persisted budget rows for root = %d, expected 1", n)
	}
	if n := countRows(t, conn, `SELECT count(*) FROM pg_temp.index_mirror_root_budget`); n != 1 {
		t.Fatalf("trigger wrote into temporary relation: pg_temp rows = %d, expected the 1 probe row", n)
	}

	var canSelect, canUpdate bool
	if err := owner.QueryRow(ctx, `SELECT has_table_privilege($1, 'public.index_mirror_root_budget', 'SELECT'),
 has_table_privilege($1, 'public.index_mirror_root_budget', 'UPDATE')`, role).Scan(&canSelect, &canUpdate); err != nil {
		fatalSQL(t, "privilege probe unavailable", err)
	}
	if canSelect || canUpdate {
		t.Fatalf("least privilege violated: SELECT = %v, UPDATE = %v", canSelect, canUpdate)
	}
}

func countRows(t *testing.T, q postgres.Querier, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		fatalSQL(t, "count failed: "+sql, err)
	}
	return n
}

func fatalSQL(t *testing.T, context string, err error) {
	t.Helper()
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		t.Fatalf("%s: SQLSTATE %s: %s", context, pgerr.Code, pgerr.Message)
	}
	t.Fatalf("%s: %v", context, err)
}
