package postgres

import (
	"context"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// AppliedExistsAtGeneration reports whether the given IO3 identity was already
// applied at exactly the given generation. Only this case is an IO3 NO-OP
// (doc A C-AS3); identity match alone is NOT sufficient.
func (s *Store) AppliedExistsAtGeneration(ctx context.Context, q Querier, rootID string, id domain.SnapshotIdentity, generation int64) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM index_applied_snapshot
		    WHERE root_id = $1::uuid AND snapshot_identity_kind = $2
		      AND snapshot_identity_namespace = $3 AND snapshot_identity_version = $4
		      AND snapshot_identity_value = $5 AND applied_generation = $6)`,
		rootID, string(id.Kind), id.Namespace, id.Version, id.Value, generation).Scan(&exists)
	return exists, err
}

// InsertAppliedSnapshot appends one application-history row (never an upsert; C-AS2).
func (s *Store) InsertAppliedSnapshot(ctx context.Context, q Querier, a domain.AppliedSnapshot) error {
	_, err := q.Exec(ctx,
		`INSERT INTO index_applied_snapshot(
		     root_id, snapshot_identity_kind, snapshot_identity_namespace, snapshot_identity_version,
		     snapshot_identity_value, snapshot_id, applied_generation, applied_admission_seq)
		 VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, $8)`,
		a.RootID, string(a.SnapshotIdentityKind), a.SnapshotIdentityNamespace, a.SnapshotIdentityVersion,
		a.SnapshotIdentityValue, a.SnapshotID, a.AppliedGeneration, a.AppliedAdmissionSeq)
	return err
}

// MaxAppliedGeneration returns the greatest applied generation recorded for an
// identity, or 0 if it was never applied (used for re-reconcile reasoning).
func (s *Store) MaxAppliedGeneration(ctx context.Context, q Querier, rootID string, id domain.SnapshotIdentity) (int64, error) {
	var n *int64
	if err := q.QueryRow(ctx,
		`SELECT max(applied_generation) FROM index_applied_snapshot
		  WHERE root_id = $1::uuid AND snapshot_identity_kind = $2
		    AND snapshot_identity_namespace = $3 AND snapshot_identity_version = $4
		    AND snapshot_identity_value = $5`,
		rootID, string(id.Kind), id.Namespace, id.Version, id.Value).Scan(&n); err != nil {
		return 0, err
	}
	if n == nil {
		return 0, nil
	}
	return *n, nil
}