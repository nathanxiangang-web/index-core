package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// ErrTerminalSnapshot is deliberately finite: corrupt or incomplete evidence
// must not become success, nor leak database/provider details into diagnostics.
var ErrTerminalSnapshot = errors.New("inconsistent terminal snapshot receipt")

// terminalSnapshotReceipt is read-only and MUST be called with the existing
// index_root FOR UPDATE lock held. That lock serializes all legitimate writers
// of Snapshot/Admission/result/generation history; no partially committed result
// can be observed. expectedSeq=0 discovers the single authoritative sequence;
// Stage 2 must instead supply its already selected sequence.
//
// index_reconcile_result has no Snapshot column. Its exact Snapshot binding is
// the unique validated Admission(root,seq,snapshot), not generation equality.
func (s *Store) terminalSnapshotReceipt(ctx context.Context, tx pgx.Tx, rootID, snapshotID string, expectedSeq, currentGen int64) (int64, ReconcileOutcome, error) {
	fail := func() (int64, ReconcileOutcome, error) { return 0, ReconcileOutcome{}, ErrTerminalSnapshot }
	if currentGen < 0 || expectedSeq < 0 {
		return fail()
	}
	var sr, lc string
	var acceptance *string
	if err := tx.QueryRow(ctx, `SELECT root_id::text,lifecycle_state,acceptance_state FROM index_snapshot WHERE snapshot_id=$1::uuid`, snapshotID).Scan(&sr, &lc, &acceptance); err != nil {
		return 0, ReconcileOutcome{}, err
	}
	if sr != rootID || lc != string(domain.SnapshotReconciled) || acceptance == nil || *acceptance == string(domain.AcceptanceFailed) {
		return fail()
	}

	// A Snapshot with multiple admissions is ambiguous even if one looks valid.
	rows, err := tx.Query(ctx, `SELECT root_id::text,admission_seq,status,applied_generation FROM index_admission WHERE snapshot_id=$1::uuid ORDER BY root_id,admission_seq LIMIT 2`, snapshotID)
	if err != nil {
		return 0, ReconcileOutcome{}, err
	}
	var ar, status string
	var seq int64
	var gen *int64
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&ar, &seq, &status, &gen); err != nil {
			break
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return 0, ReconcileOutcome{}, err
	}
	if rowErr != nil {
		return 0, ReconcileOutcome{}, rowErr
	}
	if count != 1 || ar != rootID || seq <= 0 || expectedSeq > 0 && seq != expectedSeq || gen == nil || *gen < 0 || *gen > currentGen || (status != string(domain.AdmissionApplied) && status != string(domain.AdmissionNoop)) {
		return fail()
	}
	var latest int64
	if err = tx.QueryRow(ctx, `SELECT latest_admission_seq FROM index_root WHERE root_id=$1::uuid`, rootID).Scan(&latest); err != nil {
		return 0, ReconcileOutcome{}, err
	}
	if seq > latest {
		return fail()
	}
	if *gen > 0 {
		var generationExists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM index_generation WHERE root_id=$1::uuid AND generation_number=$2)`, rootID, *gen).Scan(&generationExists); err != nil {
			return 0, ReconcileOutcome{}, err
		}
		if !generationExists {
			return fail()
		}
	}

	rows, err = tx.Query(ctx, `SELECT outcome,generation_number,rejection_reason FROM index_reconcile_result WHERE root_id=$1::uuid AND admission_seq=$2 LIMIT 2`, rootID, seq)
	if err != nil {
		return 0, ReconcileOutcome{}, err
	}
	var outcome string
	var resultGen *int64
	var reason *string
	count = 0
	for rows.Next() {
		count++
		if err = rows.Scan(&outcome, &resultGen, &reason); err != nil {
			break
		}
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil {
		return 0, ReconcileOutcome{}, err
	}
	if rowErr != nil {
		return 0, ReconcileOutcome{}, rowErr
	}
	want := string(domain.ReconcileReconciled)
	if status == string(domain.AdmissionNoop) {
		want = string(domain.ReconcileNoop)
	}
	if count != 1 || outcome != want || resultGen == nil || *resultGen != *gen || reason != nil && *reason != "" {
		return fail()
	}

	// APPLIED must have its exact immutable application-history row; NOOP must
	// not invent one. Read persisted identity only, never recompute a new one.
	rows, err = tx.Query(ctx, `SELECT root_id::text,applied_admission_seq,applied_generation FROM index_applied_snapshot WHERE snapshot_id=$1::uuid LIMIT 2`, snapshotID)
	if err != nil {
		return 0, ReconcileOutcome{}, err
	}
	var hr string
	var hs, hg int64
	count = 0
	for rows.Next() {
		count++
		if err = rows.Scan(&hr, &hs, &hg); err != nil {
			break
		}
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil {
		return 0, ReconcileOutcome{}, err
	}
	if rowErr != nil {
		return 0, ReconcileOutcome{}, rowErr
	}
	if status == string(domain.AdmissionApplied) {
		if count != 1 || hr != rootID || hs != seq || hg != *gen {
			return fail()
		}
	} else {
		if count != 0 {
			return fail()
		}
		var anchor bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM index_applied_snapshot WHERE root_id=$1::uuid AND applied_generation=$2)`, rootID, *gen).Scan(&anchor); err != nil {
			return 0, ReconcileOutcome{}, err
		}
		if !anchor {
			return fail()
		}
	}

	// Reconstruct the original pre/post generation and Mutated flag from durable
	// provenance, not the root's possibly newer generation. NOOP is never mutating.
	rows, err = tx.Query(ctx, `SELECT generation_number,produced_by_snapshot_id::text,produced_by_admission_seq FROM index_generation WHERE root_id=$1::uuid AND (produced_by_snapshot_id=$2::uuid OR produced_by_admission_seq=$3) LIMIT 2`, rootID, snapshotID, seq)
	if err != nil {
		return 0, ReconcileOutcome{}, err
	}
	var gg int64
	var gs *string
	var ga *int64
	count = 0
	for rows.Next() {
		count++
		if err = rows.Scan(&gg, &gs, &ga); err != nil {
			break
		}
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil {
		return 0, ReconcileOutcome{}, err
	}
	if rowErr != nil {
		return 0, ReconcileOutcome{}, rowErr
	}
	out := ReconcileOutcome{Status: domain.AdmissionStatus(status), SnapshotLifecycle: domain.SnapshotReconciled, Generation: *gen, AppliedGeneration: *gen}
	if count > 0 {
		if count != 1 || status != string(domain.AdmissionApplied) || gg != *gen || gg <= 0 || gs == nil || *gs != snapshotID || ga == nil || *ga != seq {
			return fail()
		}
		out.Generation = gg - 1
		out.Mutated = true
	}
	return seq, out, nil
}
