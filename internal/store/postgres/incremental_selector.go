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
