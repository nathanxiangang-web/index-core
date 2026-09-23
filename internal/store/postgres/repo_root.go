package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// CreateRoot inserts T1 index_root. root_id is Kernel-assigned and never reused.
func (s *Store) CreateRoot(ctx context.Context, q Querier, rootID string, scopeDescriptor []byte, lifecycle domain.RootLifecycleState) error {
	_, err := q.Exec(ctx,
		`INSERT INTO index_root(root_id, scope_descriptor, lifecycle_state)
		 VALUES ($1::uuid, $2, $3)`,
		rootID, scopeDescriptor, string(lifecycle))
	return err
}

// GetRoot loads T1 index_root.
func (s *Store) GetRoot(ctx context.Context, q Querier, rootID string) (domain.ResourceRoot, error) {
	var (
		r         domain.ResourceRoot
		lifecycle string
	)
	err := q.QueryRow(ctx,
		`SELECT root_id::text, scope_descriptor, owning_collector_ref, lifecycle_state,
		        current_generation, latest_admission_seq, created_at, updated_at
		   FROM index_root WHERE root_id = $1::uuid`, rootID).Scan(
		&r.RootID, &r.ScopeDescriptor, &r.OwningCollectorRef, &lifecycle,
		&r.CurrentGeneration, &r.LatestAdmissionSeq, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ResourceRoot{}, ErrNotFound
	}
	if err != nil {
		return domain.ResourceRoot{}, err
	}
	r.LifecycleState = domain.RootLifecycleState(lifecycle)
	return r, nil
}

// ListRoots returns roots filtered by the frozen visibility defaults. When
// includeDeprecated/includeDeleted are false, only NEW/ACTIVE are returned (doc C V4..V6).
func (s *Store) ListRoots(ctx context.Context, q Querier, includeDeprecated, includeDeleted bool) ([]domain.ResourceRoot, error) {
	rows, err := q.Query(ctx,
		`SELECT root_id::text, scope_descriptor, owning_collector_ref, lifecycle_state,
		        current_generation, latest_admission_seq, created_at, updated_at
		   FROM index_root
		  WHERE lifecycle_state IN ('NEW','ACTIVE')
		     OR ($1 AND lifecycle_state = 'DEPRECATED')
		     OR ($2 AND lifecycle_state = 'DELETED')
		  ORDER BY created_at, root_id`,
		includeDeprecated, includeDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ResourceRoot
	for rows.Next() {
		var (
			r         domain.ResourceRoot
			lifecycle string
		)
		if err := rows.Scan(&r.RootID, &r.ScopeDescriptor, &r.OwningCollectorRef, &lifecycle,
			&r.CurrentGeneration, &r.LatestAdmissionSeq, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.LifecycleState = domain.RootLifecycleState(lifecycle)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRootLifecycle applies a root lifecycle transition. When advanceGeneration is
// true it also creates the generation record and returns the new generation
// number; the caller is responsible for the journal event in the same transaction
// (doc D Sec 5.1, doc B R11).
func (s *Store) SetRootLifecycle(ctx context.Context, q Querier, rootID string, next domain.RootLifecycleState) error {
	_, err := q.Exec(ctx,
		`UPDATE index_root SET lifecycle_state = $2, updated_at = now()
		  WHERE root_id = $1::uuid`, rootID, string(next))
	return err
}
