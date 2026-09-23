package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// ErrNotDraft means Stage-1 finalize was asked for a Snapshot that is not DRAFT.
var ErrNotDraft = errors.New("snapshot is not DRAFT")

// CreateDraftSnapshot persists a DRAFT Snapshot and its entries WITHOUT holding
// the per-root serialization lock. A large Collector scan (20k+ entries) thus
// never blocks the root's admission lane with a long FOR UPDATE transaction.
//
// It ONLY ever creates DRAFT work (G3-R5.2): a caller that supplies any other
// lifecycle_state is rejected, so this path can never smuggle a SUBMITTED
// Snapshot past admission. The DRAFT is inert: it carries no admission_seq, is
// excluded from the worker's PENDING head, and is never treated as executable
// input. A crash between this write and the Stage-1 finalize therefore leaves
// only a non-executable DRAFT; it can never produce a SUBMITTED-but-unadmitted
// Snapshot.
func (s *Store) CreateDraftSnapshot(ctx context.Context, snap domain.Snapshot, entries []domain.SnapshotEntry) error {
	if snap.LifecycleState != "" && snap.LifecycleState != domain.SnapshotDraft {
		return fmt.Errorf("%w: CreateDraftSnapshot got lifecycle_state=%s", ErrNotDraft, snap.LifecycleState)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	snap.LifecycleState = domain.SnapshotDraft
	if err := s.InsertSnapshotStub(ctx, tx, snap); err != nil {
		return err
	}
	for i := range entries {
		entries[i].SnapshotID = snap.SnapshotID
		if err := s.InsertSnapshotEntry(ctx, tx, entries[i]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SubmitAndAdmitSnapshot is the SHORT Stage-1 transaction (G3-R2.1): lock the
// root, verify the Snapshot is DRAFT and bound to that root, move DRAFT ->
// SUBMITTED, allocate the authoritative admission_seq, and INSERT the PENDING
// admission. It performs only O(1) work under the root FOR UPDATE lock and never
// writes entries, so admission order stays authoritative without being coupled to
// scan size.
func (s *Store) SubmitAndAdmitSnapshot(ctx context.Context, rootID, snapshotID string) (int64, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var lifecycle string
	if err := tx.QueryRow(ctx,
		`SELECT lifecycle_state FROM index_root WHERE root_id = $1::uuid FOR UPDATE`, rootID).Scan(&lifecycle); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}

	var (
		snapRoot string
		snapLc   string
	)
	if err := tx.QueryRow(ctx,
		`SELECT root_id::text, lifecycle_state FROM index_snapshot WHERE snapshot_id = $1::uuid`,
		snapshotID).Scan(&snapRoot, &snapLc); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if snapRoot != rootID {
		return 0, ErrBindingMismatch
	}
	if snapLc != string(domain.SnapshotDraft) {
		return 0, ErrNotDraft
	}

	if err := s.MarkSnapshotSubmitted(ctx, tx, snapshotID); err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRow(ctx,
		`UPDATE index_root SET latest_admission_seq = latest_admission_seq + 1, updated_at = now()
		  WHERE root_id = $1::uuid RETURNING latest_admission_seq`, rootID).Scan(&seq); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO index_admission(root_id, admission_seq, snapshot_id, status)
		 VALUES ($1::uuid, $2, $3::uuid, 'PENDING')`, rootID, seq, snapshotID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return seq, nil
}
