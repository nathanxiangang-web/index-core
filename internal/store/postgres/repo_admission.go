package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// AllocateAdmission is Stage 1 (doc B Sec 1.1): under the per-root row lock it
// advances index_root.latest_admission_seq and inserts a PENDING admission row.
// The admission_seq is Kernel-owned and never derived from provider wall-clock (IO1/IO7).
func (s *Store) AllocateAdmission(ctx context.Context, q Querier, rootID, snapshotID string) (int64, error) {
	var seq int64
	if err := q.QueryRow(ctx,
		`UPDATE index_root
		    SET latest_admission_seq = latest_admission_seq + 1, updated_at = now()
		  WHERE root_id = $1::uuid
		  RETURNING latest_admission_seq`, rootID).Scan(&seq); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if _, err := q.Exec(ctx,
		`INSERT INTO index_admission(root_id, admission_seq, snapshot_id, status)
		 VALUES ($1::uuid, $2, $3::uuid, 'PENDING')`, rootID, seq, snapshotID); err != nil {
		return 0, err
	}
	return seq, nil
}

// GetAdmission loads one admission row.
func (s *Store) GetAdmission(ctx context.Context, q Querier, rootID string, seq int64) (domain.Admission, error) {
	var (
		a      domain.Admission
		status string
	)
	err := q.QueryRow(ctx,
		`SELECT root_id::text, admission_seq, snapshot_id::text, admitted_at, status,
		        applied_generation, claimed_by, claimed_at, lease_expires_at
		   FROM index_admission WHERE root_id = $1::uuid AND admission_seq = $2`,
		rootID, seq).Scan(&a.RootID, &a.AdmissionSeq, &a.SnapshotID, &a.AdmittedAt, &status,
		&a.AppliedGeneration, &a.ClaimedBy, &a.ClaimedAt, &a.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Admission{}, ErrNotFound
	}
	if err != nil {
		return domain.Admission{}, err
	}
	a.Status = domain.AdmissionStatus(status)
	return a, nil
}

// HeadAdmission returns the absolute head-of-line PENDING admission (min
// admission_seq). Only this row may be claimed/processed/committed (doc B RC1/RC4).
func (s *Store) HeadAdmission(ctx context.Context, q Querier, rootID string) (domain.Admission, error) {
	var (
		a      domain.Admission
		status string
	)
	err := q.QueryRow(ctx,
		`SELECT root_id::text, admission_seq, snapshot_id::text, admitted_at, status,
		        applied_generation, claimed_by, claimed_at, lease_expires_at
		   FROM index_admission
		  WHERE root_id = $1::uuid AND status = 'PENDING'
		  ORDER BY admission_seq
		  LIMIT 1`, rootID).Scan(&a.RootID, &a.AdmissionSeq, &a.SnapshotID, &a.AdmittedAt, &status,
		&a.AppliedGeneration, &a.ClaimedBy, &a.ClaimedAt, &a.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Admission{}, ErrNotFound
	}
	if err != nil {
		return domain.Admission{}, err
	}
	a.Status = domain.AdmissionStatus(status)
	return a, nil
}

// ClaimHead marks the head-of-line admission as claimed with a bounded lease. It
// never renumbers the sequence (doc B RC2/RC3).
func (s *Store) ClaimHead(ctx context.Context, q Querier, rootID string, seq int64, claimedBy string, leaseSeconds int) error {
	tag, err := q.Exec(ctx,
		`UPDATE index_admission
		    SET claimed_by = $3, claimed_at = now(),
		        lease_expires_at = now() + make_interval(secs => $4)
		  WHERE root_id = $1::uuid AND admission_seq = $2 AND status = 'PENDING'
		    AND (claimed_by IS NULL OR lease_expires_at < now())`,
		rootID, seq, claimedBy, leaseSeconds)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// SetAdmissionStatus transitions an admission row to a terminal status.
func (s *Store) SetAdmissionStatus(ctx context.Context, q Querier, rootID string, seq int64, status domain.AdmissionStatus, appliedGeneration *int64) error {
	_, err := q.Exec(ctx,
		`UPDATE index_admission
		    SET status = $3, applied_generation = $4
		  WHERE root_id = $1::uuid AND admission_seq = $2`,
		rootID, seq, string(status), appliedGeneration)
	return err
}

// AppliedMax returns the greatest APPLIED admission_seq for a root, or 0 if none.
func (s *Store) AppliedMax(ctx context.Context, q Querier, rootID string) (int64, error) {
	var n *int64
	if err := q.QueryRow(ctx,
		`SELECT max(admission_seq) FROM index_admission
		  WHERE root_id = $1::uuid AND status = 'APPLIED'`, rootID).Scan(&n); err != nil {
		return 0, err
	}
	if n == nil {
		return 0, nil
	}
	return *n, nil
}
