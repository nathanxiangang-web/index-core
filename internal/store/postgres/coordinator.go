package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/pipeline"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
)

// ErrBindingMismatch is returned when the supplied root/snapshot do not match an
// existing admission binding.
var ErrBindingMismatch = errors.New("snapshot/root binding mismatch")

// ErrNotSubmitted is returned when a Snapshot is not in SUBMITTED state.
var ErrNotSubmitted = errors.New("snapshot is not SUBMITTED")

// Coordinator is the production-safe Kernel execution path. It makes the frozen
// sequence the normal path (R3-8):
//
//	SUBMITTED -> atomic Stage-1 admission -> generation-consistent evaluation
//	(final acceptance + IO3 identity under the per-root lock) -> Stage-2 reconcile
type Coordinator struct {
	store *Store
	cfg   reconcile.Config
}

// NewCoordinator builds a coordinator with the given per-root policy config. The
// config is validated fail-closed before any mutation (R3-6).
func NewCoordinator(store *Store, cfg reconcile.Config) *Coordinator {
	return &Coordinator{store: store, cfg: cfg}
}

// ProcessSnapshot runs the full safe pipeline for one SUBMITTED snapshot.
func (c *Coordinator) ProcessSnapshot(ctx context.Context, rootID, snapshotID string) (ReconcileOutcome, error) {
	if err := c.cfg.Validate(); err != nil {
		return ReconcileOutcome{}, err
	}
	seq, err := c.store.AdmitSnapshot(ctx, rootID, snapshotID)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	return c.store.reconcileHeadSafe(ctx, rootID, seq, snapshotID, c.cfg)
}

// AdmitSnapshot is Stage 1 as one short per-root serialized transaction:
// lock root -> validate Snapshot exists + SUBMITTED + same root -> allocate seq
// -> INSERT PENDING admission -> commit (R3-4).
func (s *Store) AdmitSnapshot(ctx context.Context, rootID, snapshotID string) (int64, error) {
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
	if snapLc != string(domain.SnapshotSubmitted) {
		return 0, ErrNotSubmitted
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

// reconcileHeadSafe runs Stage 2 with evaluation performed under the per-root lock,
// so acceptance and the final IO3 identity are computed against the canonical
// generation actually being reconciled (R3-3). It also verifies the head
// admission binding (R3-4) and preserves the distinct STALE_INPUT path (R3-5).
func (s *Store) reconcileHeadSafe(ctx context.Context, rootID string, seq int64, snapshotID string, cfg reconcile.Config) (ReconcileOutcome, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ReconcileOutcome{}, err
	}
	defer tx.Rollback(ctx)

	var (
		currentGen int64
		rootLc     string
	)
	if err := tx.QueryRow(ctx,
		`SELECT current_generation, lifecycle_state FROM index_root WHERE root_id = $1::uuid FOR UPDATE`,
		rootID).Scan(&currentGen, &rootLc); err != nil {
		return ReconcileOutcome{}, err
	}

	// Verify the head admission binding: the PENDING head must be this admission
	// and must be bound to this exact Snapshot.
	var headSeq *int64
	if err := tx.QueryRow(ctx,
		`SELECT min(admission_seq) FROM index_admission WHERE root_id = $1::uuid AND status = 'PENDING'`,
		rootID).Scan(&headSeq); err != nil {
		return ReconcileOutcome{}, err
	}
	if headSeq == nil || *headSeq != seq {
		return ReconcileOutcome{}, ErrNotHead
	}
	var boundSnap, boundRoot string
	if err := tx.QueryRow(ctx,
		`SELECT snapshot_id::text, root_id::text FROM index_admission WHERE root_id = $1::uuid AND admission_seq = $2`,
		rootID, seq).Scan(&boundSnap, &boundRoot); err != nil {
		return ReconcileOutcome{}, err
	}
	if boundSnap != snapshotID || boundRoot != rootID {
		return ReconcileOutcome{}, ErrBindingMismatch
	}

	in := ReconcileInput{RootID: rootID, AdmissionSeq: seq, SnapshotID: snapshotID}

	if domain.RootLifecycleState(rootLc) == domain.RootDeleted {
		return s.reject(ctx, tx, in, currentGen, "root is DELETED")
	}

	snap, err := s.GetSnapshot(ctx, tx, snapshotID)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	if snap.RootID != rootID {
		return ReconcileOutcome{}, ErrBindingMismatch
	}
	if snap.LifecycleState != domain.SnapshotSubmitted {
		return ReconcileOutcome{}, ErrNotSubmitted
	}

	// Evaluate under the lock: load prior + entries, then finalize acceptance and
	// the Kernel-owned IO3 identity against this generation (R3-3).
	prior, err := s.LoadPriorResources(ctx, tx, rootID)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	entries, err := s.ListSnapshotEntries(ctx, tx, snapshotID)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	priorPresent := int64(0)
	for i := range prior {
		if prior[i].ResourcePresence == domain.ResourcePresent {
			priorPresent++
		}
	}
	// Corroboration is only relevant to a significant shrink (C-7); otherwise a
	// matching earlier observation must not alter the evaluated identity.
	corroboration := domain.ShrinkNone
	if significantShrink(priorPresent, len(entries)) {
		corroboration = s.deriveQualifiedCorroboration(ctx, tx, rootID, seq, snapshotID, entries)
	}
	now := snap.ObservedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	eval := pipeline.Evaluate(entries, prior, pipeline.Evidence{
		TraversalStatus:   snap.TraversalStatus,
		SkippedScopes:     snap.SkippedScopes,
		SkippedKnownEmpty: snap.SkippedScopesKnownEmpty,
		HasErrorSummary:   len(snap.ErrorSummary) > 0,
		Freshness:         freshOrUnknown(snap.FreshnessEvidence),
		Assurance:         assuranceOrUnknown(snap.CollectorCompletenessAssurance),
		EntryCount:        int64(len(entries)),
		PriorPresent:      priorPresent,
		Corroboration:     corroboration,
	}, cfg, now)
	if err := s.SetSnapshotEvaluated(ctx, tx, snapshotID, eval.Acceptance, &eval.Corroboration); err != nil {
		return ReconcileOutcome{}, err
	}
	if eval.Acceptance == domain.AcceptanceFailed {
		return s.reject(ctx, tx, in, currentGen, "snapshot acceptance_state is FAILED")
	}

	// IO3 NO-OP.
	noop, err := s.AppliedExistsAtGeneration(ctx, tx, rootID, eval.Identity, currentGen)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	if noop {
		gen := currentGen
		if err := s.SetAdmissionStatus(ctx, tx, rootID, seq, domain.AdmissionNoop, &gen); err != nil {
			return ReconcileOutcome{}, err
		}
		if err := s.SetSnapshotLifecycleState(ctx, tx, snapshotID, domain.SnapshotReconciled); err != nil {
			return ReconcileOutcome{}, err
		}
		if err := s.insertResult(ctx, tx, in, currentGen, domain.ReconcileNoop, ""); err != nil {
			return ReconcileOutcome{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ReconcileOutcome{}, err
		}
		return ReconcileOutcome{Status: domain.AdmissionNoop, SnapshotLifecycle: domain.SnapshotReconciled,
			Generation: currentGen, AppliedGeneration: currentGen}, nil
	}

	// STALE / out-of-order: distinct STALE_INPUT classification (R3-5).
	appliedMax, err := s.AppliedMax(ctx, tx, rootID)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	if seq <= appliedMax {
		return s.staleInput(ctx, tx, in, currentGen)
	}

	res := reconcile.Reconcile(prior, entries, eval.Acceptance, cfg, now, snapshotID)
	plan := s.PlanFromResult(rootID, snapshotID, snap.ObservedAt, res)

	appliedGen := currentGen
	if plan.MutatesCanonical {
		tag, err := tx.Exec(ctx,
			`UPDATE index_root SET current_generation = current_generation + 1, updated_at = now()
			  WHERE root_id = $1::uuid AND current_generation = $2`, rootID, currentGen)
		if err != nil {
			return ReconcileOutcome{}, err
		}
		if tag.RowsAffected() != 1 {
			return ReconcileOutcome{}, ErrCASConflict
		}
		appliedGen = currentGen + 1
		if _, err := tx.Exec(ctx,
			`INSERT INTO index_generation(root_id, generation_number, produced_by_snapshot_id,
			        produced_by_admission_seq, summary)
			 VALUES ($1::uuid, $2, $3::uuid, $4, $5)`,
			rootID, appliedGen, snapshotID, seq, jsonOrEmpty(plan.Counts)); err != nil {
			return ReconcileOutcome{}, err
		}
	}
	if plan.Apply != nil {
		if err := plan.Apply(ctx, tx, appliedGen); err != nil {
			return ReconcileOutcome{}, err
		}
	}
	if plan.MutatesCanonical {
		evSeq, err := s.NextEventSeq(ctx, tx, rootID)
		if err != nil {
			return ReconcileOutcome{}, err
		}
		for _, ev := range plan.Events {
			ev.RootID = rootID
			ev.EventSeq = evSeq
			ev.GenerationNumber = appliedGen
			if ev.IntraGenerationSeq == 0 {
				intra, err := s.NextIntraGenerationSeq(ctx, tx, rootID, appliedGen)
				if err != nil {
					return ReconcileOutcome{}, err
				}
				ev.IntraGenerationSeq = intra
			}
			if err := s.AppendJournalEvent(ctx, tx, ev); err != nil {
				return ReconcileOutcome{}, err
			}
			evSeq++
		}
	}

	if err := s.SetAdmissionStatus(ctx, tx, rootID, seq, domain.AdmissionApplied, &appliedGen); err != nil {
		return ReconcileOutcome{}, err
	}
	if err := s.SetSnapshotLifecycleState(ctx, tx, snapshotID, domain.SnapshotReconciled); err != nil {
		return ReconcileOutcome{}, err
	}
	if err := s.InsertAppliedSnapshot(ctx, tx, domain.AppliedSnapshot{
		RootID:                    rootID,
		SnapshotIdentityKind:      eval.Identity.Kind,
		SnapshotIdentityNamespace: eval.Identity.Namespace,
		SnapshotIdentityVersion:   eval.Identity.Version,
		SnapshotIdentityValue:     eval.Identity.Value,
		SnapshotID:                snapshotID,
		AppliedGeneration:         appliedGen,
		AppliedAdmissionSeq:       seq,
	}); err != nil {
		return ReconcileOutcome{}, err
	}
	if err := s.insertResult(ctx, tx, in, appliedGen, domain.ReconcileReconciled, ""); err != nil {
		return ReconcileOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ReconcileOutcome{}, err
	}
	return ReconcileOutcome{Status: domain.AdmissionApplied, SnapshotLifecycle: domain.SnapshotReconciled,
		Generation: currentGen, AppliedGeneration: appliedGen, Mutated: plan.MutatesCanonical}, nil
}

// staleInput records the distinct STALE_INPUT classification with no mutation.
func (s *Store) staleInput(ctx context.Context, tx pgx.Tx, in ReconcileInput, generation int64) (ReconcileOutcome, error) {
	if err := s.SetAdmissionStatus(ctx, tx, in.RootID, in.AdmissionSeq, domain.AdmissionStaleInput, nil); err != nil {
		return ReconcileOutcome{}, err
	}
	if err := s.SetSnapshotLifecycleState(ctx, tx, in.SnapshotID, domain.SnapshotRejected); err != nil {
		return ReconcileOutcome{}, err
	}
	if err := s.insertResult(ctx, tx, in, generation, domain.ReconcileStaleInput, "superseded input"); err != nil {
		return ReconcileOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ReconcileOutcome{}, err
	}
	return ReconcileOutcome{Status: domain.AdmissionStaleInput, SnapshotLifecycle: domain.SnapshotRejected,
		Generation: generation, AppliedGeneration: generation}, nil
}

// significantShrink mirrors the completeness gate's default shrink threshold
// (Gate 1C runtime config D-DEFER-7, default 0.5).
func significantShrink(priorPresent int64, entryCount int) bool {
	if priorPresent <= 0 {
		return false
	}
	drop := priorPresent - int64(entryCount)
	if drop <= 0 {
		return false
	}
	return float64(drop)/float64(priorPresent) >= 0.5
}

// deriveQualifiedCorroboration returns CORROBORATED only when a qualifying,
// independently admitted EARLIER observation (admission_seq < current) with
// all-positive completeness evidence independently observed the same reduced
// scope signature (R3-2). Otherwise NONE.
func (s *Store) deriveQualifiedCorroboration(ctx context.Context, q Querier, rootID string, currentSeq int64, snapshotID string, entries []domain.SnapshotEntry) domain.ScopeShrinkCorroboration {
	target := pipeline.RawScopeSignature(entries)
	if target == "" {
		return domain.ShrinkNone
	}
	rows, err := q.Query(ctx,
		`SELECT s.snapshot_id::text
		   FROM index_snapshot s
		   JOIN index_admission a ON a.snapshot_id = s.snapshot_id AND a.root_id = s.root_id
		  WHERE s.root_id = $1::uuid AND s.snapshot_id <> $2::uuid AND a.admission_seq < $3
		    AND s.lifecycle_state IN ('EVALUATED','RECONCILED')
		    AND s.traversal_status = 'SUCCESS' AND s.error_summary IS NULL
		    AND s.skipped_scopes_known_empty = true
		    AND s.freshness_evidence IN ('FRESH_DIRECT','FRESH_REFRESHED','CACHED_FRESH')
		    AND s.collector_completeness_assurance = 'STRONG_FAILURE_VISIBILITY'`,
		rootID, snapshotID, currentSeq)
	if err != nil {
		return domain.ShrinkNone
	}
	defer rows.Close()
	var candidateIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return domain.ShrinkNone
		}
		candidateIDs = append(candidateIDs, id)
	}
	for _, id := range candidateIDs {
		priorEntries, err := s.ListSnapshotEntries(ctx, q, id)
		if err != nil {
			continue
		}
		if pipeline.RawScopeSignature(priorEntries) == target {
			return domain.ShrinkCorroborated
		}
	}
	return domain.ShrinkNone
}
