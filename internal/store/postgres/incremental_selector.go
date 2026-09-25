package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// NextEligiblePendingWork selects exactly one eligible PENDING DirtyScopeWork for
// an ACTIVE root, deterministically, and returns its current version.
//
// It is strictly read-only: it neither claims nor locks the item, and it creates
// no execution ownership. A caller must win ClaimWork(expectedWorkVersion) before
// it may execute the returned row; if another mutation wins first, ClaimWork
// returns ErrStateCASConflict.
//
// Only PENDING is eligible. RETRY_WAIT / BLOCKED / SUSPENDED / VERIFIED remain
// governed by the explicit P3 primitives and are never auto-transitioned here.
//
// Deterministic order (operational selection only; this is not a Canonical
// cross-root order): priority rank DESC, then pending_first_seen_at ASC, then
// root_id ASC, then scope_key ASC. The priority rank is explicit — text lexical
// order is not authoritative.
func (s *Store) NextEligiblePendingWork(ctx context.Context, now time.Time) (state.DirtyScopeWork, bool, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+workColumns+`
		  FROM index_dirty_scope_work
		 WHERE (root_id, scope_key) = (
		       SELECT w.root_id, w.scope_key
		         FROM index_dirty_scope_work w
		         JOIN index_root r ON r.root_id = w.root_id
		        WHERE r.lifecycle_state = 'ACTIVE'
		          AND w.work_state = 'PENDING'
		          AND (w.pending_not_before IS NULL OR w.pending_not_before <= $1)
		        ORDER BY
		          CASE w.pending_priority
		            WHEN 'URGENT' THEN 4
		            WHEN 'HIGH'   THEN 3
		            WHEN 'NORMAL' THEN 2
		            WHEN 'LOW'    THEN 1
		            ELSE 0
		          END DESC,
		          w.pending_first_seen_at ASC,
		          w.root_id ASC,
		          w.scope_key ASC
		        LIMIT 1
		 )`, now)
	wk, err := scanWork(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.DirtyScopeWork{}, false, nil
	}
	if err != nil {
		return state.DirtyScopeWork{}, false, err
	}
	return wk, true, nil
}

// DueRetryClasses are the only error classes the P10 hybrid runtime is
// authorized to auto-promote from RETRY_WAIT back to PENDING.
var DueRetryClasses = []state.ErrorClass{state.ErrorTransientProvider, state.ErrorThrottled}

// ListDueRetryWork selects up to limit ACTIVE-root RETRY_WAIT rows that are due
// (pending_not_before <= now) and whose last_error_class is one of the
// P10-approved transient provider classes. It is strictly read-only and returns
// the current row version, so the caller must win RetryReady's CAS. CAS conflict
// is stale operational state and must not be retried in the same pass.
//
// Deterministic order: pending_not_before ASC, root_id ASC, scope_key ASC.
func (s *Store) ListDueRetryWork(ctx context.Context, now time.Time, limit int) ([]state.DirtyScopeWork, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+workColumns+`
		  FROM index_dirty_scope_work
		 WHERE (root_id, scope_key) IN (
		       SELECT w.root_id, w.scope_key
		         FROM index_dirty_scope_work w
		         JOIN index_root r ON r.root_id = w.root_id
		        WHERE r.lifecycle_state = 'ACTIVE'
		          AND w.work_state = 'RETRY_WAIT'
		          AND w.pending_not_before IS NOT NULL
		          AND w.pending_not_before <= $1
		          AND w.last_error_class IN ('TRANSIENT_PROVIDER','THROTTLED')
		        ORDER BY w.pending_not_before ASC, w.root_id ASC, w.scope_key ASC
		        LIMIT $2)
		 ORDER BY pending_not_before ASC, root_id ASC, scope_key ASC`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []state.DirtyScopeWork
	for rows.Next() {
		wk, err := scanWork(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, wk)
	}
	return out, rows.Err()
}

// ListInflightRoots returns up to limit deterministic root IDs that still have
// IN_FLIGHT DirtyScopeWork rows. It is used only for writer-owned startup
// recovery and is strictly read-only.
func (s *Store) ListInflightRoots(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT root_id
		  FROM index_dirty_scope_work
		 WHERE work_state = 'IN_FLIGHT'
		 ORDER BY root_id ASC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
