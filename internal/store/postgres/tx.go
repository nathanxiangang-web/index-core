package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
)

// ErrNotHead is returned when a reconcile is attempted for a non-head-of-line
// admission. Absolute per-root FIFO allows only the minimum PENDING seq (doc B RC1/RC4).
var ErrNotHead = errors.New("admission seq is not the per-root head-of-line")

// ErrCASConflict is returned when the expected generation no longer matches the
// persisted generation; the caller must reload and retry or abort (doc B R11).
var ErrCASConflict = errors.New("generation CAS conflict")

// ReconcileInput identifies one admitted input to process.
type ReconcileInput struct {
	RootID       string
	AdmissionSeq int64
	SnapshotID   string
	Identity     domain.SnapshotIdentity
	// ExpectedGeneration optionally overrides the generation read under the root
	// lock, to model a stale computation (used by CAS-conflict tests). When nil,
	// the locked current generation is used.
	ExpectedGeneration *int64
}

// Plan is the Kernel-computed reconcile decision (produced by the Safe Reconcile
// core, P4). Apply performs canonical writes and IdentityEvidence appends inside
// the transaction; Events are the ordered journal events the Kernel decided to emit.
type Plan struct {
	MutatesCanonical bool
	Apply            func(ctx context.Context, tx pgx.Tx, generation int64) error
	Events           []domain.JournalEvent
	Counts           []byte
}

// ReconcileOutcome reports the terminal result of a Stage-2 transaction.
type ReconcileOutcome struct {
	Status            domain.AdmissionStatus
	SnapshotLifecycle domain.SnapshotLifecycleState
	Generation        int64
	AppliedGeneration int64
	Mutated           bool
}

// ApplyFunc computes the Plan from the loaded rich prior state and snapshot.
// The prior state (canonical + latest IdentityEvidence) is loaded by the Store
// inside the Stage-2 transaction (B3).
type ApplyFunc func(prior []reconcile.PriorResource, snap domain.Snapshot, currentGeneration int64) (*Plan, error)

// ReconcileHead runs Stage 2 (doc B Sec 1.2) as a single atomic transaction:
// lock root, enforce absolute per-root FIFO, evaluate IO3/stale, load rich prior,
// apply the Kernel plan, append journal events + identity evidence, advance
// generation only on real mutation, record applied/admission state and the
// Snapshot lifecycle, and commit all-or-nothing.
func (s *Store) ReconcileHead(ctx context.Context, in ReconcileInput, compute ApplyFunc) (ReconcileOutcome, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ReconcileOutcome{}, err
	}
	defer tx.Rollback(ctx)

	var (
		currentGen int64
		lifecycle  string
	)
	if err := tx.QueryRow(ctx,
		`SELECT current_generation, lifecycle_state
		   FROM index_root WHERE root_id = $1::uuid FOR UPDATE`, in.RootID).Scan(&currentGen, &lifecycle); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ReconcileOutcome{}, ErrNotFound
		}
		return ReconcileOutcome{}, err
	}
	expected := currentGen
	if in.ExpectedGeneration != nil {
		expected = *in.ExpectedGeneration
	}

	// R2: absolute per-root FIFO.
	var headSeq *int64
	if err := tx.QueryRow(ctx,
		`SELECT min(admission_seq) FROM index_admission
		  WHERE root_id = $1::uuid AND status = 'PENDING'`, in.RootID).Scan(&headSeq); err != nil {
		return ReconcileOutcome{}, err
	}
	if headSeq == nil || *headSeq != in.AdmissionSeq {
		return ReconcileOutcome{}, ErrNotHead
	}

	// R4: DELETED root rejects new external reconciles before any write.
	if domain.RootLifecycleState(lifecycle) == domain.RootDeleted {
		if err := s.SetAdmissionStatus(ctx, tx, in.RootID, in.AdmissionSeq, domain.AdmissionRejected, nil); err != nil {
			return ReconcileOutcome{}, err
		}
		if err := s.SetSnapshotLifecycleState(ctx, tx, in.SnapshotID, domain.SnapshotRejected); err != nil {
			return ReconcileOutcome{}, err
		}
		if err := s.insertResult(ctx, tx, in, currentGen, domain.ReconcileRejected, "root is DELETED"); err != nil {
			return ReconcileOutcome{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ReconcileOutcome{}, err
		}
		return ReconcileOutcome{Status: domain.AdmissionRejected, SnapshotLifecycle: domain.SnapshotRejected,
			Generation: currentGen, AppliedGeneration: currentGen}, nil
	}

	// R5: IO3 NO-OP.
	noop, err := s.AppliedExistsAtGeneration(ctx, tx, in.RootID, in.Identity, currentGen)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	if noop {
		gen := currentGen
		if err := s.SetAdmissionStatus(ctx, tx, in.RootID, in.AdmissionSeq, domain.AdmissionNoop, &gen); err != nil {
			return ReconcileOutcome{}, err
		}
		if err := s.SetSnapshotLifecycleState(ctx, tx, in.SnapshotID, domain.SnapshotReconciled); err != nil {
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

	// R6: stale/out-of-order.
	appliedMax, err := s.AppliedMax(ctx, tx, in.RootID)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	if in.AdmissionSeq <= appliedMax {
		if err := s.SetAdmissionStatus(ctx, tx, in.RootID, in.AdmissionSeq, domain.AdmissionStaleInput, nil); err != nil {
			return ReconcileOutcome{}, err
		}
		if err := s.SetSnapshotLifecycleState(ctx, tx, in.SnapshotID, domain.SnapshotRejected); err != nil {
			return ReconcileOutcome{}, err
		}
		if err := s.insertResult(ctx, tx, in, currentGen, domain.ReconcileStaleInput, "superseded input"); err != nil {
			return ReconcileOutcome{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ReconcileOutcome{}, err
		}
		return ReconcileOutcome{Status: domain.AdmissionStaleInput, SnapshotLifecycle: domain.SnapshotRejected,
			Generation: currentGen, AppliedGeneration: currentGen}, nil
	}

	// R7: load rich prior (canonical + latest IdentityEvidence) in this transaction.
	prior, err := s.LoadPriorResources(ctx, tx, in.RootID)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	snap, err := s.GetSnapshot(ctx, tx, in.SnapshotID)
	if err != nil {
		return ReconcileOutcome{}, err
	}

	// R8: Kernel computes the plan (pure).
	plan, err := compute(prior, snap, currentGen)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	if plan == nil {
		plan = &Plan{}
	}

	appliedGen := currentGen
	if plan.MutatesCanonical {
		tag, err := tx.Exec(ctx,
			`UPDATE index_root
			    SET current_generation = current_generation + 1, updated_at = now()
			  WHERE root_id = $1::uuid AND current_generation = $2`, in.RootID, expected)
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
			in.RootID, appliedGen, in.SnapshotID, in.AdmissionSeq, jsonOrEmpty(plan.Counts)); err != nil {
			return ReconcileOutcome{}, err
		}
		if plan.Apply != nil {
			if err := plan.Apply(ctx, tx, appliedGen); err != nil {
				return ReconcileOutcome{}, err
			}
		}
		seq, err := s.NextEventSeq(ctx, tx, in.RootID)
		if err != nil {
			return ReconcileOutcome{}, err
		}
		for _, ev := range plan.Events {
			ev.RootID = in.RootID
			ev.EventSeq = seq
			ev.GenerationNumber = appliedGen
			if ev.IntraGenerationSeq == 0 {
				intra, err := s.NextIntraGenerationSeq(ctx, tx, in.RootID, appliedGen)
				if err != nil {
					return ReconcileOutcome{}, err
				}
				ev.IntraGenerationSeq = intra
			}
			if err := s.AppendJournalEvent(ctx, tx, ev); err != nil {
				return ReconcileOutcome{}, err
			}
			seq++
		}
	} else {
		// R11 ZERO-mutation path: no advance, but still compare-and-check the
		// expected generation under the held lock.
		var check int64
		if err := tx.QueryRow(ctx,
			`SELECT current_generation FROM index_root
			  WHERE root_id = $1::uuid AND current_generation = $2 FOR UPDATE`,
			in.RootID, expected).Scan(&check); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ReconcileOutcome{}, ErrCASConflict
			}
			return ReconcileOutcome{}, err
		}
		// Observations may still need appending even without canonical mutation.
		if plan.Apply != nil {
			if err := plan.Apply(ctx, tx, appliedGen); err != nil {
				return ReconcileOutcome{}, err
			}
		}
		appliedGen = currentGen
	}

	// R12: terminal admission + application-history row + Snapshot lifecycle.
	if err := s.SetAdmissionStatus(ctx, tx, in.RootID, in.AdmissionSeq, domain.AdmissionApplied, &appliedGen); err != nil {
		return ReconcileOutcome{}, err
	}
	if err := s.SetSnapshotLifecycleState(ctx, tx, in.SnapshotID, domain.SnapshotReconciled); err != nil {
		return ReconcileOutcome{}, err
	}
	if err := s.InsertAppliedSnapshot(ctx, tx, domain.AppliedSnapshot{
		RootID:                    in.RootID,
		SnapshotIdentityKind:      in.Identity.Kind,
		SnapshotIdentityNamespace: in.Identity.Namespace,
		SnapshotIdentityVersion:   in.Identity.Version,
		SnapshotIdentityValue:     in.Identity.Value,
		SnapshotID:                in.SnapshotID,
		AppliedGeneration:         appliedGen,
		AppliedAdmissionSeq:       in.AdmissionSeq,
	}); err != nil {
		return ReconcileOutcome{}, err
	}
	if err := s.insertResult(ctx, tx, in, appliedGen, domain.ReconcileReconciled, ""); err != nil {
		return ReconcileOutcome{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return ReconcileOutcome{}, err
	}
	return ReconcileOutcome{
		Status: domain.AdmissionApplied, SnapshotLifecycle: domain.SnapshotReconciled,
		Generation: currentGen, AppliedGeneration: appliedGen, Mutated: plan.MutatesCanonical,
	}, nil
}

// MarkAdmissionFailed records a terminal FAILED status for a rolled-back
// reconcile, in a separate later transaction (doc B T-AT5). The Snapshot is left
// at EVALUATED so a retry is still possible (B6: do not falsely retire).
func (s *Store) MarkAdmissionFailed(ctx context.Context, rootID string, seq int64, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.SetAdmissionStatus(ctx, tx, rootID, seq, domain.AdmissionFailed, nil); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO index_reconcile_result(reconcile_id, root_id, admission_seq, outcome, counts, rejection_reason)
		 VALUES (gen_random_uuid(), $1::uuid, $2, 'FAILED', '{}'::jsonb, $3)`,
		rootID, seq, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) insertResult(ctx context.Context, q Querier, in ReconcileInput, generation int64, outcome domain.ReconcileOutcome, reason string) error {
	var gen *int64
	if outcome != domain.ReconcileRejected && outcome != domain.ReconcileStaleInput && outcome != domain.ReconcileFailed {
		gen = &generation
	}
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	_, err := q.Exec(ctx,
		`INSERT INTO index_reconcile_result(reconcile_id, root_id, admission_seq, generation_number, outcome, counts, rejection_reason)
		 VALUES (gen_random_uuid(), $1::uuid, $2, $3, $4, '{}'::jsonb, $5)`,
		in.RootID, in.AdmissionSeq, gen, string(outcome), reasonPtr)
	return err
}

func jsonOrEmpty(b []byte) []byte {
	if len(b) == 0 {
		return []byte(`{}`)
	}
	return b
}
