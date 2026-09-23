package postgres

import "context"

// UnadmittedGroup lists the SUBMITTED-but-unadmitted Snapshots of one root.
type UnadmittedGroup struct {
	RootID      string
	SnapshotIDs []string
}

// UnadmittedSubmittedByRoot returns SUBMITTED Snapshots that have no admission
// row, grouped by root. It deliberately applies NO created_at/DB-timing ordering:
// with the split Stage-1 path (CreateDraftSnapshot -> SubmitAndAdmitSnapshot) this
// state should not occur in normal operation, and it must never be used to invent
// authoritative ordering (G3-R2.1). This is the whole-database sweep used by the
// daemon worker.
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

// UnadmittedSnapshotIDsForRoot returns the SUBMITTED-but-unadmitted Snapshot IDs
// of ONE root. A one-shot `scan --root A` uses this so it can recover A without
// touching (or admitting work for) roots B/C: recovery is root-scoped.
func (s *Store) UnadmittedSnapshotIDsForRoot(ctx context.Context, rootID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT s.snapshot_id::text
		   FROM index_snapshot s
		  WHERE s.root_id = $1::uuid
		    AND s.lifecycle_state = 'SUBMITTED'
		    AND NOT EXISTS (SELECT 1 FROM index_admission a WHERE a.snapshot_id = s.snapshot_id)`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ResolveUnadmittedSubmitted deterministically admits legacy/fault-stranded
// SUBMITTED Snapshots across all roots: a root with exactly ONE candidate is
// admitted (no ordering guess); a root with MULTIPLE ambiguous candidates is
// reported as ambiguous and left untouched so the caller fails closed rather than
// guessing an order from DB timestamps (G3-R2.1). Used by the daemon worker.
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

// ResolveUnadmittedSubmittedForRoot is the root-scoped recovery used by the
// one-shot `scan --root`: it only ever inspects and admits stranded SUBMITTED work
// for the requested root. Exactly one candidate -> admit; more than one -> report
// ambiguity and leave it untouched (never ordered by DB timestamp). Roots other
// than rootID are never mutated.
func (s *Store) ResolveUnadmittedSubmittedForRoot(ctx context.Context, rootID string) (resolved int, ambiguous bool, err error) {
	ids, err := s.UnadmittedSnapshotIDsForRoot(ctx, rootID)
	if err != nil {
		return 0, false, err
	}
	switch len(ids) {
	case 0:
		return 0, false, nil
	case 1:
		if _, _, aerr := s.AdmitOrResumeSnapshot(ctx, rootID, ids[0]); aerr != nil {
			return 0, false, aerr
		}
		return 1, false, nil
	default:
		return 0, true, nil
	}
}
