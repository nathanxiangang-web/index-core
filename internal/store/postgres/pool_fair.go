package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"time"
)

// Read-only bounded planning. Root rotation gives one fair slot per pool cycle;
// every row still requires the unchanged ClaimWork CAS. No priority is rewritten.
func (s *Store) NextPoolFairWork(ctx context.Context, now time.Time, after string, excluded ...string) (state.DirtyScopeWork, bool, error) {
	if len(excluded) > 8 {
		return state.DirtyScopeWork{}, false, errors.New("pool excluded root bound")
	}
	if excluded == nil {
		excluded = []string{}
	}
	row := s.pool.QueryRow(ctx, `SELECT `+workColumns+` FROM index_dirty_scope_work WHERE (root_id,scope_key)=(
 SELECT w.root_id,w.scope_key FROM index_dirty_scope_work w JOIN index_root r ON r.root_id=w.root_id
 WHERE r.lifecycle_state='ACTIVE' AND w.work_state='PENDING' AND (w.pending_not_before IS NULL OR w.pending_not_before<=$1) AND NOT(w.root_id::text=ANY($3::text[]))
 ORDER BY CASE WHEN w.root_id::text>$2 THEN 0 ELSE 1 END,w.root_id,
 CASE w.pending_priority WHEN 'HIGH' THEN 0 WHEN 'NORMAL' THEN 1 ELSE 2 END,w.pending_first_seen_at,w.scope_key LIMIT 1)`, now, after, excluded)
	w, err := scanWork(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, false, nil
	}
	return w, err == nil, err
}
