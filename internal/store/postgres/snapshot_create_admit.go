package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// CreateSubmittedSnapshotAndAdmit persists DRAFT + entries, transitions
// DRAFT -> SUBMITTED, allocates the authoritative admission_seq, and inserts the
// PENDING admission in ONE short per-root transaction (G3-R2.1). This removes the
// ambiguous SUBMITTED-but-unadmitted state from the normal path: admission order
// is authoritative at the moment SUBMITTED work becomes executable, with no
// reliance on DB timestamps.
func (s *Store) CreateSubmittedSnapshotAndAdmit(ctx context.Context, snap domain.Snapshot, entries []domain.SnapshotEntry) (int64, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// Lock the root first so lifecycle/admission allocation is serialized.
	var lifecycle string
	if err := tx.QueryRow(ctx,
		`SELECT lifecycle_state FROM index_root WHERE root_id = $1::uuid FOR UPDATE`, snap.RootID).Scan(&lifecycle); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}

	if err := s.InsertSnapshotStub(ctx, tx, snap); err != nil {
		return 0, err
	}
	for i := range entries {
		entries[i].SnapshotID = snap.SnapshotID
		if err := s.InsertSnapshotEntry(ctx, tx, entries[i]); err != nil {
			return 0, err
		}
	}
	if err := s.MarkSnapshotSubmitted(ctx, tx, snap.SnapshotID); err != nil {
		return 0, err
	}

	var seq int64
	if err := tx.QueryRow(ctx,
		`UPDATE index_root SET latest_admission_seq = latest_admission_seq + 1, updated_at = now()
		  WHERE root_id = $1::uuid RETURNING latest_admission_seq`, snap.RootID).Scan(&seq); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO index_admission(root_id, admission_seq, snapshot_id, status)
		 VALUES ($1::uuid, $2, $3::uuid, 'PENDING')`, snap.RootID, seq, snap.SnapshotID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return seq, nil
}
