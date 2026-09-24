package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// GetWork reads one DirtyScopeWork row. Missing rows return ErrNotFound.
func (s *Store) GetWork(ctx context.Context, rootID, scopeKey string) (state.DirtyScopeWork, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+workColumns+` FROM index_dirty_scope_work WHERE root_id=$1 AND scope_key=$2`,
		rootID, scopeKey)
	wk, err := scanWork(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.DirtyScopeWork{}, ErrNotFound
	}
	return wk, err
}

// BudgetDefer moves a PENDING item's eligibility later without counting an
// attempt or a failure (P2: budget defer is scheduling pressure, not failure).
func (s *Store) BudgetDefer(ctx context.Context, rootID, scopeKey string, expectedVersion int64, notBefore time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE index_dirty_scope_work
		SET pending_not_before=$3, updated_at=$4, version=version+1
		WHERE root_id=$1 AND scope_key=$2 AND version=$5 AND work_state='PENDING'
		  AND (pending_not_before IS NULL OR pending_not_before < $3)`,
		rootID, scopeKey, notBefore, time.Now().UTC(), expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStateCASConflict
	}
	return nil
}

// RetryReady transitions a due RETRY_WAIT item back to PENDING. Any transition
// that makes work runnable again must confirm the root is ACTIVE in the same
// transaction (lock order Root -> Watch -> Work).
func (s *Store) RetryReady(ctx context.Context, rootID, scopeKey string, expectedVersion int64, now time.Time) (state.DirtyScopeWork, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	defer tx.Rollback(ctx)
	lifecycle, err := lockRootForUpdate(ctx, tx, rootID)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if !activeFromLifecycle(lifecycle) {
		return state.DirtyScopeWork{}, ErrStateCASConflict
	}
	if _, _, err := lockWatchForUpdate(ctx, tx, rootID, scopeKey); err != nil {
		return state.DirtyScopeWork{}, err
	}
	row := tx.QueryRow(ctx, `
		UPDATE index_dirty_scope_work
		SET work_state='PENDING', pending_not_before=NULL, updated_at=$3, version=version+1
		WHERE root_id=$1 AND scope_key=$2 AND version=$4 AND work_state='RETRY_WAIT'
		  AND pending_not_before IS NOT NULL AND pending_not_before <= $3
		RETURNING `+workColumns,
		rootID, scopeKey, now, expectedVersion)
	out, err := scanWork(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.DirtyScopeWork{}, ErrStateCASConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return state.DirtyScopeWork{}, err
	}
	return out, nil
}

// ResumeSuspended transitions a SUSPENDED item back to PENDING, only when the
// root is ACTIVE.
func (s *Store) ResumeSuspended(ctx context.Context, rootID, scopeKey string, expectedVersion int64, now time.Time) (state.DirtyScopeWork, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	defer tx.Rollback(ctx)
	lifecycle, err := lockRootForUpdate(ctx, tx, rootID)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if !activeFromLifecycle(lifecycle) {
		return state.DirtyScopeWork{}, ErrStateCASConflict
	}
	row := tx.QueryRow(ctx, `
		UPDATE index_dirty_scope_work
		SET work_state='PENDING', updated_at=$3, version=version+1
		WHERE root_id=$1 AND scope_key=$2 AND version=$4 AND work_state='SUSPENDED'
		RETURNING `+workColumns,
		rootID, scopeKey, now, expectedVersion)
	out, err := scanWork(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.DirtyScopeWork{}, ErrStateCASConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return state.DirtyScopeWork{}, err
	}
	return out, nil
}

// RepairBlocked transitions a BLOCKED item back to PENDING via the explicit
// repair transition (the only path out of BLOCKED).
func (s *Store) RepairBlocked(ctx context.Context, rootID, scopeKey string, expectedVersion int64, now time.Time) (state.DirtyScopeWork, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	defer tx.Rollback(ctx)
	lifecycle, err := lockRootForUpdate(ctx, tx, rootID)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if !activeFromLifecycle(lifecycle) {
		return state.DirtyScopeWork{}, ErrStateCASConflict
	}
	if _, _, err := lockWatchForUpdate(ctx, tx, rootID, scopeKey); err != nil {
		return state.DirtyScopeWork{}, err
	}
	row := tx.QueryRow(ctx, `
		UPDATE index_dirty_scope_work
		SET work_state='PENDING', updated_at=$3, version=version+1
		WHERE root_id=$1 AND scope_key=$2 AND version=$4 AND work_state='BLOCKED'
		RETURNING `+workColumns,
		rootID, scopeKey, now, expectedVersion)
	out, err := scanWork(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.DirtyScopeWork{}, ErrStateCASConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return state.DirtyScopeWork{}, err
	}
	return out, nil
}
