package postgres

import "context"

// UnadmittedGroup lists the SUBMITTED-but-unadmitted Snapshots of one root.
type UnadmittedGroup struct {
	RootID      string
	SnapshotIDs []string
}

// UnadmittedSubmittedByRoot returns SUBMITTED Snapshots that have no admission
// row, grouped by root. It deliberately applies NO created_at/DB-timing ordering:
// with the atomic create+admit path this state should not occur in normal
// operation, and it must never be used to invent authoritative ordering (G3-R2.1).
func (s *Store) UnadmittedSubmittedByRoot(ctx context.Context) ([]UnadmittedGroup, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT s.root_id::text, s.snapshot_id::text
		   FROM index_snapshot s
		  WHERE s.lifecycle_state = 'SUBMITTED'
		    AND NOT EXISTS (SELECT 1 FROM index_admission a WHERE a.snapshot_id = s.snapshot_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byRoot := map[string]*UnadmittedGroup{}
	var order []string
	for rows.Next() {
		var rootID, snapID string
		if err := rows.Scan(&rootID, &snapID); err != nil {
			return nil, err
		}
		g := byRoot[rootID]
		if g == nil {
			g = &UnadmittedGroup{RootID: rootID}
			byRoot[rootID] = g
			order = append(order, rootID)
		}
		g.SnapshotIDs = append(g.SnapshotIDs, snapID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]UnadmittedGroup, 0, len(order))
	for _, r := range order {
		out = append(out, *byRoot[r])
	}
	return out, nil
}

// ResolveUnadmittedSubmitted deterministically admits legacy/fault-stranded
// SUBMITTED Snapshots: a root with exactly ONE candidate is admitted (no ordering
// guess); a root with MULTIPLE ambiguous candidates is reported as ambiguous and
// left untouched so the caller fails closed rather than guessing an order from
// DB timestamps (G3-R2.1).
func (s *Store) ResolveUnadmittedSubmitted(ctx context.Context) (resolved int, ambiguous []string, err error) {
	groups, err := s.UnadmittedSubmittedByRoot(ctx)
	if err != nil {
		return 0, nil, err
	}
	for _, g := range groups {
		if len(g.SnapshotIDs) != 1 {
			ambiguous = append(ambiguous, g.RootID)
			continue
		}
		if _, _, aerr := s.AdmitOrResumeSnapshot(ctx, g.RootID, g.SnapshotIDs[0]); aerr != nil {
			return resolved, ambiguous, aerr
		}
		resolved++
	}
	return resolved, ambiguous, nil
}
