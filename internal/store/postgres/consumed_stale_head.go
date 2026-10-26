package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// ErrStaleHeadConsumed is progress by another in-process admission actor, NOT
// successful coverage and NOT permission to verify/requeue Work. Unlike an
// APPLIED/NOOP receipt, a STALE_INPUT result has no persisted generation. Do not
// manufacture an original ReconcileOutcome from the current Root generation.
var ErrStaleHeadConsumed = errors.New("selected stale head already durably consumed")

// consumedStaleHead positively checks the exact SELECTed (root,seq,snapshot)
// after Stage2 lost the FIFO head race. Root lock serializes the entire evidence
// read. No Canonical, Work, admission, result or original source time is changed.
// This is deliberately NOT used by ProcessSnapshot/AdmitOrResumeSnapshot.
func (s *Store) consumedStaleHead(ctx context.Context, rootID string, seq int64, snapshotID string) (bool, error) {
	if seq <= 0 {
		return false, ErrTerminalSnapshot
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var latest int64
	if err = tx.QueryRow(ctx, `SELECT latest_admission_seq FROM index_root WHERE root_id=$1::uuid FOR UPDATE`, rootID).Scan(&latest); err != nil {
		return false, err
	}
	var root, lifecycle string
	if err = tx.QueryRow(ctx, `SELECT root_id::text,lifecycle_state FROM index_snapshot WHERE snapshot_id=$1::uuid`, snapshotID).Scan(&root, &lifecycle); err != nil {
		return false, err
	}
	if root != rootID || seq > latest {
		return false, ErrTerminalSnapshot
	}
	if lifecycle != string(domain.SnapshotRejected) {
		return false, nil // keep the original head error; do not guess a receipt
	}
	var count int64
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(bool_and(root_id=$1::uuid AND admission_seq=$2 AND status='STALE_INPUT' AND applied_generation IS NULL),false)
 FROM index_admission WHERE snapshot_id=$3::uuid`, rootID, seq, snapshotID).Scan(&count, &valid); err != nil {
		return false, err
	}
	if count != 1 || !valid {
		return false, ErrTerminalSnapshot
	}
	if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(bool_and(outcome='STALE_INPUT' AND generation_number IS NULL AND counts='{}'::jsonb AND conflicts IS NULL AND rejection_reason='superseded input'),false)
 FROM index_reconcile_result WHERE root_id=$1::uuid AND admission_seq=$2`, rootID, seq).Scan(&count, &valid); err != nil {
		return false, err
	}
	if count != 1 || !valid {
		return false, ErrTerminalSnapshot
	}
	var applied, generation bool
	if err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM index_applied_snapshot WHERE snapshot_id=$2::uuid OR (root_id=$1::uuid AND applied_admission_seq=$3)),
 EXISTS(SELECT 1 FROM index_generation WHERE produced_by_snapshot_id=$2::uuid OR (root_id=$1::uuid AND produced_by_admission_seq=$3))`, rootID, snapshotID, seq).Scan(&applied, &generation); err != nil {
		return false, err
	}
	if applied || generation {
		return false, ErrTerminalSnapshot
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
