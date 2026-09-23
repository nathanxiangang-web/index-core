package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// CreateSubmittedSnapshot persists a DRAFT Snapshot, its entries, and the
// DRAFT -> SUBMITTED transition in ONE transaction. An interrupted scan
// therefore leaves either nothing or a fully submitted Snapshot; it never
// creates partial canonical state (the Kernel still owns reconciliation).
func (s *Store) CreateSubmittedSnapshot(ctx context.Context, snap domain.Snapshot, entries []domain.SnapshotEntry) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := s.InsertSnapshotStub(ctx, tx, snap); err != nil {
		return err
	}
	for i := range entries {
		entries[i].SnapshotID = snap.SnapshotID
		if err := s.InsertSnapshotEntry(ctx, tx, entries[i]); err != nil {
			return err
		}
	}
	if err := s.MarkSnapshotSubmitted(ctx, tx, snap.SnapshotID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
