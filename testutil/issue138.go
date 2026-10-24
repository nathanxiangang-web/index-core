package testutil

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Issue138Pool refuses schema resets outside an exact owner-marked loopback
// PG18 target. No production/default DSN fallback, including optional scale runs.
func Issue138Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("INDEXCORE_ISSUE138_DISPOSABLE") == "" {
		t.Skip("requires owned Issue138 PG18")
	}
	u, err := url.Parse(os.Getenv("INDEXCORE_TEST_DATABASE_URL"))
	if err != nil || os.Getenv("INDEXCORE_ISSUE138_DISPOSABLE") != "owned-disposable-only" ||
		(u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/index138_test" ||
		(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.RawQuery != "sslmode=disable" || u.User == nil || u.Fragment != "" {
		t.Fatal("refuses ambiguous Issue138 target")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal("Issue138 connection failed")
	}
	t.Cleanup(pool.Close)
	var marker, owner string
	var version int
	if err := pool.QueryRow(ctx, `SELECT coalesce(shobj_description(oid,'pg_database'),''),pg_get_userbyid(datdba),current_setting('server_version_num')::int FROM pg_database WHERE datname=current_database()`).Scan(&marker, &owner, &version); err != nil || marker != "indexcore-issue138-owned-disposable" || owner != pool.Config().ConnConfig.User || version < 180000 || version >= 190000 {
		t.Fatal("refuses unverified Issue138 database")
	}
	return pool
}
