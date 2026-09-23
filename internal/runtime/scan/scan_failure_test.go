package scan_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func failureStore(t *testing.T) (*postgres.Store, context.Context, string) {
	t.Helper()
	pool := testutil.Pool(t)
	ctx := context.Background()
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	const rootID = "a5000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	return st, ctx, rootID
}

func persistedLifecycle(t *testing.T, st *postgres.Store, ctx context.Context, snapID string) domain.SnapshotLifecycleState {
	t.Helper()
	var lc string
	if err := st.Pool().QueryRow(ctx, `SELECT lifecycle_state FROM index_snapshot WHERE snapshot_id=$1::uuid`, snapID).Scan(&lc); err != nil {
		t.Fatalf("snapshot lifecycle: %v", err)
	}
	return domain.SnapshotLifecycleState(lc)
}

// G3-R2.3: a failed rclone traversal is durably REJECTED AND reported as a
// non-nil service error (CLI exits non-zero).
func TestScanReturnsErrorOnRcloneFailure(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone binary not installed")
	}
	st, ctx, rootID := failureStore(t)
	cfg, _ := json.Marshal(map[string]string{"remote": "indexcore-nonexistent-remote", "path": "/"})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "rclone", Config: cfg}); err != nil {
		t.Fatal(err)
	}
	svc := scan.New(st, "rclone", "", 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	res, err := svc.Scan(ctx, rootID)
	if err == nil || !errors.Is(err, scan.ErrSourceFailed) {
		t.Fatalf("failed rclone traversal must return ErrSourceFailed, got %v", err)
	}
	if lc := persistedLifecycle(t, st, ctx, res.SnapshotID); lc != domain.SnapshotRejected {
		t.Fatalf("failed snapshot must be durably REJECTED, got %s", lc)
	}
}

// G3-R2.3: a failing AList HTTP/list is persisted REJECTED and returns an error.
func TestScanReturnsErrorOnAListFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"code":500,"message":"boom","data":null}`)
	}))
	defer srv.Close()

	st, ctx, rootID := failureStore(t)
	cfg, _ := json.Marshal(map[string]string{"base_url": srv.URL, "path": "/"})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "alist", Config: cfg}); err != nil {
		t.Fatal(err)
	}
	svc := scan.New(st, "rclone", "", 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	res, err := svc.Scan(ctx, rootID)
	if err == nil || !errors.Is(err, scan.ErrSourceFailed) {
		t.Fatalf("failed AList traversal must return ErrSourceFailed, got %v", err)
	}
	if lc := persistedLifecycle(t, st, ctx, res.SnapshotID); lc != domain.SnapshotRejected {
		t.Fatalf("failed snapshot must be durably REJECTED, got %s", lc)
	}
}

func newDraftFixture(t *testing.T, snapID, rootID string) (domain.Snapshot, []domain.SnapshotEntry) {
	t.Helper()
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	count := int64(1)
	mt := time.Now().UTC()
	size := int64(1)
	alg := "sha256"
	hash := "h1"
	return domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: mt,
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft, EntryCount: &count,
	}, []domain.SnapshotEntry{{
		EntryLocalID: "a.txt", Name: "a.txt", ParentRef: "/", Size: &size, Mtime: &mt,
		ContentHash: &hash, HashAlgorithm: &alg,
	}}
}

// G3-R2.5(1) / G3-R4(6): interruption AFTER the DRAFT is persisted but BEFORE the
// Stage-1 finalize must leave NO executable work. This exercises the real runtime
// persistence API (CreateDraftSnapshot), not hand-built DB state.
func TestInterruptionBeforeFinalizeLeavesNoExecutableWork(t *testing.T) {
	st, ctx, rootID := failureStore(t)
	snapID := "a6000000-0000-0000-0000-000000000001"
	snap, entries := newDraftFixture(t, snapID, rootID)
	if err := st.CreateDraftSnapshot(ctx, snap, entries); err != nil {
		t.Fatal(err)
	}
	// Crash before SubmitAndAdmitSnapshot: the DRAFT is inert and recovery must not
	// allocate an admission_seq for it.
	resolved, ambiguous, err := st.ResolveUnadmittedSubmittedForRoot(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 0 || ambiguous {
		t.Fatalf("DRAFT must not be recovered as executable work, resolved=%d ambiguous=%v", resolved, ambiguous)
	}
	var admissions, canonical int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, rootID).Scan(&admissions)
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE root_id=$1::uuid`, rootID).Scan(&canonical)
	if admissions != 0 || canonical != 0 {
		t.Fatalf("crash before finalize must leave no executable work, admissions=%d canonical=%d", admissions, canonical)
	}
	if lc := persistedLifecycle(t, st, ctx, snapID); lc != domain.SnapshotDraft {
		t.Fatalf("snapshot must remain DRAFT, got %s", lc)
	}
}

// G3-R4(6): a crash AFTER finalize/admit but BEFORE Stage-2 leaves a durable
// PENDING admission that recovery processes by reusing the SAME admission_seq.
func TestFinalizeAdmitCrashBeforeStage2LeavesResumablePending(t *testing.T) {
	st, ctx, rootID := failureStore(t)
	snapID := "a8000000-0000-0000-0000-000000000001"
	snap, entries := newDraftFixture(t, snapID, rootID)
	if err := st.CreateDraftSnapshot(ctx, snap, entries); err != nil {
		t.Fatal(err)
	}
	seq, err := st.SubmitAndAdmitSnapshot(ctx, rootID, snapID)
	if err != nil {
		t.Fatal(err)
	}
	// Crash before Stage-2: the durable PENDING head exists, no canonical state yet.
	var pending int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid AND status='PENDING'`, rootID).Scan(&pending)
	if pending != 1 {
		t.Fatalf("crash before Stage-2 must leave exactly one durable PENDING admission, got %d", pending)
	}
	if lc := persistedLifecycle(t, st, ctx, snapID); lc != domain.SnapshotSubmitted {
		t.Fatalf("snapshot must be SUBMITTED, got %s", lc)
	}
	cfg, _ := st.RootReconcileConfig(ctx, rootID)
	coord := postgres.NewCoordinator(st, cfg)
	out, done, err := coord.ProcessHead(ctx, rootID)
	if err != nil || !done || out.Status != domain.AdmissionApplied {
		t.Fatalf("resume failed: out=%+v done=%v err=%v", out, done, err)
	}
	adm, err := st.GetAdmission(ctx, st.Pool(), rootID, seq)
	if err != nil {
		t.Fatal(err)
	}
	if adm.Status != domain.AdmissionApplied {
		t.Fatalf("resume must reuse admission_seq %d (now APPLIED), got %s", seq, adm.Status)
	}
	var canonical int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE root_id=$1::uuid`, rootID).Scan(&canonical)
	if canonical != 1 {
		t.Fatalf("resume must apply the resource, got canonical=%d", canonical)
	}
}

// G3-R2.5(2) / G3-R4(6): a FAILED traversal persisted through the real runtime
// API is durably REJECTED and leaves no canonical state.
func TestFailureAfterSnapshotCreationIsAudited(t *testing.T) {
	st, ctx, rootID := failureStore(t)
	snapID := "a7000000-0000-0000-0000-000000000001"
	snap, entries := newDraftFixture(t, snapID, rootID)
	snap.TraversalStatus = domain.TraversalFailed
	snap.CompletenessFlag = domain.CompletenessFlagPartial
	if err := st.CreateDraftSnapshot(ctx, snap, entries); err != nil {
		t.Fatal(err)
	}
	seq, err := st.SubmitAndAdmitSnapshot(ctx, rootID, snapID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := st.RootReconcileConfig(ctx, rootID)
	out, done, err := postgres.NewCoordinator(st, cfg).ProcessHead(ctx, rootID)
	if err != nil || !done {
		t.Fatalf("process head: out=%+v done=%v err=%v", out, done, err)
	}
	if out.Status != domain.AdmissionRejected {
		t.Fatalf("FAILED traversal must be REJECTED, got %s", out.Status)
	}
	if lc := persistedLifecycle(t, st, ctx, snapID); lc != domain.SnapshotRejected {
		t.Fatalf("snapshot must be REJECTED, got %s", lc)
	}
	adm, _ := st.GetAdmission(ctx, st.Pool(), rootID, seq)
	if adm.Status != domain.AdmissionRejected {
		t.Fatalf("admission must be REJECTED, got %s", adm.Status)
	}
	var canonical int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE root_id=$1::uuid`, rootID).Scan(&canonical)
	if canonical != 0 {
		t.Fatalf("rejected snapshot must not create canonical state, got %d", canonical)
	}
}

// G3-R4(4): a PARTIAL traversal is a legal additive-safe input. It is Kernel-
// evaluated (acceptance_state=PARTIAL), the Snapshot is NOT rejected, and it
// never advances removal evidence.
func TestPartialTraversalIsEvaluatedNotRejected(t *testing.T) {
	st, ctx, rootID := failureStore(t)
	snapID := "a9000000-0000-0000-0000-0000000000c1"
	snap, entries := newDraftFixture(t, snapID, rootID)
	snap.TraversalStatus = domain.TraversalPartial
	snap.CompletenessFlag = domain.CompletenessFlagPartial
	snap.SkippedScopesKnownEmpty = false
	weak := domain.WeakFailureVisibility
	snap.CollectorCompletenessAssurance = &weak
	if err := st.CreateDraftSnapshot(ctx, snap, entries); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SubmitAndAdmitSnapshot(ctx, rootID, snapID); err != nil {
		t.Fatal(err)
	}
	cfg, _ := st.RootReconcileConfig(ctx, rootID)
	out, done, err := postgres.NewCoordinator(st, cfg).ProcessHead(ctx, rootID)
	if err != nil || !done {
		t.Fatalf("process head: out=%+v done=%v err=%v", out, done, err)
	}
	if out.Status == domain.AdmissionRejected {
		t.Fatalf("PARTIAL traversal must not be REJECTED, got %s", out.Status)
	}
	if lc := persistedLifecycle(t, st, ctx, snapID); lc == domain.SnapshotRejected {
		t.Fatalf("PARTIAL snapshot must not be REJECTED, got %s", lc)
	}
	var acceptance *string
	if err := st.Pool().QueryRow(ctx, `SELECT acceptance_state FROM index_snapshot WHERE snapshot_id=$1::uuid`, snapID).Scan(&acceptance); err != nil {
		t.Fatal(err)
	}
	if acceptance == nil || *acceptance != string(domain.AcceptancePartial) {
		t.Fatalf("PARTIAL traversal acceptance must be PARTIAL, got %v", acceptance)
	}
}
