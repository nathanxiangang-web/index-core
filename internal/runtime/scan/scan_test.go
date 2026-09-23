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
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestScanEndToEndWithRealRclone(t *testing.T) {
	rcloneBin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone binary not installed; skipping scan E2E")
	}
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	ctx := context.Background()

	const rootID = "e0000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatal(err)
	}
	acfg, _ := json.Marshal(map[string]string{"remote": "", "path": dir})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "rclone", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := scan.New(st, rcloneBin, "", 30*time.Second, logger)

	res, err := svc.Scan(ctx, rootID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if res.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("first scan must APPLY, got %+v", res.Outcome)
	}
	if res.Outcome.AppliedGeneration != 1 {
		t.Fatalf("first scan must reach generation 1, got %d", res.Outcome.AppliedGeneration)
	}
	// Canonical Inventory populated through the Kernel; nested resource present.
	if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), rootID, "/sub/nested.txt"); len(rows) != 1 {
		t.Fatalf("nested resource must be canonical, got %d", len(rows))
	}
	// rclone skip evidence stays UNKNOWN (non-destructive authority).
	var knownEmpty bool
	if err := st.Pool().QueryRow(ctx, `SELECT skipped_scopes_known_empty FROM index_snapshot WHERE snapshot_id=$1::uuid`, res.SnapshotID).Scan(&knownEmpty); err != nil {
		t.Fatal(err)
	}
	if knownEmpty {
		t.Fatal("rclone must not claim confirmed-empty skip evidence")
	}

	// A repeated identical scan is idempotent at the current generation (NOOP).
	res2, err := svc.Scan(ctx, rootID)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if res2.Outcome.Status != domain.AdmissionNoop {
		t.Fatalf("identical repeat scan must be NOOP, got %s", res2.Outcome.Status)
	}
}

func TestScanRejectsDeletedRoot(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	ctx := context.Background()
	const rootID = "e0000000-0000-0000-0000-000000000002"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "rclone", Config: []byte(`{"remote":"","path":"/tmp"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TransitionRootLifecycle(ctx, rootID, domain.RootDeprecated); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TransitionRootLifecycle(ctx, rootID, domain.RootDeleted); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := scan.New(st, "rclone", "", 5*time.Second, logger).Scan(ctx, rootID); err == nil {
		t.Fatal("scan of a DELETED root must be rejected")
	}
}
