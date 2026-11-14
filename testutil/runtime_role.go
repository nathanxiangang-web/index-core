package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Issue138RestrictedLogin creates a real, unprivileged LOGIN in the existing
// purpose-guarded disposable cluster. Never SET ROLE on an owner connection.
// Cleanup revokes this unique role's grants and drops only that role.
func Issue138RestrictedLogin(t *testing.T, owner *pgxpool.Pool) (*pgxpool.Pool, string) {
	t.Helper()
	if owner.Config().ConnConfig.Database != "index138_test" {
		t.Fatal("restricted login requires Issue138 target")
	}
	guard := Issue138Pool(t)
	a, b := owner.Config().ConnConfig, guard.Config().ConnConfig
	if a.Host != b.Host || a.Port != b.Port || a.User != b.User || a.Database != b.Database {
		t.Fatal("restricted login owner differs from guarded target")
	}
	guard.Close()
	ctx := context.Background()
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal("test role entropy failed")
	}
	role := "issue134_role_" + hex.EncodeToString(raw[:8])
	password := hex.EncodeToString(raw[8:])
	ident := pgx.Identifier{role}.Sanitize()
	if _, err := owner.Exec(ctx, `CREATE ROLE `+ident+` LOGIN PASSWORD '`+password+`' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS`); err != nil {
		t.Fatal("create disposable role failed")
	}
	var limited *pgxpool.Pool
	t.Cleanup(func() {
		if limited != nil {
			limited.Close()
		}
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := owner.Exec(clean, `DROP OWNED BY `+ident); err != nil {
			t.Error("test role grant cleanup failed")
		}
		if _, err := owner.Exec(clean, `DROP ROLE `+ident); err != nil {
			t.Error("test role cleanup failed")
		}
	})
	if _, err := owner.Exec(ctx, `GRANT CONNECT ON DATABASE index138_test TO `+ident); err != nil {
		t.Fatal(err)
	}
	cfg := owner.Config().Copy()
	cfg.ConnConfig.User = role
	cfg.ConnConfig.Password = password
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.MaxConns = 2
	var err error
	limited, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("restricted pool configuration failed")
	}
	if err := limited.Ping(ctx); err != nil {
		t.Fatal("restricted LOGIN failed")
	}
	return limited, role
}

// ApplyRuntimeGrants executes the actual reviewed SQL recipe for this test role.
// psql's quoted-identifier variable is replaced with a pgx-quoted identifier.
func ApplyRuntimeGrants(t *testing.T, owner *pgxpool.Pool, role string, rolling bool) {
	t.Helper()
	if !strings.HasPrefix(role, "issue134_role_") || owner.Config().ConnConfig.Database != "index138_test" {
		t.Fatal("grant recipe requires owned test role")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("grant recipe location unavailable")
	}
	names := []string{"runtime-grants.sql"}
	if rolling {
		names = append(names, "mirror-rolling-grants.sql")
	}
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "scripts", "sql", name))
		if err != nil {
			t.Fatal(err)
		}
		sql := strings.ReplaceAll(string(body), `:"runtime_role"`, pgx.Identifier{role}.Sanitize())
		sql = strings.ReplaceAll(sql, `:"runtime_database"`, pgx.Identifier{owner.Config().ConnConfig.Database}.Sanitize())
		if _, err := owner.Exec(context.Background(), sql); err != nil {
			t.Fatal("runtime grant recipe failed", err)
		}
	}
}
