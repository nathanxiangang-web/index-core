package postgres

import (
	"context"


	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// InsertCanonicalResource inserts T3. It never sets a unique path constraint
// (C-C3 REJECTED): live rows MAY share canonical_path (R8 overlap).
func (s *Store) InsertCanonicalResource(ctx context.Context, q Querier, r domain.CanonicalResource) error {
	_, err := q.Exec(ctx,
		`INSERT INTO index_canonical_resource(
		     resource_id, root_id, introduced_at_generation, last_confirmed_generation,
		     resource_presence, removal_evidence_state, missing_since, consecutive_complete_missing,
		     canonical_path, parent_resource_id, name, is_dir, size, mtime,
		     content_hash, hash_algorithm, content_type, current_attributes)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10::uuid, $11, $12, $13, $14, $15, $16, $17, $18)`,
		r.ResourceID, r.RootID, r.IntroducedAtGeneration, r.LastConfirmedGeneration,
		string(r.ResourcePresence), string(r.RemovalEvidenceState), r.MissingSince, r.ConsecutiveCompleteMissing,
		r.CanonicalPath, r.ParentResourceID, r.Name, r.IsDir, r.Size, r.Mtime,
		r.ContentHash, r.HashAlgorithm, r.ContentType, r.CurrentAttributes)
	return err
}

// GetCanonicalResource loads T3 by resource_id.
func (s *Store) GetCanonicalResource(ctx context.Context, q Querier, resourceID string) (domain.CanonicalResource, error) {
	rows, err := s.queryCanonical(ctx, q, `WHERE resource_id = $1::uuid`, resourceID)
	if err != nil {
		return domain.CanonicalResource{}, err
	}
	if len(rows) == 0 {
		return domain.CanonicalResource{}, ErrNotFound
	}
	return rows[0], nil
}

// ListCanonicalResources returns all resources of a root (PRESENT and REMOVED),
// the input to Kernel reconcile (doc B R7). RemovalEvidenceState stays Kernel-internal.
func (s *Store) ListCanonicalResources(ctx context.Context, q Querier, rootID string) ([]domain.CanonicalResource, error) {
	return s.queryCanonical(ctx, q, `WHERE root_id = $1::uuid ORDER BY resource_id`, rootID)
}

// ListCanonicalPresent returns only PRESENT resources (consumer-visible reads, doc C V1).
func (s *Store) ListCanonicalPresent(ctx context.Context, q Querier, rootID string) ([]domain.CanonicalResource, error) {
	return s.queryCanonical(ctx, q,
		`WHERE root_id = $1::uuid AND resource_presence = 'PRESENT' ORDER BY canonical_path, resource_id`, rootID)
}

func (s *Store) queryCanonical(ctx context.Context, q Querier, where string, args ...any) ([]domain.CanonicalResource, error) {
	rows, err := q.Query(ctx,
		`SELECT resource_id::text, root_id::text, introduced_at_generation, last_confirmed_generation,
		        resource_presence, removal_evidence_state, missing_since, consecutive_complete_missing,
		        canonical_path, parent_resource_id::text, name, is_dir, size, mtime,
		        content_hash, hash_algorithm, content_type, current_attributes, created_at, updated_at
		   FROM index_canonical_resource `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CanonicalResource
	for rows.Next() {
		var (
			r                        domain.CanonicalResource
			presence, removalState   string
		)
		if err := rows.Scan(&r.ResourceID, &r.RootID, &r.IntroducedAtGeneration, &r.LastConfirmedGeneration,
			&presence, &removalState, &r.MissingSince, &r.ConsecutiveCompleteMissing,
			&r.CanonicalPath, &r.ParentResourceID, &r.Name, &r.IsDir, &r.Size, &r.Mtime,
			&r.ContentHash, &r.HashAlgorithm, &r.ContentType, &r.CurrentAttributes, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.ResourcePresence = domain.ResourcePresence(presence)
		r.RemovalEvidenceState = domain.RemovalEvidenceState(removalState)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRemovalEvidence updates only the Kernel-internal removal evidence columns;
// it never changes resource_presence (MISSING is additive evidence, not deletion).
func (s *Store) SetRemovalEvidence(ctx context.Context, q Querier, resourceID string, state domain.RemovalEvidenceState, missingSince any, consecutive int32, lastConfirmed int64) error {
	_, err := q.Exec(ctx,
		`UPDATE index_canonical_resource
		    SET removal_evidence_state = $2, missing_since = $3,
		        consecutive_complete_missing = $4, last_confirmed_generation = $5, updated_at = now()
		  WHERE resource_id = $1::uuid`,
		resourceID, string(state), missingSince, consecutive, lastConfirmed)
	return err
}

// ConfirmRemoval sets the logical REMOVED tombstone (C-C5).
func (s *Store) ConfirmRemoval(ctx context.Context, q Querier, resourceID string, generation int64) error {
	_, err := q.Exec(ctx,
		`UPDATE index_canonical_resource
		    SET resource_presence = 'REMOVED', last_confirmed_generation = $2, updated_at = now()
		  WHERE resource_id = $1::uuid`, resourceID, generation)
	return err
}

// UpdateResourcePath applies RENAME/MOVE (path/parent/name), preserving resource_id.
func (s *Store) UpdateResourcePath(ctx context.Context, q Querier, resourceID string, path *string, parentID *string, name *string, generation int64) error {
	_, err := q.Exec(ctx,
		`UPDATE index_canonical_resource
		    SET canonical_path = $2, parent_resource_id = $3::uuid, name = $4,
		        last_confirmed_generation = $5, updated_at = now()
		  WHERE resource_id = $1::uuid`,
		resourceID, path, parentID, name, generation)
	return err
}

// UpdateResourceAttributes updates size/mtime/hash/attributes (UPDATE transition).
func (s *Store) UpdateResourceAttributes(ctx context.Context, q Querier, r domain.CanonicalResource) error {
	_, err := q.Exec(ctx,
		`UPDATE index_canonical_resource
		    SET size = $2, mtime = $3, content_hash = $4, hash_algorithm = $5,
		        content_type = $6, current_attributes = $7,
		        last_confirmed_generation = $8, updated_at = now()
		  WHERE resource_id = $1::uuid`,
		r.ResourceID, r.Size, r.Mtime, r.ContentHash, r.HashAlgorithm,
		r.ContentType, r.CurrentAttributes, r.LastConfirmedGeneration)
	return err
}

// InsertIdentityEvidence appends one IdentityEvidence observation (authoritative history).
func (s *Store) InsertIdentityEvidence(ctx context.Context, q Querier, e domain.IdentityEvidenceObservation) error {
	_, err := q.Exec(ctx,
		`INSERT INTO index_identity_evidence_observation(
		     resource_id, snapshot_id, source_ref, observed_at, provider_object_id,
		     provider_object_id_scope, provider_identity_assurance, content_hash, hash_algorithm,
		     observed_path, observed_parent_ref, size, mtime, is_dir, extra_evidence)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		e.ResourceID, e.SnapshotID, e.SourceRef, e.ObservedAt, e.ProviderObjectID,
		e.ProviderObjectIDScope, string(e.ProviderIdentityAssurance), e.ContentHash, e.HashAlgorithm,
		e.ObservedPath, e.ObservedParentRef, e.Size, e.Mtime, e.IsDir, e.ExtraEvidence)
	return err
}

// PresentResourcesAtPath returns PRESENT resources sharing a canonical path, used
// by resolve_path to surface R8 overlap explicitly (never a silent single winner).
func (s *Store) PresentResourcesAtPath(ctx context.Context, q Querier, rootID, path string) ([]domain.CanonicalResource, error) {
	rows, err := s.queryCanonical(ctx, q,
		`WHERE root_id = $1::uuid AND canonical_path = $2 AND resource_presence = 'PRESENT' ORDER BY resource_id`,
		rootID, path)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
