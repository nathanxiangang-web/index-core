package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Querier is satisfied by *pgxpool.Pool and pgx.Tx, so repository methods run
// either standalone or inside a caller-managed transaction (doc B T-AT1).
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store is the PostgreSQL realization of the frozen Store Interface behind the
// Kernel Domain. The Domain never imports PostgreSQL types (ADR-002, M2).
type Store struct {
	pool *pgxpool.Pool
}

// New wraps an existing pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the underlying pool for transaction management (doc B Stage 1/2).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }