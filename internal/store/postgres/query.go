package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/query"
)

const defaultPageSize = 100

// QueryListRoots returns roots honoring the frozen visibility defaults (doc C V4..V6).
func (s *Store) QueryListRoots(ctx context.Context, includeDeprecated, includeDeleted bool) ([]query.RootView, error) {
	roots, err := s.ListRoots(ctx, s.pool, includeDeprecated, includeDeleted)
	if err != nil {
		return nil, err
	}
	out := make([]query.RootView, 0, len(roots))
	for _, r := range roots {
		out = append(out, query.RootView{
			RootID: r.RootID, ScopeDescriptor: r.ScopeDescriptor,
			LifecycleState: r.LifecycleState, CurrentGeneration: r.CurrentGeneration, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// QueryRootStatus returns root lifecycle/generation plus the applied high-water mark.
func (s *Store) QueryRootStatus(ctx context.Context, rootID string) (query.RootStatus, error) {
	root, err := s.GetRoot(ctx, s.pool, rootID)
	if err != nil {
		return query.RootStatus{}, err
	}
	appliedMax, err := s.AppliedMax(ctx, s.pool, rootID)
	if err != nil {
		return query.RootStatus{}, err
	}
	var applied *int64
	if appliedMax > 0 {
		applied = &appliedMax
	}
	return query.RootStatus{
		RootID: root.RootID, LifecycleState: root.LifecycleState,
		CurrentGeneration: root.CurrentGeneration, LastAppliedAdmissionSeq: applied,
	}, nil
}

// QueryGetResource returns a resource unless it is a REMOVED tombstone and
// includeRemoved is false (doc C V1/V2, QC2/QC3).
func (s *Store) QueryGetResource(ctx context.Context, resourceID string, includeRemoved bool) (*query.ResourceView, error) {
	r, err := s.GetCanonicalResource(ctx, s.pool, resourceID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if r.ResourcePresence == domain.ResourceRemoved && !includeRemoved {
		return nil, nil
	}
	v := query.View(r)
	return &v, nil
}

// QueryResolvePath returns ALL live resources at a path with explicit ambiguity
// (doc C V9, QC12). It never fabricates a single winner.
func (s *Store) QueryResolvePath(ctx context.Context, rootID, path string, includeRemoved bool) (query.PathResolution, error) {
	rows, err := s.queryCanonical(ctx, s.pool,
		`WHERE root_id = $1::uuid AND canonical_path = $2 AND resource_presence = 'PRESENT' ORDER BY resource_id`,
		rootID, path)
	if err != nil {
		return query.PathResolution{}, err
	}
	res := query.PathResolution{}
	for _, r := range rows {
		res.Matches = append(res.Matches, query.View(r))
	}
	if includeRemoved {
		removed, err := s.queryCanonical(ctx, s.pool,
			`WHERE root_id = $1::uuid AND canonical_path = $2 AND resource_presence = 'REMOVED' ORDER BY resource_id`,
			rootID, path)
		if err != nil {
			return query.PathResolution{}, err
		}
		for _, r := range removed {
			res.Matches = append(res.Matches, query.View(r))
		}
	}
	res.Ambiguous = len(res.Matches) > 1
	return res, nil
}

// QueryListActivePage returns PRESENT resources ordered by (canonical_path,
// resource_id) with generation-bound pagination (doc C P1..P3, QC11).
func (s *Store) QueryListActivePage(ctx context.Context, rootID string, cur *query.Cursor, limit int) (query.ResourcePage, error) {
	return s.listPage(ctx, rootID, cur, limit, domain.ResourcePresent)
}

// QueryListRemovedPage returns REMOVED tombstones (explicit history access, doc C Q7).
func (s *Store) QueryListRemovedPage(ctx context.Context, rootID string, cur *query.Cursor, limit int) (query.ResourcePage, error) {
	return s.listPage(ctx, rootID, cur, limit, domain.ResourceRemoved)
}

func (s *Store) listPage(ctx context.Context, rootID string, cur *query.Cursor, limit int, presence domain.ResourcePresence) (query.ResourcePage, error) {
	root, err := s.GetRoot(ctx, s.pool, rootID)
	if err != nil {
		return query.ResourcePage{}, err
	}
	if cur != nil {
		if cur.RootID != rootID || cur.Generation != root.CurrentGeneration {
			return query.ResourcePage{}, query.ErrStaleCursor
		}
	}
	if limit <= 0 {
		limit = defaultPageSize
	}

	var (
		rows pgx.Rows
	)
	base := `SELECT resource_id::text, root_id::text, canonical_path, parent_resource_id::text, name, is_dir,
	               size, mtime, content_hash, content_type, resource_presence,
	               introduced_at_generation, last_confirmed_generation
	          FROM index_canonical_resource
	         WHERE root_id = $1::uuid AND resource_presence = $2`
	if cur != nil && cur.HasAfter {
		rows, err = s.pool.Query(ctx, base+`
		  AND (COALESCE(canonical_path, ''), resource_id::text) > ($3::text, $4::text)
		  ORDER BY COALESCE(canonical_path, ''), resource_id::text
		  LIMIT $5`, rootID, string(presence), cur.AfterPath, cur.AfterResourceID, limit)
	} else {
		rows, err = s.pool.Query(ctx, base+`
		  ORDER BY COALESCE(canonical_path, ''), resource_id::text
		  LIMIT $3`, rootID, string(presence), limit)
	}
	if err != nil {
		return query.ResourcePage{}, err
	}
	defer rows.Close()

	page := query.ResourcePage{}
	for rows.Next() {
		v, err := scanResourceView(rows)
		if err != nil {
			return query.ResourcePage{}, err
		}
		page.Items = append(page.Items, v)
	}
	if err := rows.Err(); err != nil {
		return query.ResourcePage{}, err
	}
	if len(page.Items) == limit {
		last := page.Items[len(page.Items)-1]
		p := ""
		if last.CanonicalPath != nil {
			p = *last.CanonicalPath
		}
		page.Next = &query.Cursor{
			RootID: rootID, Generation: root.CurrentGeneration,
			HasAfter: true, AfterPath: p, AfterResourceID: last.ResourceID,
		}
	}
	return page, nil
}

// QueryReadJournal returns per-root journal events after a per-root cursor
// (doc C Q8, JC2/JC3).
func (s *Store) QueryReadJournal(ctx context.Context, rootID string, afterSeq int64, limit int) ([]query.JournalEventView, error) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	events, err := s.ReadJournal(ctx, s.pool, rootID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	out := make([]query.JournalEventView, 0, len(events))
	for _, e := range events {
		out = append(out, query.JournalEventView{
			EventSeq: e.EventSeq, EventID: e.EventID, RootID: e.RootID,
			GenerationNumber: e.GenerationNumber, IntraGenerationSeq: e.IntraGenerationSeq,
			EventType: e.EventType, ResourceID: e.ResourceID, Payload: e.Payload, CommittedAt: e.CommittedAt,
		})
	}
	return out, nil
}

func scanResourceView(rows pgx.Rows) (query.ResourceView, error) {
	var (
		v        query.ResourceView
		presence string
	)
	if err := rows.Scan(&v.ResourceID, &v.RootID, &v.CanonicalPath, &v.ParentResourceID, &v.Name, &v.IsDir,
		&v.Size, &v.Mtime, &v.ContentHash, &v.ContentType, &presence,
		&v.IntroducedAtGeneration, &v.LastConfirmedGeneration); err != nil {
		return query.ResourceView{}, err
	}
	v.ResourcePresence = domain.ResourcePresence(presence)
	return v, nil
}
