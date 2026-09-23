package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/query"
)

const defaultPageSize = 100

// QueryReader is the consumer-facing read-only facade (doc C W2/W3, B5).
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

// GetRoot returns a single root honoring the independent visibility opts (Q1).
func (qr *QueryReader) GetRoot(ctx context.Context, rootID string, opts query.ReadOptions) (*query.RootView, error) {
	v, err := scanRoot(qr.pool.QueryRow(ctx, rootSelect+` WHERE root_id = $1::uuid`, rootID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !rootVisible(v.LifecycleState, opts) {
		return nil, nil
	}
	return &v, nil
}

// RootStatus returns root status, honoring root visibility (Q9). The two reads
// run in ONE read-only consistent transaction (CR1, R3-7).
func (qr *QueryReader) RootStatus(ctx context.Context, rootID string, opts query.ReadOptions) (query.RootStatus, error) {
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
	if !rootVisible(st.LifecycleState, opts) {
		return query.RootStatus{}, ErrNotFound
	}
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
// opts.IncludeRemoved is false, and hides children of non-visible roots unless
// the corresponding root opt-in is set (R3-7).
func (qr *QueryReader) GetResource(ctx context.Context, resourceID string, opts query.ReadOptions) (*query.ResourceView, error) {
	v, err := scanResource(qr.pool.QueryRow(ctx,
		resourceSelect+` WHERE c.resource_id = $1::uuid AND `+rootVisibilityClause(opts), resourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v.ResourcePresence == domain.ResourceRemoved && !opts.IncludeRemoved {
		return nil, nil
	}
	return &v, nil
}

// ListResources returns hierarchy children of a parent (root scope when
// parentResourceID is nil) with generation-bound pagination (Q4, R3-7).
func (qr *QueryReader) ListResources(ctx context.Context, rootID string, parentResourceID *string, opts query.ReadOptions, cur *query.Cursor, limit int) (query.ResourcePage, error) {
	presence := `c.resource_presence = 'PRESENT'`
	if opts.IncludeRemoved {
		presence = `c.resource_presence IN ('PRESENT','REMOVED')`
	}
	return qr.listPageInternal(ctx, rootID, parentResourceID, parentResourceID == nil, presence, rootVisibilityClause(opts), cur, limit)
}

// ResolvePath returns ALL live resources at a path with explicit ambiguity
// (doc C V9, QC12), read in ONE read-only consistent transaction (CR1, R3-7).
func (qr *QueryReader) ResolvePath(ctx context.Context, rootID, path string, opts query.ReadOptions) (query.PathResolution, error) {
	tx, err := qr.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return query.PathResolution{}, err
	}
	defer tx.Rollback(ctx)

	var res query.PathResolution
	presences := []domain.ResourcePresence{domain.ResourcePresent}
	if opts.IncludeRemoved {
		presences = append(presences, domain.ResourceRemoved)
	}
	for _, presence := range presences {
		rows, err := tx.Query(ctx,
			resourceSelect+` WHERE c.root_id = $1::uuid AND c.canonical_path = $2 AND c.resource_presence = $3 AND `+
				rootVisibilityClause(opts)+` ORDER BY c.resource_id`,
			rootID, path, string(presence))
		if err != nil {
			return query.PathResolution{}, err
		}
		for rows.Next() {
			v, err := scanResource(rows)
			if err != nil {
				rows.Close()
				return query.PathResolution{}, err
			}
			res.Matches = append(res.Matches, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return query.PathResolution{}, err
		}
	}
	res.Ambiguous = len(res.Matches) > 1
	return res, nil
}

// ListActivePage returns PRESENT resources with generation-bound pagination.
func (qr *QueryReader) ListActivePage(ctx context.Context, rootID string, cur *query.Cursor, limit int) (query.ResourcePage, error) {
	return qr.listPageInternal(ctx, rootID, nil, false, `c.resource_presence = 'PRESENT'`, rootVisibilityClause(query.ReadOptions{}), cur, limit)
}

// ListRemovedPage returns REMOVED tombstones (explicit history).
func (qr *QueryReader) ListRemovedPage(ctx context.Context, rootID string, cur *query.Cursor, limit int) (query.ResourcePage, error) {
	opts := query.ReadOptions{IncludeRemoved: true, IncludeDeprecatedRoot: true, IncludeDeletedRoot: true}
	return qr.listPageInternal(ctx, rootID, nil, false, `c.resource_presence = 'REMOVED'`, rootVisibilityClause(opts), cur, limit)
}

// listPageInternal reads the generation and rows inside ONE read-only REPEATABLE
// READ transaction; the cursor is generation-bound (CR1/P2/P3, R3-7).
func (qr *QueryReader) listPageInternal(ctx context.Context, rootID string, parentID *string, rootLevelOnly bool, presencePredicate, rootVisibility string, cur *query.Cursor, limit int) (query.ResourcePage, error) {
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

	args := []any{rootID}
	where := `c.root_id = $1::uuid AND ` + presencePredicate + ` AND ` + rootVisibility
	// Q4 root-level listing filters on parent IS NULL; Q6/Q7 (whole-root reads)
	// apply no parent filter (R4-3).
	switch {
	case rootLevelOnly:
		where += ` AND c.parent_resource_id IS NULL`
	case parentID != nil:
		args = append(args, *parentID)
		where += fmt.Sprintf(` AND c.parent_resource_id = $%d::uuid`, len(args))
	}
	if cur != nil && cur.HasAfter {
		args = append(args, cur.AfterPath, cur.AfterResourceID)
		where += fmt.Sprintf(` AND (COALESCE(c.canonical_path,''), c.resource_id::text) > ($%d::text, $%d::text)`,
			len(args)-1, len(args))
	}
	args = append(args, limit)
	q := resourceSelect + ` WHERE ` + where +
		` ORDER BY COALESCE(c.canonical_path,''), c.resource_id::text LIMIT ` + fmt.Sprintf(`$%d`, len(args))

	rows, err := tx.Query(ctx, q, args...)
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
		page.Next = &query.Cursor{RootID: rootID, Generation: currentGeneration, HasAfter: true, AfterPath: p, AfterResourceID: last.ResourceID}
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

// rootVisibilityClause builds a root-visibility predicate from the independent
// read options (R3-7). Values are fixed literals (no injection).
func rootVisibilityClause(opts query.ReadOptions) string {
	pred := `r.lifecycle_state IN ('NEW','ACTIVE')`
	if opts.IncludeDeprecatedRoot {
		pred += ` OR r.lifecycle_state = 'DEPRECATED'`
	}
	if opts.IncludeDeletedRoot {
		pred += ` OR r.lifecycle_state = 'DELETED'`
	}
	return `EXISTS (SELECT 1 FROM index_root r WHERE r.root_id = c.root_id AND (` + pred + `))`
}

func rootVisible(lc domain.RootLifecycleState, opts query.ReadOptions) bool {
	switch lc {
	case domain.RootDeprecated:
		return opts.IncludeDeprecatedRoot
	case domain.RootDeleted:
		return opts.IncludeDeletedRoot
	default:
		return true
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
