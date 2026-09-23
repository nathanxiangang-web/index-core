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
// exposes no mutating Store method or Pool() accessor (doc C W2/W3, B5).
type QueryReader struct {
	pool *pgxpool.Pool
}

// NewQueryReader builds a read-only Query service.
func NewQueryReader(pool *pgxpool.Pool) *QueryReader { return &QueryReader{pool: pool} }

var _ query.Reader = (*QueryReader)(nil)

// ListRoots honors the frozen visibility defaults (doc C V4..V6).
func (qr *QueryReader) ListRoots(ctx context.Context, includeDeprecated, includeDeleted bool) ([]query.RootView, error) {
	rows, err := qr.pool.Query(ctx, rootSelect+
		` WHERE lifecycle_state IN ('NEW','ACTIVE')
		     OR ($1 AND lifecycle_state = 'DEPRECATED')
		     OR ($2 AND lifecycle_state = 'DELETED')
		  ORDER BY created_at, root_id`, includeDeprecated, includeDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []query.RootView
	for rows.Next() {
		v, err := scanRoot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetRoot returns a single root honoring the frozen visibility defaults (Q1).
func (qr *QueryReader) GetRoot(ctx context.Context, rootID string, includeDeprecated, includeDeleted bool) (*query.RootView, error) {
	v, err := scanRoot(qr.pool.QueryRow(ctx, rootSelect+` WHERE root_id = $1::uuid`, rootID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch v.LifecycleState {
	case domain.RootDeprecated:
		if !includeDeprecated {
			return nil, nil
		}
	case domain.RootDeleted:
		if !includeDeleted {
			return nil, nil
		}
	}
	return &v, nil
}

// RootStatus returns root lifecycle/generation plus the applied high-water mark,
// read in ONE read-only consistent transaction (CR1, R2-11).
func (qr *QueryReader) RootStatus(ctx context.Context, rootID string) (query.RootStatus, error) {
	tx, err := qr.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return query.RootStatus{}, err
	}
	defer tx.Rollback(ctx)

	var (
		st        query.RootStatus
		lifecycle string
	)
	if err := tx.QueryRow(ctx,
		`SELECT root_id::text, lifecycle_state, current_generation FROM index_root WHERE root_id = $1::uuid`,
		rootID).Scan(&st.RootID, &lifecycle, &st.CurrentGeneration); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return query.RootStatus{}, ErrNotFound
		}
		return query.RootStatus{}, err
	}
	st.LifecycleState = domain.RootLifecycleState(lifecycle)
	var applied *int64
	if err := tx.QueryRow(ctx,
		`SELECT max(admission_seq) FROM index_admission WHERE root_id = $1::uuid AND status = 'APPLIED'`,
		rootID).Scan(&applied); err != nil {
		return query.RootStatus{}, err
	}
	st.LastAppliedAdmissionSeq = applied
	return st, nil
}

// GetResource returns a resource unless it is a REMOVED tombstone and
// includeRemoved is false (doc C V1/V2). Default reads do not leak children of a
// DEPRECATED/DELETED root; explicit history access (includeRemoved) may (R2-11).
func (qr *QueryReader) GetResource(ctx context.Context, resourceID string, includeRemoved bool) (*query.ResourceView, error) {
	v, err := scanResource(qr.pool.QueryRow(ctx,
		resourceSelect+` WHERE c.resource_id = $1::uuid AND `+rootVisibility(includeRemoved), resourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v.ResourcePresence == domain.ResourceRemoved && !includeRemoved {
		return nil, nil
	}
	return &v, nil
}

// ListResources returns the hierarchy children of a parent (root scope when
// parentResourceID is nil) (Q4).
func (qr *QueryReader) ListResources(ctx context.Context, rootID string, parentResourceID *string, includeRemoved bool, limit int) (query.ResourcePage, error) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	presence := domain.ResourcePresent
	if includeRemoved {
		// still default to PRESENT children unless the caller wants removed too;
		// Q4 is the live hierarchy surface.
		presence = domain.ResourcePresent
	}
	q := resourceSelect + ` WHERE c.root_id = $1::uuid AND c.resource_presence = $2 AND ` + rootVisibility(includeRemoved)
	args := []any{rootID, string(presence)}
	if parentResourceID == nil {
		q += ` AND c.parent_resource_id IS NULL`
	} else {
		q += ` AND c.parent_resource_id = $3::uuid`
		args = append(args, *parentResourceID)
	}
	q += ` ORDER BY COALESCE(c.canonical_path,''), c.resource_id::text LIMIT ` + placeholders(len(args)+1)
	args = append(args, limit)

	rows, err := qr.pool.Query(ctx, q, args...)
	if err != nil {
		return query.ResourcePage{}, err
	}
	defer rows.Close()
	page := query.ResourcePage{}
	for rows.Next() {
		v, err := scanResource(rows)
		if err != nil {
			return query.ResourcePage{}, err
		}
		page.Items = append(page.Items, v)
	}
	return page, rows.Err()
}

// ResolvePath returns ALL live resources at a path with explicit ambiguity
// (doc C V9, QC12), read in ONE read-only consistent transaction (CR1, R2-11).
func (qr *QueryReader) ResolvePath(ctx context.Context, rootID, path string, includeRemoved bool) (query.PathResolution, error) {
	tx, err := qr.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return query.PathResolution{}, err
	}
	defer tx.Rollback(ctx)

	var res query.PathResolution
	collect := func(presence domain.ResourcePresence) error {
		rows, err := tx.Query(ctx,
			resourceSelect+` WHERE c.root_id = $1::uuid AND c.canonical_path = $2 AND c.resource_presence = $3 AND `+
				rootVisibility(includeRemoved)+` ORDER BY c.resource_id`,
			rootID, path, string(presence))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanResource(rows)
			if err != nil {
				return err
			}
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
	return qr.listPage(ctx, rootID, cur, limit, domain.ResourcePresent, false)
}

// ListRemovedPage returns REMOVED tombstones (explicit history; audit access may
// read the retained partition of a DELETED root).
func (qr *QueryReader) ListRemovedPage(ctx context.Context, rootID string, cur *query.Cursor, limit int) (query.ResourcePage, error) {
	return qr.listPage(ctx, rootID, cur, limit, domain.ResourceRemoved, true)
}

// listPage reads the generation and rows inside ONE read-only REPEATABLE READ
// transaction, so a page can never mix generation G metadata with G+1 rows
// (doc C CR1/P2/P3, B5).
func (qr *QueryReader) listPage(ctx context.Context, rootID string, cur *query.Cursor, limit int, presence domain.ResourcePresence, allowHiddenRoots bool) (query.ResourcePage, error) {
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

	base := resourceSelect + ` WHERE c.root_id = $1::uuid AND c.resource_presence = $2 AND ` + rootVisibility(allowHiddenRoots)
	var rows pgx.Rows
	if cur != nil && cur.HasAfter {
		rows, err = tx.Query(ctx, base+`
		  AND (COALESCE(c.canonical_path, ''), c.resource_id::text) > ($3::text, $4::text)
		  ORDER BY COALESCE(c.canonical_path, ''), c.resource_id::text
		  LIMIT $5`, rootID, string(presence), cur.AfterPath, cur.AfterResourceID, limit)
	} else {
		rows, err = tx.Query(ctx, base+`
		  ORDER BY COALESCE(c.canonical_path, ''), c.resource_id::text
		  LIMIT $3`, rootID, string(presence), limit)
	}
	if err != nil {
		return query.ResourcePage{}, err
	}
	defer rows.Close()

	page := query.ResourcePage{}
	for rows.Next() {
		v, err := scanResource(rows)
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

const rootSelect = `SELECT root_id::text, scope_descriptor, lifecycle_state, current_generation, created_at
	  FROM index_root`

const resourceSelect = `SELECT c.resource_id::text, c.root_id::text, c.canonical_path, c.parent_resource_id::text,
	       c.name, c.is_dir, c.size, c.mtime, c.content_hash, c.content_type, c.resource_presence,
	       c.introduced_at_generation, c.last_confirmed_generation
	  FROM index_canonical_resource c`

// rootVisibility filters out resources whose root is DEPRECATED/DELETED unless
// audit access (allowHiddenRoots) is requested (R2-11).
func rootVisibility(allowHiddenRoots bool) string {
	if allowHiddenRoots {
		return `EXISTS (SELECT 1 FROM index_root r WHERE r.root_id = c.root_id)`
	}
	return `EXISTS (SELECT 1 FROM index_root r WHERE r.root_id = c.root_id AND r.lifecycle_state IN ('NEW','ACTIVE'))`
}

func placeholders(n int) string {
	switch n {
	case 1:
		return "$1"
	case 2:
		return "$2"
	case 3:
		return "$3"
	default:
		return "$4"
	}
}

func scanRoot(row interface{ Scan(...any) error }) (query.RootView, error) {
	var (
		v         query.RootView
		lifecycle string
	)
	if err := row.Scan(&v.RootID, &v.ScopeDescriptor, &lifecycle, &v.CurrentGeneration, &v.CreatedAt); err != nil {
		return query.RootView{}, err
	}
	v.LifecycleState = domain.RootLifecycleState(lifecycle)
	return v, nil
}

func scanResource(row interface{ Scan(...any) error }) (query.ResourceView, error) {
	var (
		v        query.ResourceView
		presence string
	)
	if err := row.Scan(&v.ResourceID, &v.RootID, &v.CanonicalPath, &v.ParentResourceID,
		&v.Name, &v.IsDir, &v.Size, &v.Mtime, &v.ContentHash, &v.ContentType, &presence,
		&v.IntroducedAtGeneration, &v.LastConfirmedGeneration); err != nil {
		return query.ResourceView{}, err
	}
	v.ResourcePresence = domain.ResourcePresence(presence)
	return v, nil
}
