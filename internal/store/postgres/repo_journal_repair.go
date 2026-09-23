package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// RepairJournal runs a J6 Journal-repair transaction: it appends a corrective
// event that reasserts current canonical truth WITHOUT mutating Canonical
// Inventory and WITHOUT advancing the generation (doc D Sec 8.2).
//
// It acquires the same per-root serialization guard as a reconcile, and is
// permitted on a DELETED root because the R4 DELETED guard blocks only new
// external Snapshot reconciles, not internal Journal audit repair (doc D JD16).
// The corrective event carries the current (unchanged) generation, takes the
// next per-root event_seq, and its intra_generation_seq is allocated after the
// current maximum for that generation (never restarted at 1).
func (s *Store) RepairJournal(ctx context.Context, rootID string, corrective domain.JournalEvent) (domain.JournalEvent, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return domain.JournalEvent{}, err
	}
	defer tx.Rollback(ctx)

	var currentGen int64
	if err := tx.QueryRow(ctx,
		`SELECT current_generation FROM index_root WHERE root_id = $1::uuid FOR UPDATE`,
		rootID).Scan(&currentGen); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.JournalEvent{}, ErrNotFound
		}
		return domain.JournalEvent{}, err
	}

	seq, err := s.NextEventSeq(ctx, tx, rootID)
	if err != nil {
		return domain.JournalEvent{}, err
	}
	intra, err := s.NextIntraGenerationSeq(ctx, tx, rootID, currentGen)
	if err != nil {
		return domain.JournalEvent{}, err
	}

	corrective.RootID = rootID
	corrective.EventSeq = seq
	corrective.GenerationNumber = currentGen
	corrective.IntraGenerationSeq = intra
	if err := s.AppendJournalEvent(ctx, tx, corrective); err != nil {
		return domain.JournalEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.JournalEvent{}, err
	}
	return corrective, nil
}
