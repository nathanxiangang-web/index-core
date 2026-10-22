package testutil

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Issue130Pool isolates destructive repair regressions from every inherited
// test database. Opt-in alone is insufficient: a purpose-named DB, dedicated
// marker, ownership, PG18 and a loopback connection are all verified BEFORE any
// reset. CI explicitly provisions it; no default DSN fallback is permitted.
func Issue130Pool(t *testing.T, options ...func(*pgxpool.Config)) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("INDEXCORE_ISSUE130_DATABASE_URL")
	if dsn == "" {
		t.Skip("Issue130 requires separately provisioned owned PG18")
	}
	if err := issue130Target(dsn, os.Getenv("INDEXCORE_ISSUE130_DISPOSABLE")); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid Issue130 configuration")
	}
	for _, option := range options {
		option(cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("Issue130 connect failed")
	}
	t.Cleanup(pool.Close)
	var db, marker, owner string
	var version int
	err = pool.QueryRow(ctx, `SELECT current_database(),coalesce(shobj_description(oid,'pg_database'),''),pg_get_userbyid(datdba),current_setting('server_version_num')::int FROM pg_database WHERE datname=current_database()`).Scan(&db, &marker, &owner, &version)
	if err != nil || db != "indexcore_issue130_test" || marker != "indexcore-issue130-owned-disposable" || owner != cfg.ConnConfig.User || version < 180000 || version >= 190000 {
		t.Fatal("Issue130 refuses unverified disposable database identity")
	}
	return pool
}

func issue130Target(dsn, optIn string) error {
	u, err := url.Parse(dsn)
	if err != nil || optIn != "owned-disposable-only" || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/indexcore_issue130_test" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.RawQuery != "sslmode=disable" || u.Fragment != "" || u.User == nil {
		return errors.New("Issue130 refuses ambiguous test target")
	}
	return nil
}
