package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// TestGate4ReferenceConsumerFixture seeds the controlled COMPLETE snapshots that
// the Gate 4 Reference Consumer E2E needs for Q7 (removed resources) and Q5
// (path ambiguity).
//
// Why this lives in index-core (not the consumer repository): rclone is
// additive-only by frozen design (skipped_scopes UNKNOWN => never COMPLETE), so
// removed/ambiguity state cannot be produced by a normal scan. The frozen Gate
// 1C contract explicitly permits controlled COMPLETE Snapshot fixtures for
// COMPLETE/removal Kernel correctness, and the consumer repository must stay
// free of Go, PostgreSQL, and canonical-mutation coupling. Verification fixtures
// therefore belong to index-core's test/verification side.
//
// This fixture uses ONLY the final accepted Gate 3 safe ingress:
//
//	CreateDraftSnapshot -> SubmitAndAdmitSnapshot -> Coordinator.ProcessHead
//
// It never uses the retired test-only path (InsertSnapshotStub /
// MarkSnapshotSubmitted / ProcessSnapshot).
//
// Gated by INDEXCORE_GATE4_FIXTURE=1 so `go test ./...` never touches a live DB.
// Required env when enabled:
//
//	INDEXCORE_GATE4_FIXTURE=1
//	E2E_ROOT_B, E2E_ROOT_C    target root UUIDs (created ACTIVE if absent)
//	INDEXCORE_DATABASE_URL    live IndexCore DSN (falls back to INDEXCORE_TEST_DATABASE_URL)
func TestGate4ReferenceConsumerFixture(t *testing.T) {
	if os.Getenv("INDEXCORE_GATE4_FIXTURE") != "1" {
		t.Skip("set INDEXCORE_GATE4_FIXTURE=1 to seed the Gate 4 controlled fixtures")
	}
	dsn := os.Getenv("INDEXCORE_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("INDEXCORE_TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Fatal("INDEXCORE_DATABASE_URL (or INDEXCORE_TEST_DATABASE_URL) is required")
	}
	rootB := os.Getenv("E2E_ROOT_B")
	rootC := os.Getenv("E2E_ROOT_C")
	if rootB == "" || rootC == "" {
		t.Fatal("E2E_ROOT_B and E2E_ROOT_C are required")
	}

	ctx := context.Background()
	pool, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer pool.Close()
	st := postgres.New(pool)

	// Removal fixture: grace 0 + one required consecutive COMPLETE missing, so a
	// single later independent COMPLETE observation confirms removal (the first
	// MISSING observation is evidence only and can never self-confirm).
	gate4EnsureRoot(ctx, t, st, rootB, postgres.RootPolicy{
		RemovalGracePeriod:            0,
		MoveRecognitionHorizon:        0,
		MinConsecutiveCompleteMissing: 1,
		MinIndependentConfirmations:   1,
	})
	// Ambiguity fixture: a long grace period and a high consecutive-missing bar
	// keep the old row PRESENT (with missing evidence) while a different-content
	// resource takes the same canonical path (R8 imposter).
	gate4EnsureRoot(ctx, t, st, rootC, postgres.RootPolicy{
		RemovalGracePeriod:            time.Hour,
		MoveRecognitionHorizon:        time.Hour,
		MinConsecutiveCompleteMissing: 5,
		MinIndependentConfirmations:   1,
	})

	gate4SeedRemoval(ctx, t, st, rootB)
	gate4SeedAmbiguity(ctx, t, st, rootC)
}

// gate4EnsureRoot creates the root ACTIVE if absent and (re)applies the policy.
func gate4EnsureRoot(ctx context.Context, t *testing.T, st *postgres.Store, rootID string, policy postgres.RootPolicy) {
	t.Helper()
	if _, err := st.GetRoot(ctx, st.Pool(), rootID); err != nil {
		if !errors.Is(err, postgres.ErrNotFound) {
			t.Fatalf("gate4 get root %s: %v", rootID, err)
		}
		if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
			t.Fatalf("gate4 create root %s: %v", rootID, err)
		}
	}
	if err := st.UpsertRootPolicy(ctx, rootID, policy); err != nil {
		t.Fatalf("gate4 policy %s: %v", rootID, err)
	}
}

// gate4SeedRemoval: 4 entries -> 3 entries (first MISSING) -> 3 entries
// (independent confirmation => CONFIRMED_REMOVED). The 1-of-4 drop stays below
// the 0.5 significant-shrink threshold, so every snapshot is COMPLETE and may
// advance removal evidence.
func gate4SeedRemoval(ctx context.Context, t *testing.T, st *postgres.Store, rootID string) {
	t.Helper()
	keepers := []domain.SnapshotEntry{
		gate4Entry("keep1.txt", "keep-one"),
		gate4Entry("keep2.txt", "keep-two"),
		gate4Entry("keep3.txt", "keep-three"),
	}
	target := gate4Entry("removal-target.txt", "removal-target")

	gate4Process(ctx, t, st, rootID, append([]domain.SnapshotEntry{target}, keepers...))
	gate4Process(ctx, t, st, rootID, keepers)
	gate4Process(ctx, t, st, rootID, keepers)
}

// gate4SeedAmbiguity: add amb.txt -> it goes MISSING but stays PRESENT inside the
// grace period -> a DIFFERENT-content resource appears at the same path (R8
// imposter => new resource), leaving two PRESENT canonical rows at /amb.txt so
// resolve_path is ambiguous.
func gate4SeedAmbiguity(ctx context.Context, t *testing.T, st *postgres.Store, rootID string) {
	t.Helper()
	keepers := []domain.SnapshotEntry{
		gate4Entry("ck1.txt", "c-keep-one"),
		gate4Entry("ck2.txt", "c-keep-two"),
		gate4Entry("ck3.txt", "c-keep-three"),
	}
	ambV1 := gate4Entry("amb.txt", "amb-version-one")
	ambV2 := gate4Entry("amb.txt", "amb-version-two-different")

	gate4Process(ctx, t, st, rootID, append([]domain.SnapshotEntry{ambV1}, keepers...))
	gate4Process(ctx, t, st, rootID, keepers)
	gate4Process(ctx, t, st, rootID, append([]domain.SnapshotEntry{ambV2}, keepers...))
}

// gate4Process runs one controlled COMPLETE snapshot through the accepted safe
// ingress and drains the resulting PENDING head.
func gate4Process(ctx context.Context, t *testing.T, st *postgres.Store, rootID string, entries []domain.SnapshotEntry) {
	t.Helper()
	snapID := reconcile.NewUUID()
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	count := int64(len(entries))
	snap := domain.Snapshot{
		SnapshotID:                     snapID,
		RootID:                         rootID,
		Provenance:                     []byte(`{"fixture":"gate4-reference-consumer"}`),
		ObservedAt:                     time.Now().UTC(),
		TraversalStatus:                domain.TraversalSuccess,
		SkippedScopesKnownEmpty:        true,
		FreshnessEvidence:              &fresh,
		CollectorCompletenessAssurance: &strong,
		CompletenessFlag:               domain.CompletenessFlagComplete,
		LifecycleState:                 domain.SnapshotDraft,
		EntryCount:                     &count,
	}

	if err := st.CreateDraftSnapshot(ctx, snap, entries); err != nil {
		t.Fatalf("gate4 create draft snapshot: %v", err)
	}
	if _, err := st.SubmitAndAdmitSnapshot(ctx, rootID, snapID); err != nil {
		t.Fatalf("gate4 submit+admit snapshot: %v", err)
	}
	cfg, err := st.RootReconcileConfig(ctx, rootID)
	if err != nil {
		t.Fatalf("gate4 reconcile config %s: %v", rootID, err)
	}
	out, done, err := postgres.NewCoordinator(st, cfg).ProcessHead(ctx, rootID)
	if err != nil {
		t.Fatalf("gate4 process head %s: %v", rootID, err)
	}
	if !done {
		t.Fatalf("gate4 process head %s: no pending admission after submit", rootID)
	}
	t.Logf("gate4 root=%s snapshot=%s status=%s lifecycle=%s generation=%d applied=%d",
		rootID, snapID, out.Status, out.SnapshotLifecycle, out.Generation, out.AppliedGeneration)
}

func gate4Entry(name, content string) domain.SnapshotEntry {
	alg := "sha256"
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	size := int64(len(content))
	mtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return domain.SnapshotEntry{
		EntryLocalID:  name,
		Name:          name,
		ParentRef:     "/",
		Size:          &size,
		Mtime:         &mtime,
		ContentHash:   &hash,
		HashAlgorithm: &alg,
	}
}
