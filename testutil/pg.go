package testutil

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultTestDSN targets the local Gate 2 PostgreSQL 18 container (make pg-up).
const DefaultTestDSN = "postgres://indexcore:indexcore@localhost:55432/indexcore?sslmode=disable"

// TestDSN returns INDEXCORE_TEST_DATABASE_URL or the local default.
func TestDSN() string {
	if v := os.Getenv("INDEXCORE_TEST_DATABASE_URL"); v != "" {
		return v
	}
	return DefaultTestDSN
}

// Pool connects to the real PostgreSQL test database. Per Issue #44, PostgreSQL
// behavior MUST be tested against PostgreSQL, never an in-memory fake.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, TestDSN())
	if err != nil {
		t.Fatalf("connect test postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test postgres (%s): %v", TestDSN(), err)
	}
	return pool
}

// ResetSchema drops and recreates the public schema so each test starts from an
// empty database, proving migrations are reproducible from scratch.
func ResetSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
}
