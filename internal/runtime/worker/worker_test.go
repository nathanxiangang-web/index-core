package worker_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/worker"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func newWorkerStore(t *testing.T) (*postgres.Store, context.Context) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return postgres.New(pool), context.Background()
}

func seedPendingRoot(t *testing.T, st *postgres.Store, ctx context.Context, rootID, snapID, name, hash string) int64 {
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
	alg := "sha256"
	scope := "root"
	if err := st.InsertSnapshotEntry(ctx, st.Pool(), domain.SnapshotEntry{
		SnapshotID: snapID, EntryLocalID: name, Name: name, ParentRef: "/",
		Size: pInt64(1), Mtime: pTime(time.Now().UTC()), ContentHash: &hash, HashAlgorithm: &alg,
		ProviderObjectID: pStr("P-" + name), ProviderObjectIDScope: &scope,
	}); err != nil {
		t.Fatalf("entry: %v", err)
	}
	// Stage-1 only: leaves a durable PENDING head (simulating a pre-restart state).
	seq, _, err := st.AdmitOrResumeSnapshot(ctx, rootID, snapID)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return seq
}

func runWorker(t *testing.T, st *postgres.Store, ctx context.Context) context.CancelFunc {
	t.Helper()
	w := worker.NewWithIntervals(st, 4, 50*time.Millisecond, 50*time.Millisecond,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	wctx, cancel := context.WithCancel(ctx)
	go func() { _ = w.Run(wctx) }()
	return cancel
}

func waitApplied(t *testing.T, st *postgres.Store, ctx context.Context, rootID string, seq int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		adm, err := st.GetAdmission(ctx, st.Pool(), rootID, seq)
		if err == nil && adm.Status == domain.AdmissionApplied {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending head %d not processed in time (last=%+v err=%v)", seq, adm, err)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// A durable PENDING head created before a restart is resumed by the worker with
// the SAME admission_seq (no renumbering).
func TestWorkerResumesPendingHeadAcrossRestart(t *testing.T) {
	st, ctx := newWorkerStore(t)
	const rootID = "b0000000-0000-0000-0000-000000000001"
	const snapID = "b0000000-0000-0000-0000-0000000000a1"
	seq := seedPendingRoot(t, st, ctx, rootID, snapID, "a.txt", "ha")

	cancel := runWorker(t, st, ctx)
	defer cancel()
	waitApplied(t, st, ctx, rootID, seq)

	r, err := st.GetRoot(ctx, st.Pool(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	if r.CurrentGeneration != 1 {
		t.Fatalf("ADD reconcile must advance to generation 1, got %d", r.CurrentGeneration)
	}
	var admissions int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_admission WHERE root_id=$1::uuid`, rootID).Scan(&admissions); err != nil {
		t.Fatal(err)
	}
	if admissions != 1 {
		t.Fatalf("restart recovery must not allocate extra admissions, got %d", admissions)
	}
}

// Different roots make concurrent progress within the bounded worker.
func TestWorkerProcessesDifferentRoots(t *testing.T) {
	st, ctx := newWorkerStore(t)
	const rootA = "b0000000-0000-0000-0000-000000000002"
	const rootB = "b0000000-0000-0000-0000-000000000003"
	seqA := seedPendingRoot(t, st, ctx, rootA, "b0000000-0000-0000-0000-0000000000a2", "a.txt", "ha")
	seqB := seedPendingRoot(t, st, ctx, rootB, "b0000000-0000-0000-0000-0000000000a3", "b.txt", "hb")

	cancel := runWorker(t, st, ctx)
	defer cancel()
	waitApplied(t, st, ctx, rootA, seqA)
	waitApplied(t, st, ctx, rootB, seqB)
}

func pInt64(v int64) *int64        { return &v }
func pTime(v time.Time) *time.Time { return &v }
func pStr(s string) *string        { return &s }
