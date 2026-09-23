package scan_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/runtime/worker"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func recoverySetup(t *testing.T, rcloneBin, dir string) (*postgres.Store, context.Context, string) {
	t.Helper()
	pool := testutil.Pool(t)
	ctx := context.Background()
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	const rootID = "e2000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	acfg, _ := json.Marshal(map[string]string{"remote": "", "path": dir})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "rclone", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}
	return st, ctx, rootID
}

// G3-R3(1): a durable SUBMITTED Snapshot with no admission (crash before Stage-1)
// is recovered by the worker sweep and processed.
func TestWorkerRecoversUnadmittedSubmittedSnapshot(t *testing.T) {
	rcloneBin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone binary not installed")
	}
	dir := t.TempDir()
	writeF(t, filepath.Join(dir, "a.txt"), "a")
	st, ctx, rootID := recoverySetup(t, rcloneBin, dir)

	// Simulate the crash: persist SUBMITTED + entries, never admit.
	snapID := "e3000000-0000-0000-0000-000000000001"
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	count := int64(1)
	alg, scope, mt := "sha256", "root", time.Now().UTC()
	hash, size, prov := "ha", int64(1), "PA"
	if err := st.InsertSnapshotStub(ctx, st.Pool(), domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: mt,
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft, EntryCount: &count,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertSnapshotEntry(ctx, st.Pool(), domain.SnapshotEntry{
		SnapshotID: snapID, EntryLocalID: "a.txt", Name: "a.txt", ParentRef: "/",
		Size: &size, Mtime: &mt, ContentHash: &hash, HashAlgorithm: &alg,
		ProviderObjectID: &prov, ProviderObjectIDScope: &scope,
	}); err != nil {
		t.Fatal(err)
	}

	w := worker.NewWithIntervals(st, 4, 50*time.Millisecond, 50*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = w.Run(wctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		var admissions, applied int
		_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE snapshot_id=$1::uuid`, snapID).Scan(&admissions)
		_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE snapshot_id=$1::uuid AND status='APPLIED'`, snapID).Scan(&applied)
		if admissions == 1 && applied == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unadmitted SUBMITTED snapshot was not recovered (admissions=%d applied=%d)", admissions, applied)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// G3-R3(2): rerunning scan after an interrupted prior scan drains the older
// PENDING head instead of stranding/leapfrogging it.
func TestScanRerunDrainsOlderPending(t *testing.T) {
	rcloneBin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone binary not installed")
	}
	dir := t.TempDir()
	writeF(t, filepath.Join(dir, "a.txt"), "a")
	st, ctx, rootID := recoverySetup(t, rcloneBin, dir)

	// Interrupted prior scan: SUBMITTED + PENDING seq=1, never processed.
	oldSnap := "e4000000-0000-0000-0000-000000000001"
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	count := int64(1)
	if err := st.InsertSnapshotStub(ctx, st.Pool(), domain.Snapshot{
		SnapshotID: oldSnap, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft, EntryCount: &count,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), oldSnap); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AdmitOrResumeSnapshot(ctx, rootID, oldSnap); err != nil {
		t.Fatal(err)
	}

	// Rerun scan with fresh content.
	svc := scan.New(st, rcloneBin, "", 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := svc.Scan(ctx, rootID); err != nil {
		t.Fatalf("scan rerun: %v", err)
	}

	var pending int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid AND status='PENDING'`, rootID).Scan(&pending)
	if pending != 0 {
		t.Fatalf("rerun must drain older pending head, %d still pending", pending)
	}
	// The freshly scanned resource is canonical.
	if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), rootID, "/a.txt"); len(rows) != 1 {
		t.Fatalf("fresh scan result must be canonical, got %d", len(rows))
	}
}

func writeF(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
