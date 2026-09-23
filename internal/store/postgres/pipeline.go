package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/pipeline"
)

// EvaluateSnapshot loads a submitted Snapshot and its entries in one transaction,
// runs the Kernel evaluation pipeline (completeness -> final digest), persists the
// Kernel-owned acceptance_state + scope_shrink_corroboration, and returns the
// final IO3 identity. The identity is never injected by the adapter (B4).
//
// corroboration is the Kernel-derived significance corroboration from later
// independent admitted observations; nil means NONE.
func (s *Store) EvaluateSnapshot(ctx context.Context, snapshotID string, corroboration *domain.ScopeShrinkCorroboration) (pipeline.Result, error) {
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
	priorPresent, err := s.CountCanonicalPresent(ctx, tx, snap.RootID)
	if err != nil {
		return pipeline.Result{}, err
	}

	ev := pipeline.Evidence{
		TraversalStatus:          snap.TraversalStatus,
		SkippedScopes:            snap.SkippedScopes,
		SkippedKnownEmpty:        snap.SkippedScopesKnownEmpty,
		HasErrorSummary:          len(snap.ErrorSummary) > 0,
		Freshness:                freshOrUnknown(snap.FreshnessEvidence),
		Assurance:                assuranceOrUnknown(snap.CollectorCompletenessAssurance),
		EntryCount:               countOrZero(snap.EntryCount),
		PriorPresent:             priorPresent,
		IndependentCorroboration: corroboration,
	}
	res := pipeline.Evaluate(entries, ev)

	if err := s.SetSnapshotEvaluated(ctx, tx, snapshotID, res.Acceptance, &res.Corroboration); err != nil {
		return pipeline.Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return pipeline.Result{}, err
	}
	return res, nil
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

func countOrZero(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}
