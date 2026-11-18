package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestIssue134RollingRestrictedRootRegistration(t *testing.T) {
	owner := testutil.Issue138Pool(t)
	testutil.ResetSchema(t, owner)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, owner); err != nil {
		t.Fatal(err)
	}
	limited, role := testutil.Issue138RestrictedLogin(t, owner)
	ident := pgx.Identifier{role}.Sanitize()
	if _, err := owner.Exec(ctx, `GRANT USAGE ON SCHEMA public TO `+ident); err != nil {
		t.Fatal(err)
	}
	var current, session string
	var privileged, tableOwner, definer bool
	if err := limited.QueryRow(ctx, `SELECT current_user,session_user,r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolbypassrls OR r.rolreplication,
 c.relowner=r.oid,p.prosecdef FROM pg_roles r,pg_class c,pg_proc p
 WHERE r.rolname=current_user AND c.oid='public.index_root'::regclass AND p.oid='public.index_register_mirror_root()'::regprocedure`).Scan(&current, &session, &privileged, &tableOwner, &definer); err != nil {
		t.Fatal(err)
	}
	if current != role || session != role || privileged || tableOwner || definer {
		t.Fatal("not a real invoker/non-owner LOGIN")
	}
	if _, err := owner.Exec(ctx, `GRANT USAGE ON SCHEMA public TO `+ident+`;GRANT INSERT ON public.index_root TO `+ident); err != nil {
		t.Fatal(err)
	}
	st := postgres.New(limited) // F4 OFF, no WithMirrorRolling clone.
	const root = "13450000-0000-4000-8000-000000000001"
	err := st.CreateRoot(ctx, limited, root, []byte(`{}`), domain.RootActive)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
		t.Fatal("missing budget INSERT did not fail closed")
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM index_root)+(SELECT count(*) FROM index_mirror_root_budget)`).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial Root/budget commit", n, err)
	}
	if _, err := owner.Exec(ctx, `GRANT INSERT ON public.index_mirror_root_budget TO `+ident); err != nil {
		t.Fatal(err)
	}
	// Exactly INSERT-only on both tables suffices for the invoker trigger. No
	// owner membership, budget SELECT/UPDATE or function SECURITY DEFINER.
	if err := st.CreateRoot(ctx, limited, root, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	var read, write bool
	if err := owner.QueryRow(ctx, `SELECT has_table_privilege($1,'public.index_mirror_root_budget','SELECT'),has_table_privilege($1,'public.index_mirror_root_budget','UPDATE')`, role).Scan(&read, &write); err != nil || read || write {
		t.Fatal("unexpected extra privileges", err)
	}
	for _, sql := range []string{`DELETE FROM public.index_mirror_root_budget`, `TRUNCATE public.index_mirror_root_budget`, `UPDATE public.index_mirror_root_budget SET scope_cursor=''`} {
		if _, err := limited.Exec(ctx, sql); !errors.As(err, &pgerr) || pgerr.Code != "42501" {
			t.Fatal("registration-only role can mutate persisted budgets")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("13450000-0000-4000-8000-%012d", i+2)
			if err := st.CreateRoot(ctx, limited, id, []byte(`{}`), domain.RootActive); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM index_mirror_root_budget`).Scan(&n); err != nil || n != 9 {
		t.Fatal(n, err)
	}
	if err := st.CreateRoot(ctx, limited, root, []byte(`{}`), domain.RootActive); !errors.As(err, &pgerr) || pgerr.Code != "23505" {
		t.Fatal("duplicate Root accepted")
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM index_mirror_root_budget`).Scan(&n); err != nil || n != 9 {
		t.Fatal("duplicate budgets", n, err)
	}
}

func TestIssue134RollingRuntimeGrantProfiles(t *testing.T) {
	owner := testutil.Issue138Pool(t)
	testutil.ResetSchema(t, owner)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, owner); err != nil {
		t.Fatal(err)
	}
	limited, role := testutil.Issue138RestrictedLogin(t, owner)
	testutil.ApplyRuntimeGrants(t, owner, role, false)
	if err := postgres.CheckRuntimePrivileges(ctx, limited, false); err != nil {
		t.Fatal(err)
	}
	if err := postgres.CheckRuntimePrivileges(ctx, limited, true); !errors.Is(err, postgres.ErrRuntimePrivileges) {
		t.Fatal("OFF grants accepted for ON", err)
	}
	testutil.ApplyRuntimeGrants(t, owner, role, true)
	if err := postgres.CheckRuntimePrivileges(ctx, limited, true); err != nil {
		t.Fatal(err)
	}
	var dangerous bool
	if err := limited.QueryRow(ctx, `SELECT has_table_privilege(current_user,'public.index_mirror_root_budget','DELETE') OR has_table_privilege(current_user,'public.index_mirror_root_budget','TRUNCATE') OR has_schema_privilege(current_user,'public','CREATE')`).Scan(&dangerous); err != nil || dangerous {
		t.Fatal("grant profile is overprivileged", err)
	}
	if _, err := owner.Exec(ctx, `REVOKE INSERT ON public.index_mirror_root_budget FROM `+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if err := postgres.CheckRuntimePrivileges(ctx, limited, false); !errors.Is(err, postgres.ErrRuntimePrivileges) {
		t.Fatal("missing registration grant hidden", err)
	}
}
