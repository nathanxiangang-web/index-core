package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// F2 opt-in only. Borrow at most one first slot from each of existing-policy and
// managed-revisit cohorts, then retain due/root/scope FIFO. Two bounded cohort
// probes (at most limit rows each) prevent a large WARM cohort hiding HOT roots.
// Returned versions still require unchanged atomic EmitDuePoll CAS.
func (s *Store) ListDueWatchesWithMirrorFairness(ctx context.Context, now time.Time, limit int) ([]state.ScopeWatchState, error) {
	if limit < 3 || limit > 6 {
		return nil, errors.New("mirror due selection requires 3..6 candidates")
	}
	rows, err := s.pool.Query(ctx, `WITH due AS NOT MATERIALIZED (
 SELECT w.*,(w.watch_state='WARM' AND w.cadence_class='MIRROR_REVISIT' AND w.source_set=ARRAY['BACKSTOP_ENROLL']::text[]) managed
 FROM index_scope_watch_state w JOIN index_root r ON r.root_id=w.root_id
 WHERE r.lifecycle_state='ACTIVE' AND w.watch_state IN ('HOT','WARM') AND w.next_due_at<=$1
 AND (w.deferred_until IS NULL OR w.deferred_until<=$1)
), ordinary AS (SELECT * FROM due WHERE NOT managed ORDER BY next_due_at,root_id,scope_key LIMIT $2),
rolling AS (SELECT * FROM due WHERE managed ORDER BY next_due_at,root_id,scope_key LIMIT $2),
ranked AS (SELECT x.*,row_number() OVER(PARTITION BY managed ORDER BY next_due_at,root_id,scope_key) pos FROM (SELECT * FROM ordinary UNION ALL SELECT * FROM rolling) x)
SELECT `+watchColumns+` FROM ranked ORDER BY CASE WHEN pos=1 AND NOT managed THEN 0 WHEN pos=1 AND managed THEN 1 ELSE 2 END,next_due_at,root_id,scope_key LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []state.ScopeWatchState
	for rows.Next() {
		w, err := scanWatch(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
