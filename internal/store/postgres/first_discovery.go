package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// Select a first-attempt ordinary discovery, not a retry or HIGH promotion.
// Positive child + parent namespace proof is read from the existing canonical
// observations. The normal scanner must still revalidate everything under CAS.
func (s *Store) NextFirstDiscoveryWork(ctx context.Context, now time.Time, excludedRoots []string) (state.DirtyScopeWork, bool, error) {
	if len(excludedRoots) > 2 {
		return state.DirtyScopeWork{}, false, errors.New("first discovery root quota exceeded")
	}
	if excludedRoots == nil {
		excludedRoots = []string{}
	}
	row := s.pool.QueryRow(ctx, `SELECT `+workColumns+` FROM index_dirty_scope_work WHERE (root_id,scope_key)=(
 SELECT w.root_id,w.scope_key FROM index_dirty_scope_work w
 JOIN index_root r ON r.root_id=w.root_id AND r.lifecycle_state='ACTIVE'
 JOIN index_root_adapter_config a ON a.root_id=w.root_id AND a.collector_kind IN ('alist','openlist')
 JOIN index_dirty_scope_work p ON p.root_id=w.root_id
 AND p.scope_key=COALESCE(NULLIF(regexp_replace(w.scope_key,'/[^/]+$',''),''),'/')
 AND p.last_verified_at IS NOT NULL
 AND p.work_state IN ('VERIFIED','PENDING','IN_FLIGHT') AND p.last_error_class IS NULL
 JOIN index_canonical_resource c ON c.root_id=w.root_id
 AND c.canonical_path=(CASE WHEN a.config->>'path'='/' THEN '' ELSE rtrim(a.config->>'path','/') END)||w.scope_key
 AND c.resource_presence='PRESENT' AND c.is_dir AND c.removal_evidence_state='NONE'
 AND NOT EXISTS(SELECT 1 FROM index_canonical_resource other WHERE other.root_id=c.root_id
 AND other.canonical_path=c.canonical_path AND other.resource_presence='PRESENT' AND other.resource_id<>c.resource_id)
 WHERE w.work_state='PENDING' AND w.pending_priority='NORMAL'
 AND w.last_verified_at IS NULL AND w.attempt_count=0 AND w.scope_key<>'/'
 AND w.pending_source_set @> ARRAY['RECOVERY']::text[]
 AND w.pending_reason_set @> ARRAY['POSSIBLE_CHANGE']::text[]
 AND (w.pending_not_before IS NULL OR w.pending_not_before<=$1)
 AND NOT(w.root_id=ANY($2::uuid[]))
 AND array_length(string_to_array(trim(both '/' from w.scope_key),'/'),1)<=8
 AND (p.scope_key='/' OR 1=(SELECT count(*) FROM index_canonical_resource pc
 WHERE pc.root_id=w.root_id AND pc.resource_presence='PRESENT' AND pc.is_dir AND pc.removal_evidence_state='NONE'
 AND pc.canonical_path=(CASE WHEN a.config->>'path'='/' THEN '' ELSE rtrim(a.config->>'path','/') END)||p.scope_key
 AND NOT EXISTS(SELECT 1 FROM index_canonical_resource other WHERE other.root_id=pc.root_id
 AND other.canonical_path=pc.canonical_path AND other.resource_presence='PRESENT' AND other.resource_id<>pc.resource_id)))
 ORDER BY w.pending_first_seen_at,w.root_id,w.scope_key LIMIT 1)`, now, excludedRoots)
	item, err := scanWork(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.DirtyScopeWork{}, false, nil
	}
	return item, err == nil, err
}

// One aged NORMAL/LOW slot per bounded cycle prevents continuous HIGH or fresh
// arrivals from starving old work (including sibling roots). No row is mutated.
func (s *Store) NextAgedBackgroundWork(ctx context.Context, now, before time.Time) (state.DirtyScopeWork, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+workColumns+` FROM index_dirty_scope_work WHERE (root_id,scope_key)=(
 SELECT w.root_id,w.scope_key FROM index_dirty_scope_work w JOIN index_root r ON r.root_id=w.root_id
 WHERE r.lifecycle_state='ACTIVE' AND w.work_state='PENDING' AND w.pending_priority IN ('NORMAL','LOW')
 AND w.pending_first_seen_at<=$2 AND (w.pending_not_before IS NULL OR w.pending_not_before<=$1)
 ORDER BY w.pending_first_seen_at,w.root_id,w.scope_key LIMIT 1)`, now, before)
	item, err := scanWork(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.DirtyScopeWork{}, false, nil
	}
	return item, err == nil, err
}
