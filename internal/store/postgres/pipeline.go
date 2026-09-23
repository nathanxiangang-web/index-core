package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/pipeline"
)

// EvaluateSnapshot loads a SUBMITTED Snapshot and its entries in one transaction,
// loads the rich prior, derives the Kernel-owned scope-shrink corroboration from
// persisted admitted Snapshots, runs the Kernel evaluation pipeline, persists the
// acceptance_state + corroboration once (SUBMITTED -> EVALUATED), and returns the
// final IO3 identity. No caller-supplied verdict is accepted (R2-6).
func (s *Store) EvaluateSnapshot(ctx context.Context, rootID, snapshotID string) (pipeline.Result, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return pipeline.Result{}, err
	}
	defer tx.Rollback(ctx)

	snap, err := s.GetSnapshot(ctx, tx, snapshotID)
	if err != nil {
		return pipeline.Result{}, err
	}
	entries, err := s.ListSnapshotEntries(ctx, tx, snapshotID)
	if err != nil {
		return pipeline.Result{}, err
	}
	prior, err := s.LoadPriorResources(ctx, tx, rootID)
	if err != nil {
		return pipeline.Result{}, err
	}
	var priorPresent int64
	for i := range prior {
		if prior[i].ResourcePresence == domain.ResourcePresent {
			priorPresent++
		}
	}

	corroboration := s.deriveCorroboration(ctx, tx, rootID, snapshotID, priorPresent, len(entries))

	res := pipeline.Evaluate(entries, prior, pipeline.Evidence{
		TraversalStatus:   snap.TraversalStatus,
		SkippedScopes:     snap.SkippedScopes,
		SkippedKnownEmpty: snap.SkippedScopesKnownEmpty,
		HasErrorSummary:   len(snap.ErrorSummary) > 0,
		Freshness:         freshOrUnknown(snap.FreshnessEvidence),
		Assurance:         assuranceOrUnknown(snap.CollectorCompletenessAssurance),
		EntryCount:        int64(len(entries)),
		PriorPresent:      priorPresent,
		Corroboration:     corroboration,
	})

	if err := s.SetSnapshotEvaluated(ctx, tx, snapshotID, res.Acceptance, &res.Corroboration); err != nil {
		return pipeline.Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pipeline.Result{}, err
	}
	return res, nil
}

// deriveCorroboration returns CORROBORATED when the current significant shrink is
// independently confirmed by a previously admitted Snapshot (different snapshot,
// EVALUATED/RECONCILED, with a matching reduced entry count), else NONE. It is
// Kernel-derived from persisted admitted evidence, never caller-supplied (R2-6).
func (s *Store) deriveCorroboration(ctx context.Context, q Querier, rootID, snapshotID string, priorPresent int64, entryCount int) domain.ScopeShrinkCorroboration {
	if priorPresent <= 0 || entryCount < 0 {
		return domain.ShrinkNone
	}
	drop := priorPresent - int64(entryCount)
	if drop <= 0 || float64(drop)/float64(priorPresent) < 0.5 {
		return domain.ShrinkNone
	}
	var n int
	err := q.QueryRow(ctx,
		`SELECT count(*) FROM index_snapshot
		  WHERE root_id = $1::uuid AND snapshot_id <> $2::uuid
		    AND lifecycle_state IN ('EVALUATED','RECONCILED')
		    AND entry_count IS NOT NULL
		    AND abs(entry_count - $3) <= greatest(1, $3 / 10)`,
		rootID, snapshotID, entryCount).Scan(&n)
	if err != nil || n == 0 {
		return domain.ShrinkNone
	}
	return domain.ShrinkCorroborated
}

func freshOrUnknown(f *domain.FreshnessEvidence) domain.FreshnessEvidence {
	if f == nil {
		return domain.FreshnessUnknown
	}
	return *f
}

func assuranceOrUnknown(a *domain.FailureVisibility) domain.FailureVisibility {
	if a == nil {
		return domain.UnknownFailureVisibility
	}
	return *a
}
