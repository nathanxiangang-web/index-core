package scale_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"

	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Gate 3 P8 scale harness. Opt-in so the normal suite stays fast:
//
//	INDEXCORE_SCALE_TEST=1 INDEXCORE_SCALE_N=20000 go test ./internal/runtime/scale -v
func TestScalePopulationAndDelta(t *testing.T) {
	if os.Getenv("INDEXCORE_SCALE_TEST") != "1" {
		t.Skip("set INDEXCORE_SCALE_TEST=1 to run the scale harness")
	}
	n := 20000
	if v := os.Getenv("INDEXCORE_SCALE_N"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}

	pool := testutil.Pool(t)
	ctx := context.Background()
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)

	const rootID = "a1000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	cfg, err := st.RootReconcileConfig(ctx, rootID)
	if err != nil {
		t.Fatalf("cfg: %v", err)
	}
	coord := postgres.NewCoordinator(st, cfg)
	base := time.Now().UTC().Truncate(time.Second)

	// --- initial population ---
	entries := synthEntries(n, base)
	snap1 := "a1000000-0000-0000-0000-0000000000b1"
	persist(t, st, ctx, rootID, snap1, entries)
	t0 := time.Now()
	out1 := process(t, coord, ctx, rootID, snap1)
	dPopulation := time.Since(t0)
	if out1.Status != domain.AdmissionApplied {
		t.Fatalf("initial population must APPLY, got %s", out1.Status)
	}

	// --- identical repeat (idempotent, no mutation) ---
	snap2 := "a1000000-0000-0000-0000-0000000000b2"
	persist(t, st, ctx, rootID, snap2, entries)
	t0 = time.Now()
	out2 := process(t, coord, ctx, rootID, snap2)
	dRepeat := time.Since(t0)
	if out2.Status != domain.AdmissionNoop {
		t.Fatalf("identical repeat must be NOOP, got %s", out2.Status)
	}

	// --- small delta: one in-place change + one addition ---
	delta := make([]domain.SnapshotEntry, len(entries))
	copy(delta, entries)
	alg, scope, stable := "sha256", "root", domain.IdentityStableWithinScope
	changedHash, changedSize := "h-CHANGED", int64(4242)
	delta[0].ContentHash = &changedHash
	delta[0].Size = &changedSize
	newName, newHash, newID := fmt.Sprintf("f%06d.txt", n), "h-NEW", "P-NEW"
	newSize := int64(7)
	newMtime := base.Add(time.Duration(n+1) * time.Second)
	delta = append(delta, domain.SnapshotEntry{
		EntryLocalID: newName, Name: newName, ParentRef: "/", Size: &newSize, Mtime: &newMtime,
		ContentHash: &newHash, HashAlgorithm: &alg, ProviderObjectID: &newID,
		ProviderObjectIDScope: &scope, ProviderIdentityAssurance: &stable,
	})
	snap3 := "a1000000-0000-0000-0000-0000000000b3"
	persist(t, st, ctx, rootID, snap3, delta)
	t0 = time.Now()
	out3 := process(t, coord, ctx, rootID, snap3)
	dDelta := time.Since(t0)
	if out3.Status != domain.AdmissionApplied {
		t.Fatalf("delta must APPLY, got %s", out3.Status)
	}

	// --- query pagination over the whole root ---
	qr := postgres.NewQueryReader(pool)
	t0 = time.Now()
	paged := 0
	var cur *query.Cursor
	for {
		page, err := qr.ListActivePage(ctx, rootID, cur, 1000)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		paged += len(page.Items)
		if page.Next == nil {
			break
		}
		cur = page.Next
	}
	dPaging := time.Since(t0)

	// --- DB metrics ---
	var canonical, journal, snapshots int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE root_id=$1::uuid AND resource_presence='PRESENT'`, rootID).Scan(&canonical)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM index_journal_event WHERE root_id=$1::uuid`, rootID).Scan(&journal)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM index_snapshot WHERE root_id=$1::uuid`, rootID).Scan(&snapshots)
	var dbSize int64
	_ = pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&dbSize)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	t.Logf("SCALE REPORT (N=%d)", n)
	t.Logf("initial population: %s", dPopulation)
	t.Logf("identical repeat  : %s (status=%s)", dRepeat, out2.Status)
	t.Logf("small delta       : %s (status=%s)", dDelta, out3.Status)
	t.Logf("query pagination  : %s (rows=%d)", dPaging, paged)
	t.Logf("canonical PRESENT=%d journal_events=%d snapshots=%d", canonical, journal, snapshots)
	t.Logf("db_size=%.1f MiB heap_alloc=%.1f MiB", float64(dbSize)/1048576, float64(ms.HeapAlloc)/1048576)

	if canonical != n+1 {
		t.Fatalf("expected %d canonical resources, got %d", n+1, canonical)
	}
	if paged != n+1 {
		t.Fatalf("pagination must return every resource: got %d want %d", paged, n+1)
	}
}

func persist(t *testing.T, st *postgres.Store, ctx context.Context, rootID, snapID string, entries []domain.SnapshotEntry) {
	t.Helper()
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	count := int64(len(entries))
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft, EntryCount: &count,
	}
	if err := st.CreateSubmittedSnapshot(ctx, snap, entries); err != nil {
		t.Fatalf("persist snapshot %s: %v", snapID, err)
	}
}

func process(t *testing.T, coord *postgres.Coordinator, ctx context.Context, rootID, snapID string) postgres.ReconcileOutcome {
	t.Helper()
	out, err := coord.ProcessSnapshot(ctx, rootID, snapID)
	if err != nil {
		t.Fatalf("process %s: %v", snapID, err)
	}
	return out
}

func synthEntries(n int, base time.Time) []domain.SnapshotEntry {
	alg, scope, stable := "sha256", "root", domain.IdentityStableWithinScope
	out := make([]domain.SnapshotEntry, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("f%06d.txt", i)
		hash := fmt.Sprintf("h%06d", i)
		size := int64(i % 1000)
		mt := base.Add(time.Duration(i) * time.Second)
		id := fmt.Sprintf("P%06d", i)
		out = append(out, domain.SnapshotEntry{
			EntryLocalID: name, Name: name, ParentRef: "/", Size: &size, Mtime: &mt,
			ContentHash: &hash, HashAlgorithm: &alg, ProviderObjectID: &id,
			ProviderObjectIDScope: &scope, ProviderIdentityAssurance: &stable,
		})
	}
	return out
}
