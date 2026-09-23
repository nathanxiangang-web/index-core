package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func seedRootAndPolicy(t *testing.T, st *postgres.Store, ctx context.Context, rootID string) {
	t.Helper()
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
}

func seedSubmittedNoAdmission(t *testing.T, st *postgres.Store, ctx context.Context, rootID, snapID string) {
	t.Helper()
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	if err := st.InsertSnapshotStub(ctx, st.Pool(), domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft,
	}); err != nil {
		t.Fatalf("stub: %v", err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatalf("submit: %v", err)
	}
}

// G3-R2.1: two ambiguous same-root unadmitted SUBMITTED Snapshots must fail closed
// rather than being ordered by DB creation time.
func TestAmbiguousUnadmittedSubmittedFailsClosed(t *testing.T) {
	st, ctx := newStore(t)
	const rootID = "a2000000-0000-0000-0000-000000000001"
	seedRootAndPolicy(t, st, ctx, rootID)
	seedSubmittedNoAdmission(t, st, ctx, rootID, "a2000000-0000-0000-0000-0000000000a1")
	time.Sleep(5 * time.Millisecond)
	seedSubmittedNoAdmission(t, st, ctx, rootID, "a2000000-0000-0000-0000-0000000000a2")

	resolved, ambiguous, err := st.ResolveUnadmittedSubmitted(ctx)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved != 0 {
		t.Fatalf("ambiguous candidates must not be auto-admitted by timestamp order, resolved=%d", resolved)
	}
	if len(ambiguous) != 1 || ambiguous[0] != rootID {
		t.Fatalf("root with multiple candidates must be reported ambiguous, got %v", ambiguous)
	}
	var admissions int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, rootID).Scan(&admissions)
	if admissions != 0 {
		t.Fatalf("no admission may be created for ambiguous recovery, got %d", admissions)
	}
}

// G3-R2.1: a single unadmitted SUBMITTED Snapshot is resolved deterministically.
func TestSingleUnadmittedSubmittedResolved(t *testing.T) {
	st, ctx := newStore(t)
	const rootID = "a3000000-0000-0000-0000-000000000001"
	seedRootAndPolicy(t, st, ctx, rootID)
	seedSubmittedNoAdmission(t, st, ctx, rootID, "a3000000-0000-0000-0000-0000000000a1")

	resolved, ambiguous, err := st.ResolveUnadmittedSubmitted(ctx)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved != 1 || len(ambiguous) != 0 {
		t.Fatalf("single candidate must be resolved, got resolved=%d ambiguous=%v", resolved, ambiguous)
	}
	var admissions int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, rootID).Scan(&admissions)
	if admissions != 1 {
		t.Fatalf("exactly one admission expected, got %d", admissions)
	}
}

// G3-R2.1: the split Stage-1 path (DRAFT persisted WITHOUT the root lock, then a
// short DRAFT->SUBMITTED + admission transaction) leaves no SUBMITTED-but-
// unadmitted state, while a DRAFT-only crash is not executable.
func TestSplitStage1LeavesNoUnadmittedSubmitted(t *testing.T) {
	st, ctx := newStore(t)
	const rootID = "a4000000-0000-0000-0000-000000000001"
	seedRootAndPolicy(t, st, ctx, rootID)
	snapID := "a4000000-0000-0000-0000-0000000000a1"
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	count := int64(0)
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft, EntryCount: &count,
	}
	if err := st.CreateDraftSnapshot(ctx, snap, nil); err != nil {
		t.Fatalf("create draft: %v", err)
	}
	// A persisted DRAFT is inert: it is not recoverable as unadmitted SUBMITTED work.
	resolved, ambiguous, err := st.ResolveUnadmittedSubmittedForRoot(ctx, rootID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved != 0 || ambiguous {
		t.Fatalf("DRAFT must not be recovered as unadmitted SUBMITTED, got resolved=%d ambiguous=%v", resolved, ambiguous)
	}
	seq, err := st.SubmitAndAdmitSnapshot(ctx, rootID, snapID)
	if err != nil {
		t.Fatalf("submit+admit: %v", err)
	}
	if seq != 1 {
		t.Fatalf("first admission_seq must be 1, got %d", seq)
	}
	unadmitted, _ := st.UnadmittedSubmittedByRoot(ctx)
	if len(unadmitted) != 0 {
		t.Fatalf("split Stage-1 must leave no unadmitted SUBMITTED state, got %v", unadmitted)
	}
}

// G3-R4(2): root-scoped recovery used by `scan --root A` must never admit
// stranded SUBMITTED work belonging to another root.
func TestRootScopedRecoveryDoesNotTouchOtherRoots(t *testing.T) {
	st, ctx := newStore(t)
	const rootA = "a9000000-0000-0000-0000-00000000000a"
	const rootB = "a9000000-0000-0000-0000-00000000000b"
	seedRootAndPolicy(t, st, ctx, rootA)
	seedRootAndPolicy(t, st, ctx, rootB)
	snapB := "a9000000-0000-0000-0000-0000000000b1"
	seedSubmittedNoAdmission(t, st, ctx, rootB, snapB)

	// `scan --root A` resolves only A; B is left untouched.
	resolved, ambiguous, err := st.ResolveUnadmittedSubmittedForRoot(ctx, rootA)
	if err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	if resolved != 0 || ambiguous {
		t.Fatalf("root A has no stranded work, got resolved=%d ambiguous=%v", resolved, ambiguous)
	}
	var admissionsB int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, rootB).Scan(&admissionsB)
	if admissionsB != 0 {
		t.Fatalf("root-scoped recovery must not admit another root's work, got %d", admissionsB)
	}

	// The owning root still recovers its own single stranded snapshot.
	resolved, ambiguous, err = st.ResolveUnadmittedSubmittedForRoot(ctx, rootB)
	if err != nil {
		t.Fatalf("resolve B: %v", err)
	}
	if resolved != 1 || ambiguous {
		t.Fatalf("root B single candidate must resolve, got resolved=%d ambiguous=%v", resolved, ambiguous)
	}
}

// G3-R5.1/R5.2: the only way to create Snapshot work is CreateDraftSnapshot
// (which forces DRAFT). Supplying another lifecycle_state must be rejected so no
// caller can smuggle a SUBMITTED-but-unadmitted Snapshot past admission, and a
// rejected create persists nothing.
func TestCreateDraftSnapshotRejectsNonDraft(t *testing.T) {
	st, ctx := newStore(t)
	const rootID = "aa000000-0000-0000-0000-000000000001"
	seedRootAndPolicy(t, st, ctx, rootID)
	snapID := "aa000000-0000-0000-0000-0000000000a1"
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	err := st.CreateDraftSnapshot(ctx, domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotSubmitted,
	}, nil)
	if !errors.Is(err, postgres.ErrNotDraft) {
		t.Fatalf("non-DRAFT lifecycle_state must be rejected, got %v", err)
	}
	var snapshots, admissions int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_snapshot WHERE root_id=$1::uuid`, rootID).Scan(&snapshots)
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, rootID).Scan(&admissions)
	if snapshots != 0 || admissions != 0 {
		t.Fatalf("rejected create must persist nothing, snapshots=%d admissions=%d", snapshots, admissions)
	}
}
