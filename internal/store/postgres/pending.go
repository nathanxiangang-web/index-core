package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// PendingRoots returns the distinct roots that currently have a PENDING
// admission, so the runtime worker can process each root's absolute FIFO head.
func (s *Store) PendingRoots(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT root_id::text FROM index_admission
		  WHERE status = 'PENDING' ORDER BY root_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PendingHead returns the absolute head PENDING admission (seq + snapshot) for a
// root, or ok=false when there is no pending work.
func (s *Store) PendingHead(ctx context.Context, rootID string) (seq int64, snapshotID string, ok bool, err error) {
	row := s.pool.QueryRow(ctx,
		`SELECT admission_seq, snapshot_id::text FROM index_admission
		  WHERE root_id = $1::uuid AND status = 'PENDING' ORDER BY admission_seq LIMIT 1`, rootID)
	if err := row.Scan(&seq, &snapshotID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", false, nil
		}
		return 0, "", false, err
	}
	return seq, snapshotID, true, nil
}
