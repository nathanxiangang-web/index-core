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

// G3-R2.5(1): interruption before SUBMITTED leaves no executable Snapshot.
func TestInterruptionBeforeSubmittedLeavesNoExecutableSnapshot(t *testing.T) {
	st, ctx, rootID := failureStore(t)
	snapID := "a6000000-0000-0000-0000-000000000001"
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	// DRAFT only: never SUBMITTED.
	if err := st.InsertSnapshotStub(ctx, st.Pool(), domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft,
	}); err != nil {
		t.Fatal(err)
	}
	resolved, ambiguous, err := st.ResolveUnadmittedSubmitted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 0 || len(ambiguous) != 0 {
		t.Fatalf("a DRAFT snapshot must not be admitted, got resolved=%d ambiguous=%v", resolved, ambiguous)
	}
	var admissions, canonical int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, rootID).Scan(&admissions)
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE root_id=$1::uuid`, rootID).Scan(&canonical)
	if admissions != 0 || canonical != 0 {
		t.Fatalf("interruption before SUBMITTED must leave no executable work, admissions=%d canonical=%d", admissions, canonical)
	}
}

// G3-R2.5(2): failure after Snapshot creation is durably audited (REJECTED) and
// leaves no canonical state.
func TestFailureAfterSnapshotCreationIsAudited(t *testing.T) {
	st, ctx, rootID := failureStore(t)
	snapID := "a7000000-0000-0000-0000-000000000001"
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	// A SUBMITTED snapshot with FAILED traversal + admitted (simulating a scan that
	// crashed after creating the snapshot, or a collector that failed).
	if err := st.InsertSnapshotStub(ctx, st.Pool(), domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalFailed, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagPartial, LifecycleState: domain.SnapshotDraft,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatal(err)
	}
	seq, _, err := st.AdmitOrResumeSnapshot(ctx, rootID, snapID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := st.RootReconcileConfig(ctx, rootID)
	out, err := postgres.NewCoordinator(st, cfg).ProcessSnapshot(ctx, rootID, snapID)
	if err != nil {
		t.Fatalf("process: %v", err)
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
