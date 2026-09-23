package postgres

import (
	"context"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
)

// LoadPriorResources loads canonical resources enriched with their latest
// IdentityEvidence observation, using the caller's Querier so reconcile runs in
// the same consistency domain as canonical inventory (B3, doc A C-E6).
func (s *Store) LoadPriorResources(ctx context.Context, q Querier, rootID string) ([]reconcile.PriorResource, error) {
	rows, err := q.Query(ctx,
		`SELECT c.resource_id::text, c.root_id::text, c.introduced_at_generation, c.last_confirmed_generation,
		        c.resource_presence, c.removal_evidence_state, c.missing_since, c.consecutive_complete_missing,
		        c.missing_first_snapshot_id::text, c.missing_last_snapshot_id::text, c.canonical_path,
		        c.parent_resource_id::text, c.name, c.is_dir, c.size, c.mtime,
		        c.content_hash, c.hash_algorithm, c.content_type, c.current_attributes,
		        c.created_at, c.updated_at,
		        o.provider_object_id, o.provider_object_id_scope, o.provider_identity_assurance
		   FROM index_canonical_resource c
		   LEFT JOIN LATERAL (
		       SELECT provider_object_id, provider_object_id_scope, provider_identity_assurance
		         FROM index_identity_evidence_observation
		        WHERE resource_id = c.resource_id
		        ORDER BY observation_id DESC LIMIT 1
		   ) o ON true
		  WHERE c.root_id = $1::uuid
		  ORDER BY c.resource_id`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []reconcile.PriorResource
	for rows.Next() {
		var (
			p                      reconcile.PriorResource
			presence, removalState string
			assurance              *string
		)
		if err := rows.Scan(&p.ResourceID, &p.RootID, &p.IntroducedAtGeneration, &p.LastConfirmedGeneration,
			&presence, &removalState, &p.MissingSince, &p.ConsecutiveCompleteMissing,
			&p.MissingFirstSnapshotID, &p.MissingLastSnapshotID, &p.CanonicalPath, &p.ParentResourceID,
			&p.Name, &p.IsDir, &p.Size, &p.Mtime,
			&p.ContentHash, &p.HashAlgorithm, &p.ContentType, &p.CurrentAttributes,
			&p.CreatedAt, &p.UpdatedAt,
			&p.ProviderObjectID, &p.ProviderObjectIDScope, &assurance); err != nil {
			return nil, err
		}
		p.ResourcePresence = domain.ResourcePresence(presence)
		p.RemovalEvidenceState = domain.RemovalEvidenceState(removalState)
		if assurance != nil {
			p.ProviderIDAssurance = domain.ProviderIdentityAssurance(*assurance)
		} else {
			p.ProviderIDAssurance = domain.IdentityUnavailable
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AppendObservation appends one versioned IdentityEvidence observation for an
// accepted observation, inside the Stage-2 transaction (B3, doc A C-E2). The
// observation time is the Snapshot observation time, not DB processing time (R2-9).
func (s *Store) AppendObservation(ctx context.Context, q Querier, rootID, snapshotID string, resourceID string, entry domain.SnapshotEntry, observedAt time.Time) error {
	assurance := domain.IdentityUnverified
	if entry.ProviderIdentityAssurance != nil {
		assurance = *entry.ProviderIdentityAssurance
	}
	path := reconcile.EntryPath(entry)
	obs := domain.IdentityEvidenceObservation{
		ResourceID: resourceID, SnapshotID: snapshotID, ObservedAt: observedAt,
		ProviderObjectID: entry.ProviderObjectID, ProviderObjectIDScope: entry.ProviderObjectIDScope,
		ProviderIdentityAssurance: assurance, ContentHash: entry.ContentHash, HashAlgorithm: entry.HashAlgorithm,
		ObservedPath: &path, Size: entry.Size, Mtime: entry.Mtime, IsDir: entry.IsDir,
	}
	return s.InsertIdentityEvidence(ctx, q, obs)
}

// SetSnapshotLifecycleState advances a Snapshot's Kernel-owned lifecycle state.
func (s *Store) SetSnapshotLifecycleState(ctx context.Context, q Querier, snapshotID string, state domain.SnapshotLifecycleState) error {
	_, err := q.Exec(ctx,
		`UPDATE index_snapshot SET lifecycle_state = $2 WHERE snapshot_id = $1::uuid`,
		snapshotID, string(state))
	return err
}
