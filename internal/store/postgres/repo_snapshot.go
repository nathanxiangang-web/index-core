package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// InsertSnapshotStub writes a DRAFT snapshot row with the Collector-declared
// evidence. scope_shrink_corroboration and acceptance_state stay NULL until the
// Kernel evaluates (SUBMITTED -> EVALUATED).
func (s *Store) InsertSnapshotStub(ctx context.Context, q Querier, snap domain.Snapshot) error {
	_, err := q.Exec(ctx,
		`INSERT INTO index_snapshot(
		     snapshot_id, root_id, provenance, observed_at, started_at, finished_at,
		     traversal_status, error_summary, skipped_scopes, skipped_scopes_known_empty,
		     freshness_evidence, collector_completeness_assurance,
		     completeness_flag, lifecycle_state, entry_count, byte_count, generation_hint)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		snap.SnapshotID, snap.RootID, snap.Provenance, snap.ObservedAt, snap.StartedAt, snap.FinishedAt,
		string(snap.TraversalStatus), snap.ErrorSummary, snap.SkippedScopes, snap.SkippedScopesKnownEmpty,
		nullableString(snap.FreshnessEvidence), nullableString(snap.CollectorCompletenessAssurance),
		string(snap.CompletenessFlag), string(snap.LifecycleState), snap.EntryCount, snap.ByteCount, snap.GenerationHint)
	return err
}

// MarkSnapshotSubmitted moves DRAFT -> SUBMITTED (the only Collector-permitted transition).
func (s *Store) MarkSnapshotSubmitted(ctx context.Context, q Querier, snapshotID string) error {
	tag, err := q.Exec(ctx,
		`UPDATE index_snapshot SET lifecycle_state = 'SUBMITTED'
		  WHERE snapshot_id = $1::uuid AND lifecycle_state = 'DRAFT'`, snapshotID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// SetSnapshotEvaluated sets the Kernel-owned verdict once (SUBMITTED -> EVALUATED):
// acceptance_state and the Kernel-derived scope_shrink_corroboration. These
// columns are not Collector-writable (doc A Sec 3.5).
func (s *Store) SetSnapshotEvaluated(ctx context.Context, q Querier, snapshotID string, acceptance domain.AcceptanceState, corroboration *domain.ScopeShrinkCorroboration) error {
	tag, err := q.Exec(ctx,
		`UPDATE index_snapshot
		    SET acceptance_state = $2, scope_shrink_corroboration = $3, lifecycle_state = 'EVALUATED'
		  WHERE snapshot_id = $1::uuid AND lifecycle_state = 'SUBMITTED'`,
		snapshotID, string(acceptance), nullableString(corroboration))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// GetSnapshot loads T5 index_snapshot.
func (s *Store) GetSnapshot(ctx context.Context, q Querier, snapshotID string) (domain.Snapshot, error) {
	var (
		snap                                          domain.Snapshot
		traversal, completeness, lifecycle            string
		freshness, assurance, corroboration, accept   *string
	)
	err := q.QueryRow(ctx,
		`SELECT snapshot_id::text, root_id::text, provenance, observed_at, started_at, finished_at,
		        traversal_status, error_summary, skipped_scopes, skipped_scopes_known_empty,
		        freshness_evidence, collector_completeness_assurance, scope_shrink_corroboration,
		        completeness_flag, acceptance_state, lifecycle_state, entry_count, byte_count,
		        generation_hint, created_at
		   FROM index_snapshot WHERE snapshot_id = $1::uuid`, snapshotID).Scan(
		&snap.SnapshotID, &snap.RootID, &snap.Provenance, &snap.ObservedAt, &snap.StartedAt, &snap.FinishedAt,
		&traversal, &snap.ErrorSummary, &snap.SkippedScopes, &snap.SkippedScopesKnownEmpty,
		&freshness, &assurance, &corroboration, &completeness, &accept, &lifecycle,
		&snap.EntryCount, &snap.ByteCount, &snap.GenerationHint, &snap.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Snapshot{}, ErrNotFound
	}
	if err != nil {
		return domain.Snapshot{}, err
	}
	snap.TraversalStatus = domain.TraversalStatus(traversal)
	snap.CompletenessFlag = domain.CompletenessFlag(completeness)
	snap.LifecycleState = domain.SnapshotLifecycleState(lifecycle)
	if freshness != nil {
		v := domain.FreshnessEvidence(*freshness)
		snap.FreshnessEvidence = &v
	}
	if assurance != nil {
		v := domain.FailureVisibility(*assurance)
		snap.CollectorCompletenessAssurance = &v
	}
	if corroboration != nil {
		v := domain.ScopeShrinkCorroboration(*corroboration)
		snap.ScopeShrinkCorroboration = &v
	}
	if accept != nil {
		v := domain.AcceptanceState(*accept)
		snap.AcceptanceState = &v
	}
	return snap, nil
}

// InsertSnapshotEntry upserts one normalized entry (audit/replay).
func (s *Store) InsertSnapshotEntry(ctx context.Context, q Querier, e domain.SnapshotEntry) error {
	_, err := q.Exec(ctx,
		`INSERT INTO index_snapshot_entry(
		     snapshot_id, entry_local_id, name, parent_ref, is_dir, size, mtime,
		     content_hash, hash_algorithm, provider_object_id, provider_object_id_scope,
		     content_type, extra_evidence)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		e.SnapshotID, e.EntryLocalID, e.Name, e.ParentRef, e.IsDir, e.Size, e.Mtime,
		e.ContentHash, e.HashAlgorithm, e.ProviderObjectID, e.ProviderObjectIDScope,
		e.ContentType, e.ExtraEvidence)
	return err
}

// ListSnapshotEntries returns a snapshot's entries ordered deterministically.
func (s *Store) ListSnapshotEntries(ctx context.Context, q Querier, snapshotID string) ([]domain.SnapshotEntry, error) {
	rows, err := q.Query(ctx,
		`SELECT snapshot_id::text, entry_local_id, name, parent_ref, is_dir, size, mtime,
		        content_hash, hash_algorithm, provider_object_id, provider_object_id_scope,
		        content_type, extra_evidence
		   FROM index_snapshot_entry WHERE snapshot_id = $1::uuid
		  ORDER BY parent_ref, name, entry_local_id`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SnapshotEntry
	for rows.Next() {
		var e domain.SnapshotEntry
		if err := rows.Scan(&e.SnapshotID, &e.EntryLocalID, &e.Name, &e.ParentRef, &e.IsDir, &e.Size, &e.Mtime,
			&e.ContentHash, &e.HashAlgorithm, &e.ProviderObjectID, &e.ProviderObjectIDScope,
			&e.ContentType, &e.ExtraEvidence); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountCanonicalPresent returns the number of PRESENT resources in a root, used
// as prior-canonical context for the completeness gate (doc A Sec 3.5, Gate 1B C-7/C-8).
func (s *Store) CountCanonicalPresent(ctx context.Context, q Querier, rootID string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx,
		`SELECT count(*) FROM index_canonical_resource
		  WHERE root_id = $1::uuid AND resource_presence = 'PRESENT'`, rootID).Scan(&n)
	return n, err
}