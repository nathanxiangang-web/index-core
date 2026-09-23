package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrWriterLockHeld means another indexcore write daemon already owns this
// database. Gate 3 is single-daemon-writer; a second daemon must fail closed.
var ErrWriterLockHeld = errors.New("another indexcore write daemon already owns this database")

// writerLockKey is a stable database-scoped advisory-lock key.
const writerLockKey int64 = 0x1D3EC0DE

// WriterLock is a held PostgreSQL advisory lock owned by a dedicated connection.
type WriterLock struct {
	conn *pgxpool.Conn
}

// AcquireWriterLock takes a session-level advisory lock on a dedicated
// connection and holds it for the process lifetime (G3-R5). HA/multi-daemon is
// explicitly deferred; this enforces the single active writer.
func (s *Store) AcquireWriterLock(ctx context.Context) (*WriterLock, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, writerLockKey).Scan(&ok); err != nil {
		conn.Release()
		return nil, err
	}
	if !ok {
		conn.Release()
		return nil, ErrWriterLockHeld
	}
	return &WriterLock{conn: conn}, nil
}

// Release unlocks and returns the connection. Safe to call on a nil lock.
func (l *WriterLock) Release(ctx context.Context) {
	if l == nil || l.conn == nil {
		return
	}
	_, _ = l.conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, writerLockKey)
	l.conn.Release()
	l.conn = nil
}
