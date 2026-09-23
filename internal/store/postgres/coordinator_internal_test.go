package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func seedInternal(t *testing.T, st *Store, ctx context.Context, rootID, snapID string) {
	t.Helper()
	if _, err := st.Pool().Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := Migrate(ctx, st.Pool()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("root: %v", err)
	}
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft,
	}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), snap); err != nil {
		t.Fatalf("snap: %v", err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatalf("submit: %v", err)
	}
}

func newInternalStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	pool := testutil.Pool(t)
	return New(pool), context.Background()
}

func insertSubmittedInternal(t *testing.T, st *Store, ctx context.Context, rootID, snapID string) {
	t.Helper()
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft,
	}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), snap); err != nil {
		t.Fatalf("snap: %v", err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatalf("submit: %v", err)
	}
}

// R4-1: retrying the same Snapshot must reuse its existing PENDING seq.
func TestAdmitOrResumeReusesSameSeq(t *testing.T) {
	st, ctx := newInternalStore(t)
	const rootID = "ac000000-0000-0000-0000-000000000001"
	const snapID = "ac000000-0000-0000-0000-0000000000a1"
	seedInternal(t, st, ctx, rootID, snapID)

	seq1, resumed1, err := st.AdmitOrResumeSnapshot(ctx, rootID, snapID)
	if err != nil || resumed1 {
		t.Fatalf("first admit must allocate a new seq, got seq=%d resumed=%v err=%v", seq1, resumed1, err)
	}
	seq2, resumed2, err := st.AdmitOrResumeSnapshot(ctx, rootID, snapID)
	if err != nil || seq2 != seq1 || !resumed2 {
		t.Fatalf("retry must reuse seq %d, got seq=%d resumed=%v err=%v", seq1, seq2, resumed2, err)
	}
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, rootID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("retry must not duplicate PENDING rows, got %d", n)
	}
}

// R4-1: a stranded later input must progress via the head path without a new seq.
func TestStrandedInputProgressesWithoutNewAdmission(t *testing.T) {
	st, ctx := newInternalStore(t)
	const rootID = "ae000000-0000-0000-0000-000000000001"
	const s1 = "ae000000-0000-0000-0000-0000000000a1"
	const s2 = "ae000000-0000-0000-0000-0000000000a2"
	seedInternal(t, st, ctx, rootID, s1)
	insertSubmittedInternal(t, st, ctx, rootID, s2)

	if _, _, err := st.AdmitOrResumeSnapshot(ctx, rootID, s1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AdmitOrResumeSnapshot(ctx, rootID, s2); err != nil {
		t.Fatal(err)
	}

	c := NewCoordinator(st, reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1})
	if _, err := c.ProcessSnapshot(ctx, rootID, s2); err != ErrNotHead {
		t.Fatalf("s2 must be ErrNotHead while s1 is head, got %v", err)
	}
	if out, err := c.ProcessSnapshot(ctx, rootID, s1); err != nil || out.Status != domain.AdmissionApplied {
		t.Fatalf("s1 must apply, got %+v err=%v", out, err)
	}
	out, done, err := c.ProcessHead(ctx, rootID)
	if err != nil || !done {
		t.Fatalf("stranded s2 must be processed by the head path, got done=%v err=%v", done, err)
	}
	if out.Status != domain.AdmissionApplied && out.Status != domain.AdmissionNoop {
		t.Fatalf("stranded s2 must reach a terminal status, got %s", out.Status)
	}
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, rootID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("recovery must not allocate extra admissions, got %d", n)
	}
}

// R3-5: a superseded input is classified STALE_INPUT, distinct from REJECTED.
func TestStaleInputClassification(t *testing.T) {
	st, ctx := newInternalStore(t)
	const rootID = "aa000000-0000-0000-0000-000000000001"
	const snapID = "aa000000-0000-0000-0000-0000000000a1"
	seedInternal(t, st, ctx, rootID, snapID)

	seq, err := st.AdmitSnapshot(ctx, rootID, snapID)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	// Simulate a newer already-applied input so this head is superseded.
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO index_admission(root_id, admission_seq, snapshot_id, status, applied_generation)
		 VALUES ($1::uuid, $2, $3::uuid, 'APPLIED', 1)`,
		rootID, seq+1, snapID); err != nil {
		t.Fatalf("simulate applied newer: %v", err)
	}

	out, err := st.reconcileHeadSafe(ctx, rootID, seq, snapID, reconcile.Config{})
	if err != nil {
		t.Fatalf("reconcileHeadSafe: %v", err)
	}
	if out.Status != domain.AdmissionStaleInput {
		t.Fatalf("superseded input must be STALE_INPUT, got %s", out.Status)
	}
	var admissionStatus, outcome string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM index_admission WHERE root_id=$1::uuid AND admission_seq=$2`, rootID, seq).Scan(&admissionStatus); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT outcome FROM index_reconcile_result WHERE root_id=$1::uuid AND admission_seq=$2`, rootID, seq).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if admissionStatus != "STALE_INPUT" || outcome != "STALE_INPUT" {
		t.Fatalf("admission/result must be STALE_INPUT, got %s/%s", admissionStatus, outcome)
	}
}

// R3-4: Stage-1 rejects a Snapshot that does not belong to the root, and Stage-2
// rejects a mismatched snapshot binding.
func TestAdmissionBindingValidation(t *testing.T) {
	st, ctx := newInternalStore(t)
	const rootID = "ab000000-0000-0000-0000-000000000001"
	const snapID = "ab000000-0000-0000-0000-0000000000a1"
	seedInternal(t, st, ctx, rootID, snapID)

	// Wrong root for this snapshot.
	if _, err := st.AdmitSnapshot(ctx, "ab000000-0000-0000-0000-0000000000ff", snapID); err != ErrNotFound {
		t.Fatalf("unknown root must be ErrNotFound, got %v", err)
	}
	// Wrong snapshot for this root.
	if _, err := st.AdmitSnapshot(ctx, rootID, "ab000000-0000-0000-0000-0000000000fe"); err != ErrNotFound {
		t.Fatalf("unknown snapshot must be ErrNotFound, got %v", err)
	}
	// Correct binding succeeds; then a mismatched Stage-2 snapshot is rejected.
	seq, err := st.AdmitSnapshot(ctx, rootID, snapID)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if _, err := st.reconcileHeadSafe(ctx, rootID, seq, "ab000000-0000-0000-0000-0000000000fe", reconcile.Config{}); err != ErrBindingMismatch {
		t.Fatalf("mismatched snapshot binding must be ErrBindingMismatch, got %v", err)
	}
}
