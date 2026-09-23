package e2e_test

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
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/runtime/worker"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// TestAlphaEndToEnd exercises the Issue #47 P10 scenario from an empty DB.
func TestAlphaEndToEnd(t *testing.T) {
	rcloneBin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone binary not installed; skipping Alpha E2E")
	}
	pool := testutil.Pool(t)
	ctx := context.Background()
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("1 migrate: %v", err)
	}
	st := postgres.New(pool)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// 2. create/configure root.
	const rootID = "e1000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("2 create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("2 policy: %v", err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "hello.txt"), "hello")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "nested.txt"), "nested")
	acfg, _ := json.Marshal(map[string]string{"remote": "", "path": dir})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "rclone", Config: acfg}); err != nil {
		t.Fatalf("2 adapter: %v", err)
	}
	svc := scan.New(st, rcloneBin, "", 30*time.Second, logger)

	// 3-4. scan drives DRAFT -> SUBMITTED -> Coordinator -> canonical inventory.
	res1, err := svc.Scan(ctx, rootID)
	if err != nil {
		t.Fatalf("3 scan: %v", err)
	}
	if res1.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("4 first scan must APPLY, got %s", res1.Outcome.Status)
	}
	// 11. rclone UNKNOWN skips stay non-destructive (never confirmed-empty).
	var knownEmpty bool
	_ = pool.QueryRow(ctx, `SELECT skipped_scopes_known_empty FROM index_snapshot WHERE snapshot_id=$1::uuid`, res1.SnapshotID).Scan(&knownEmpty)
	if knownEmpty {
		t.Fatal("11 rclone must not claim confirmed-empty skip evidence")
	}

	// 5 + 10. Query returns resources incl. nested.
	qr := postgres.NewQueryReader(pool)
	active, err := qr.ListActivePage(ctx, rootID, nil, 100)
	if err != nil {
		t.Fatalf("5 query: %v", err)
	}
	if len(active.Items) < 3 {
		t.Fatalf("5 expected >=3 active resources, got %d", len(active.Items))
	}
	resolved, err := qr.ResolvePath(ctx, rootID, "/sub/nested.txt", query.ReadOptions{})
	if err != nil || len(resolved.Matches) != 1 {
		t.Fatalf("10 nested resolve: %v matches=%d", err, len(resolved.Matches))
	}

	// 6. repeat identical scan -> correct idempotency (NOOP).
	res2, err := svc.Scan(ctx, rootID)
	if err != nil {
		t.Fatalf("6 scan: %v", err)
	}
	if res2.Outcome.Status != domain.AdmissionNoop {
		t.Fatalf("6 identical repeat must be NOOP, got %s", res2.Outcome.Status)
	}

	// 7. small source change -> correct additive reconcile.
	writeFile(t, filepath.Join(dir, "added.txt"), "added")
	res3, err := svc.Scan(ctx, rootID)
	if err != nil {
		t.Fatalf("7 scan: %v", err)
	}
	if res3.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("7 delta must APPLY, got %s", res3.Outcome.Status)
	}
	if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), rootID, "/added.txt"); len(rows) != 1 {
		t.Fatalf("7 added.txt must be canonical, got %d", len(rows))
	}
	// 11. no destructive removal came from rclone (nothing REMOVED so far).
	var removed int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE root_id=$1::uuid AND resource_presence='REMOVED'`, rootID).Scan(&removed)
	if removed != 0 {
		t.Fatalf("11 no resource should be removed from additive-safe rclone scans, got %d", removed)
	}

	// 9. Journal ordered and readable.
	events, err := qr.ReadJournal(ctx, rootID, 0, 1000)
	if err != nil {
		t.Fatalf("9 journal: %v", err)
	}
	for i := 1; i < len(events); i++ {
		if events[i].EventSeq <= events[i-1].EventSeq {
			t.Fatalf("9 journal must be per-root event_seq ordered")
		}
	}

	// 8. restart recovery: a durable PENDING admission resumes with the SAME seq.
	const root2 = "e1000000-0000-0000-0000-000000000002"
	if err := st.CreateRoot(ctx, st.Pool(), root2, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRootPolicy(ctx, root2, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatal(err)
	}
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	count := int64(1)
	snap2 := "e1000000-0000-0000-0000-0000000000a2"
	size := int64(1)
	alg, scope := "sha256", "root"
	mt := time.Now().UTC()
	hash := "he"
	prov := "PE"
	entry := domain.SnapshotEntry{EntryLocalID: "e.txt", Name: "e.txt", ParentRef: "/",
		Size: &size, Mtime: &mt, ContentHash: &hash, HashAlgorithm: &alg,
		ProviderObjectID: &prov, ProviderObjectIDScope: &scope}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), domain.Snapshot{
		SnapshotID: snap2, RootID: root2, Provenance: []byte(`{}`), ObservedAt: mt,
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft, EntryCount: &count,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snap2); err != nil {
		t.Fatal(err)
	}
	entry.SnapshotID = snap2
	if err := st.InsertSnapshotEntry(ctx, st.Pool(), entry); err != nil {
		t.Fatal(err)
	}
	seq, _, err := st.AdmitOrResumeSnapshot(ctx, root2, snap2) // Stage-1 only = pre-restart
	if err != nil {
		t.Fatalf("8 admit: %v", err)
	}
	wk := worker.NewWithIntervals(st, 4, 50*time.Millisecond, 50*time.Millisecond, logger)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = wk.Run(wctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		adm, err := st.GetAdmission(ctx, st.Pool(), root2, seq)
		if err == nil && adm.Status == domain.AdmissionApplied {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("8 pending head not resumed after restart (last=%+v err=%v)", adm, err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	var admissions int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, root2).Scan(&admissions)
	if admissions != 1 {
		t.Fatalf("8 restart recovery must reuse the same admission, got %d rows", admissions)
	}
	cancel()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
