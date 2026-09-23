package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open returns a pgx/v5 connection pool. PostgreSQL is the Store realization
// behind the Store Interface; no ORM is used (ADR-002, PROJECT-CONTEXT Sec 3).
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, dsn)
}