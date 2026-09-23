package worker_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/worker"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// G3-R4: an already-active root must not occupy multiple slots; other roots must
// still progress.
func TestWorkerDoesNotStarveOtherRoots(t *testing.T) {
	st, ctx := newWorkerStore(t)
	const rootA = "b0000000-0000-0000-0000-0000000000a1"
	const rootB = "b0000000-0000-0000-0000-0000000000b1"
	seedPendingRoot(t, st, ctx, rootA, "b0000000-0000-0000-0000-000000000a01", "a.txt", "ha")
	// Give root A several extra durable pending admissions.
	for i := 0; i < 5; i++ {
		seedExtraPending(t, st, ctx, rootA,
			fmt.Sprintf("b0000000-0000-0000-0000-000000000a%02d", i+10),
			fmt.Sprintf("a%d.txt", i), fmt.Sprintf("ha%d", i))
	}
	seedPendingRoot(t, st, ctx, rootB, "b0000000-0000-0000-0000-000000000b01", "b.txt", "hb")

	w := worker.NewWithIntervals(st, 1, 20*time.Millisecond, 20*time.Millisecond,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = w.Run(wctx) }()

	// B must make progress despite A having more pending work and maxConcurrent=1.
	waitRootIdle(t, st, ctx, rootB, 15*time.Second)

	var admissions int
	_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, rootA).Scan(&admissions)
	if admissions != 6 {
		t.Fatalf("root A must have exactly 6 admissions (no duplicates), got %d", admissions)
	}
}

func seedExtraPending(t *testing.T, st *postgres.Store, ctx context.Context, rootID, snapID, name, hash string) {
	t.Helper()
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	if err := st.InsertSnapshotStub(ctx, st.Pool(), domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft, EntryCount: pInt64(1),
	}); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatalf("submit: %v", err)
	}
	alg, scope := "sha256", "root"
	if err := st.InsertSnapshotEntry(ctx, st.Pool(), domain.SnapshotEntry{
		SnapshotID: snapID, EntryLocalID: name, Name: name, ParentRef: "/",
		Size: pInt64(1), Mtime: pTime(time.Now().UTC()), ContentHash: &hash, HashAlgorithm: &alg,
		ProviderObjectID: pStr("P-" + name), ProviderObjectIDScope: &scope,
	}); err != nil {
		t.Fatalf("entry: %v", err)
	}
	if _, _, err := st.AdmitOrResumeSnapshot(ctx, rootID, snapID); err != nil {
		t.Fatalf("admit: %v", err)
	}
}

func waitRootIdle(t *testing.T, st *postgres.Store, ctx context.Context, rootID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var pending int
		_ = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid AND status='PENDING'`, rootID).Scan(&pending)
		if pending == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("root %s still has %d pending admissions after %s", rootID, pending, timeout)
		}
		time.Sleep(30 * time.Millisecond)
	}
}
