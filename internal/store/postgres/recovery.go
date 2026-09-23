package postgres

import "context"

// UnadmittedSnapshot is a SUBMITTED Snapshot that has no admission row, i.e. a
// crash between Snapshot commit and Stage-1 admission (G3-R3).
type UnadmittedSnapshot struct {
	RootID     string
	SnapshotID string
}

// SubmittedSnapshotsWithoutAdmission finds durable SUBMITTED Snapshots that were
// never admitted, so the runtime can deterministically admit/resume them.
func (s *Store) SubmittedSnapshotsWithoutAdmission(ctx context.Context, limit int) ([]UnadmittedSnapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT s.root_id::text, s.snapshot_id::text
		   FROM index_snapshot s
		  WHERE s.lifecycle_state = 'SUBMITTED'
		    AND NOT EXISTS (SELECT 1 FROM index_admission a WHERE a.snapshot_id = s.snapshot_id)
		  ORDER BY s.created_at
		  LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnadmittedSnapshot
	for rows.Next() {
		var u UnadmittedSnapshot
		if err := rows.Scan(&u.RootID, &u.SnapshotID); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
