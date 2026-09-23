package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/query"
)

const defaultPageSize = 100

// QueryReader is the consumer-facing read-only facade. It holds only a pool and
// exposes no mutating Store method or Pool() accessor, so a consumer cannot reach
// a write bypass around the Kernel (doc C W2/W3, B5). It satisfies query.Reader.
type QueryReader struct {
	pool *pgxpool.Pool
}

// NewQueryReader builds a read-only Query service. Consumers receive the returned
// query.Reader interface, never the concrete Store.
func NewQueryReader(pool *pgxpool.Pool) *QueryReader { return &QueryReader{pool: pool} }

var _ query.Reader = (*QueryReader)(nil)

// ListRoots honors the frozen visibility defaults (doc C V4..V6).
func (qr *QueryReader) ListRoots(ctx context.Context, includeDeprecated, includeDeleted bool) ([]query.RootView, error) {
	rows, err := qr.pool.Query(ctx,
		`SELECT root_id::text, scope_descriptor, lifecycle_state, current_generation, created_at
		   FROM index_root
		  WHERE lifecycle_state IN ('NEW','ACTIVE')
		     OR ($1 AND lifecycle_state = 'DEPRECATED')
		     OR ($2 AND lifecycle_state = 'DELETED')
		  ORDER BY created_at, root_id`, includeDeprecated, includeDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []query.RootView
	for rows.Next() {
		var (
			v         query.RootView
			lifecycle string
		)
		if err := rows.Scan(&v.RootID, &v.ScopeDescriptor, &lifecycle, &v.CurrentGeneration, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.LifecycleState = domain.RootLifecycleState(lifecycle)
		out = append(out, v)
	}
	return out, rows.Err()
}

// RootStatus returns root lifecycle/generation plus the applied high-water mark.
func (qr *QueryReader) RootStatus(ctx context.Context, rootID string) (query.RootStatus, error) {
	var (
		st        query.RootStatus
		lifecycle string
	)
	err := qr.pool.QueryRow(ctx,
		`SELECT root_id::text, lifecycle_state, current_generation FROM index_root WHERE root_id = $1::uuid`,
		rootID).Scan(&st.RootID, &lifecycle, &st.CurrentGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return query.RootStatus{}, ErrNotFound
	}
	if err != nil {
		return query.RootStatus{}, err
	}
	st.LifecycleState = domain.RootLifecycleState(lifecycle)
	var applied *int64
	if err := qr.pool.QueryRow(ctx,
		`SELECT max(admission_seq) FROM index_admission WHERE root_id = $1::uuid AND status = 'APPLIED'`,
		rootID).Scan(&applied); err != nil {
		return query.RootStatus{}, err
	}
	st.LastAppliedAdmissionSeq = applied
	return st, nil
}

// GetResource returns a resource unless it is a REMOVED tombstone and
// includeRemoved is false (doc C V1/V2, QC2/QC3).
func (qr *QueryReader) GetResource(ctx context.Context, resourceID string, includeRemoved bool) (*query.ResourceView, error) {
	var (
		v        query.ResourceView
		presence string
	)
	err := qr.pool.QueryRow(ctx,
		resourceSelect+` WHERE resource_id = $1::uuid`, resourceID).Scan(resourceScanArgs(&v, &presence)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if domain.ResourcePresence(presence) == domain.ResourceRemoved && !includeRemoved {
		return nil, nil
	}
	v.ResourcePresence = domain.ResourcePresence(presence)
	return &v, nil
}

// ResolvePath returns ALL live resources at a path with explicit ambiguity
// (doc C V9, QC12); it never fabricates a single winner.
func (qr *QueryReader) ResolvePath(ctx context.Context, rootID, path string, includeRemoved bool) (query.PathResolution, error) {
	var res query.PathResolution
	collect := func(presence domain.ResourcePresence) error {
		rows, err := qr.pool.Query(ctx,
			resourceSelect+` WHERE root_id = $1::uuid AND canonical_path = $2 AND resource_presence = $3 ORDER BY resource_id`,
			rootID, path, string(presence))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				v  query.ResourceView
				pr string
			)
			if err := rows.Scan(resourceScanArgs(&v, &pr)...); err != nil {
				return err
			}
			v.ResourcePresence = domain.ResourcePresence(pr)
			res.Matches = append(res.Matches, v)
		}
		return rows.Err()
	}
	if err := collect(domain.ResourcePresent); err != nil {
		return query.PathResolution{}, err
	}
	if includeRemoved {
		if err := collect(domain.ResourceRemoved); err != nil {
			return query.PathResolution{}, err
		}
	}
	res.Ambiguous = len(res.Matches) > 1
	return res, nil
}

// ListActivePage returns PRESENT resources with generation-bound pagination.
func (qr *QueryReader) ListActivePage(ctx context.Context, rootID string, cur *query.Cursor, limit int) (query.ResourcePage, error) {
	return qr.listPage(ctx, rootID, cur, limit, domain.ResourcePresent)
}

// ListRemovedPage returns REMOVED tombstones (explicit history access, doc C Q7).
func (qr *QueryReader) ListRemovedPage(ctx context.Context, rootID string, cur *query.Cursor, limit int) (query.ResourcePage, error) {
	return qr.listPage(ctx, rootID, cur, limit, domain.ResourceRemoved)
}

// listPage reads the generation and rows inside ONE read-only REPEATABLE READ
// transaction, so a page can never mix generation G metadata with G+1 rows
// (doc C CR1/P2/P3, B5).
func (qr *QueryReader) listPage(ctx context.Context, rootID string, cur *query.Cursor, limit int, presence domain.ResourcePresence) (query.ResourcePage, error) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	tx, err := qr.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return query.ResourcePage{}, err
	}
	defer tx.Rollback(ctx)

	var currentGeneration int64
	if err := tx.QueryRow(ctx,
		`SELECT current_generation FROM index_root WHERE root_id = $1::uuid`, rootID).Scan(&currentGeneration); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return query.ResourcePage{}, ErrNotFound
		}
		return query.ResourcePage{}, err
	}
	if cur != nil && (cur.RootID != rootID || cur.Generation != currentGeneration) {
		return query.ResourcePage{}, query.ErrStaleCursor
	}

	base := resourceSelect + ` WHERE root_id = $1::uuid AND resource_presence = $2`
	var rows pgx.Rows
	if cur != nil && cur.HasAfter {
		rows, err = tx.Query(ctx, base+`
		  AND (COALESCE(canonical_path, ''), resource_id::text) > ($3::text, $4::text)
		  ORDER BY COALESCE(canonical_path, ''), resource_id::text
		  LIMIT $5`, rootID, string(presence), cur.AfterPath, cur.AfterResourceID, limit)
	} else {
		rows, err = tx.Query(ctx, base+`
		  ORDER BY COALESCE(canonical_path, ''), resource_id::text
		  LIMIT $3`, rootID, string(presence), limit)
	}
	if err != nil {
		return query.ResourcePage{}, err
	}
	defer rows.Close()

	page := query.ResourcePage{}
	for rows.Next() {
		var (
			v  query.ResourceView
			pr string
		)
		if err := rows.Scan(resourceScanArgs(&v, &pr)...); err != nil {
			return query.ResourcePage{}, err
		}
		v.ResourcePresence = domain.ResourcePresence(pr)
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
			RootID: rootID, Generation: currentGeneration,
			HasAfter: true, AfterPath: p, AfterResourceID: last.ResourceID,
		}
	}
	return page, nil
}

// ReadJournal returns per-root journal events after a per-root cursor (doc C Q8).
func (qr *QueryReader) ReadJournal(ctx context.Context, rootID string, afterSeq int64, limit int) ([]query.JournalEventView, error) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	rows, err := qr.pool.Query(ctx,
		`SELECT root_id::text, event_seq, event_id, generation_number, intra_generation_seq,
		        event_type, resource_id::text, payload, committed_at
		   FROM index_journal_event
		  WHERE root_id = $1::uuid AND event_seq > $2
		  ORDER BY event_seq
		  LIMIT $3`, rootID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []query.JournalEventView
	for rows.Next() {
		var (
			v         query.JournalEventView
			eventType string
		)
		if err := rows.Scan(&v.RootID, &v.EventSeq, &v.EventID, &v.GenerationNumber, &v.IntraGenerationSeq,
			&eventType, &v.ResourceID, &v.Payload, &v.CommittedAt); err != nil {
			return nil, err
		}
		v.EventType = domain.EventType(eventType)
		out = append(out, v)
	}
	return out, rows.Err()
}

const resourceSelect = `SELECT resource_id::text, root_id::text, canonical_path, parent_resource_id::text, name, is_dir,
	       size, mtime, content_hash, content_type, resource_presence,
	       introduced_at_generation, last_confirmed_generation
	  FROM index_canonical_resource`

func resourceScanArgs(v *query.ResourceView, presence *string) []any {
	return []any{&v.ResourceID, &v.RootID, &v.CanonicalPath, &v.ParentResourceID, &v.Name, &v.IsDir,
		&v.Size, &v.Mtime, &v.ContentHash, &v.ContentType, presence,
		&v.IntroducedAtGeneration, &v.LastConfirmedGeneration}
}
