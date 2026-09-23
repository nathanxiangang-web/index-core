package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
)

// PlanFromResult translates a pure Kernel reconcile decision into a Store Plan.
// Apply performs canonical writes and appends IdentityEvidence observations
// (with the Snapshot observation time, R2-9) inside the Stage-2 transaction.
func (s *Store) PlanFromResult(rootID, snapshotID string, observedAt time.Time, res reconcile.Result) *Plan {
	events := make([]domain.JournalEvent, 0, len(res.Transitions))
	for _, tr := range res.Transitions {
		et, ok := eventTypeFor(tr.Kind)
		if !ok {
			continue
		}
		var rid *string
		if tr.ResourceID != "" {
			r := tr.ResourceID
			rid = &r
		}
		payload, _ := json.Marshal(map[string]any{"reason": tr.Reason})
		events = append(events, domain.JournalEvent{EventType: et, ResourceID: rid, Payload: payload})
	}
	counts, _ := json.Marshal(res.Counts)
	observations := res.Observations

	return &Plan{
		MutatesCanonical: res.MutatesCanonical,
		Events:           events,
		Counts:           counts,
		Apply: func(ctx context.Context, tx pgx.Tx, generation int64) error {
			for _, tr := range res.Transitions {
				if err := s.applyTransition(ctx, tx, rootID, generation, tr); err != nil {
					return err
				}
			}
			for _, o := range observations {
				if err := s.AppendObservation(ctx, tx, rootID, snapshotID, o.ResourceID, o.Entry, observedAt); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func (s *Store) applyTransition(ctx context.Context, tx pgx.Tx, rootID string, generation int64, tr reconcile.Transition) error {
	switch tr.Kind {
	case reconcile.KindAdd:
		now := time.Now().UTC()
		r := domain.CanonicalResource{
			ResourceID: tr.ResourceID, RootID: rootID,
			IntroducedAtGeneration: generation, LastConfirmedGeneration: generation,
			ResourcePresence: domain.ResourcePresent, RemovalEvidenceState: domain.RemovalEvidenceNone,
			CurrentAttributes: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
		}
		if tr.Entry != nil {
			p := reconcile.EntryPath(*tr.Entry)
			r.CanonicalPath = &p
			r.Name = &tr.Entry.Name
			r.IsDir = &tr.Entry.IsDir
			r.Size = tr.Entry.Size
			r.Mtime = tr.Entry.Mtime
			r.ContentHash = tr.Entry.ContentHash
			r.HashAlgorithm = tr.Entry.HashAlgorithm
			r.ContentType = tr.Entry.ContentType
			r.ParentResourceID = s.resolveParentID(ctx, tx, rootID, reconcile.PathDir(p))
		}
		return s.InsertCanonicalResource(ctx, tx, r)

	case reconcile.KindUpdate:
		if tr.Prior == nil || tr.Entry == nil {
			return nil
		}
		upd := tr.Prior.CanonicalResource
		upd.Size = tr.Entry.Size
		upd.Mtime = tr.Entry.Mtime
		upd.ContentHash = tr.Entry.ContentHash
		upd.HashAlgorithm = tr.Entry.HashAlgorithm
		upd.ContentType = tr.Entry.ContentType
		upd.LastConfirmedGeneration = generation
		return s.UpdateResourceAttributes(ctx, tx, upd)

	case reconcile.KindRename, reconcile.KindMove:
		if tr.Prior == nil {
			return nil
		}
		var name *string
		if tr.Entry != nil {
			n := tr.Entry.Name
			name = &n
		}
		// MOVE/RENAME maintain the derived hierarchy parent (R3-7); a same-parent
		// RENAME keeps the prior parent.
		parentID := tr.Prior.ParentResourceID
		if tr.NewPath != nil {
			parentID = s.resolveParentID(ctx, tx, rootID, reconcile.PathDir(*tr.NewPath))
		}
		return s.UpdateResourcePath(ctx, tx, tr.ResourceID, tr.NewPath, parentID, name, generation)

	case reconcile.KindResetRemovalEvidence:
		return s.ResetRemovalEvidence(ctx, tx, tr.ResourceID, generation)

	case reconcile.KindMissingEvidence:
		return s.SetRemovalEvidence(ctx, tx, tr.ResourceID,
			domain.RemovalEvidenceMissingConfirmedByComplete, tr.MissingSince, tr.ConsecutiveMissing,
			tr.MissingFirstSnapshotID, tr.MissingLastSnapshotID, tr.LastConfirmedGeneration)

	case reconcile.KindRemovalCandidate:
		return s.SetRemovalEvidence(ctx, tx, tr.ResourceID,
			domain.RemovalEvidenceCandidate, tr.MissingSince, tr.ConsecutiveMissing,
			tr.MissingFirstSnapshotID, tr.MissingLastSnapshotID, tr.LastConfirmedGeneration)

	case reconcile.KindConfirmRemoved:
		if err := s.SetRemovalEvidence(ctx, tx, tr.ResourceID,
			domain.RemovalEvidenceCandidate, tr.MissingSince, tr.ConsecutiveMissing,
			tr.MissingFirstSnapshotID, tr.MissingLastSnapshotID, tr.LastConfirmedGeneration); err != nil {
			return err
		}
		return s.ConfirmRemoval(ctx, tx, tr.ResourceID, generation)
	}
	return nil
}

// resolveParentID deterministically resolves a child's parent resource id from
// its parent path: exactly one PRESENT directory at that path yields its id;
// none or an ambiguous overlap yields nil (R3-7, R8-safe).
func (s *Store) resolveParentID(ctx context.Context, q Querier, rootID, parentPath string) *string {
	if parentPath == "" || parentPath == "/" || parentPath == "." {
		return nil
	}
	var id string
	err := q.QueryRow(ctx,
		`SELECT resource_id::text FROM index_canonical_resource
		  WHERE root_id = $1::uuid AND canonical_path = $2 AND resource_presence = 'PRESENT' AND is_dir = true
		  ORDER BY resource_id LIMIT 2`, rootID, parentPath).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return nil
	}
	// Reject ambiguous parents (multiple PRESENT dirs at the same path).
	rows, qerr := q.Query(ctx,
		`SELECT count(*) FROM index_canonical_resource
		  WHERE root_id = $1::uuid AND canonical_path = $2 AND resource_presence = 'PRESENT' AND is_dir = true`,
		rootID, parentPath)
	if qerr != nil {
		return nil
	}
	defer rows.Close()
	if rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil || n != 1 {
			return nil
		}
	}
	return &id
}

func eventTypeFor(k reconcile.Kind) (domain.EventType, bool) {
	switch k {
	case reconcile.KindAdd:
		return domain.EventResourceAdded, true
	case reconcile.KindUpdate:
		return domain.EventResourceUpdated, true
	case reconcile.KindRename:
		return domain.EventResourceRenamed, true
	case reconcile.KindMove:
		return domain.EventResourceMoved, true
	case reconcile.KindConfirmRemoved:
		return domain.EventResourceRemoved, true
	default:
		return "", false
	}
}
