package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

const pipeRoot = "cccccccc-0000-0000-0000-000000000001"

func seedPipelineRoot(t *testing.T, st *postgres.Store, ctx context.Context) {
	t.Helper()
	if err := st.CreateRoot(ctx, st.Pool(), pipeRoot, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
}

func insertSubmittedSnapshot(t *testing.T, st *postgres.Store, ctx context.Context, snapID string, entries []domain.SnapshotEntry) []domain.SnapshotEntry {
	t.Helper()
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: pipeRoot, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft,
		EntryCount: int64p(int64(len(entries))),
	}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), snap); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatalf("submit snapshot: %v", err)
	}
	for i := range entries {
		entries[i].SnapshotID = snapID
		if err := st.InsertSnapshotEntry(ctx, st.Pool(), entries[i]); err != nil {
			t.Fatalf("insert entry: %v", err)
		}
	}
	return entries
}

func int64p(v int64) *int64 { return &v }

func processSnapshot(t *testing.T, st *postgres.Store, ctx context.Context, snapID string, cfg reconcile.Config) postgres.ReconcileOutcome {
	t.Helper()
	out, err := postgres.NewCoordinator(st, cfg).ProcessSnapshot(ctx, pipeRoot, snapID)
	if err != nil {
		t.Fatalf("process snapshot %s: %v", snapID, err)
	}
	return out
}

func snapshotAcceptance(t *testing.T, st *postgres.Store, ctx context.Context, snapID string) domain.AcceptanceState {
	t.Helper()
	var a string
	if err := st.Pool().QueryRow(ctx, `SELECT acceptance_state FROM index_snapshot WHERE snapshot_id=$1::uuid`, snapID).Scan(&a); err != nil {
		t.Fatalf("acceptance: %v", err)
	}
	return domain.AcceptanceState(a)
}

func snapshotCorroboration(t *testing.T, st *postgres.Store, ctx context.Context, snapID string) domain.ScopeShrinkCorroboration {
	t.Helper()
	var c string
	if err := st.Pool().QueryRow(ctx, `SELECT scope_shrink_corroboration FROM index_snapshot WHERE snapshot_id=$1::uuid`, snapID).Scan(&c); err != nil {
		t.Fatalf("corroboration: %v", err)
	}
	return domain.ScopeShrinkCorroboration(c)
}

func appliedIdentity(t *testing.T, st *postgres.Store, ctx context.Context, snapID string) string {
	t.Helper()
	var v string
	if err := st.Pool().QueryRow(ctx,
		`SELECT snapshot_identity_value FROM index_applied_snapshot WHERE root_id=$1::uuid AND snapshot_id=$2::uuid
		  ORDER BY applied_generation DESC LIMIT 1`, pipeRoot, snapID).Scan(&v); err != nil {
		t.Fatalf("applied identity: %v", err)
	}
	return v
}

func countIdentityEvidence(t *testing.T, st *postgres.Store, ctx context.Context, rootID string) int {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM index_identity_evidence_observation o
		   JOIN index_canonical_resource c ON c.resource_id = o.resource_id
		  WHERE c.root_id = $1::uuid`, rootID).Scan(&n); err != nil {
		t.Fatalf("count identity evidence: %v", err)
	}
	return n
}

func entryWithProviderID(name, pathParent, provID, hash string, size int64, mt time.Time) domain.SnapshotEntry {
	alg := "sha256"
	scope := "root"
	assurance := domain.IdentityStableWithinScope
	return domain.SnapshotEntry{
		EntryLocalID: name, Name: name, ParentRef: pathParent,
		Size: &size, Mtime: &mt, ContentHash: &hash, HashAlgorithm: &alg,
		ProviderObjectID: &provID, ProviderObjectIDScope: &scope, ProviderIdentityAssurance: &assurance,
	}
}

func TestPipelineLearnsProviderContinuity(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	mt := time.Now().UTC()

	v1 := []domain.SnapshotEntry{
		entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt),
		entryWithProviderID("b.txt", "/", "P2", "hb", 2, mt),
	}
	insertSubmittedSnapshot(t, st, ctx, "c1000000-0000-0000-0000-000000000001", v1)
	out1 := processSnapshot(t, st, ctx, "c1000000-0000-0000-0000-000000000001", cfg)
	if out1.Status != domain.AdmissionApplied || out1.AppliedGeneration != 1 {
		t.Fatalf("snapshot 1 must APPLY at generation 1, got %+v", out1)
	}
	if n := countIdentityEvidence(t, st, ctx, pipeRoot); n != 2 {
		t.Fatalf("snapshot 1 must append 2 IdentityEvidence observations automatically, got %d", n)
	}

	mt2 := mt.Add(time.Hour)
	v2 := []domain.SnapshotEntry{
		entryWithProviderID("a.txt", "/", "P1", "ha2", 9, mt2),
		entryWithProviderID("b.txt", "/", "P2", "hb", 2, mt),
	}
	insertSubmittedSnapshot(t, st, ctx, "c1000000-0000-0000-0000-000000000002", v2)
	out2 := processSnapshot(t, st, ctx, "c1000000-0000-0000-0000-000000000002", cfg)
	if out2.Status != domain.AdmissionApplied {
		t.Fatalf("snapshot 2 must APPLY, got %+v", out2)
	}
	if appliedIdentity(t, st, ctx, "c1000000-0000-0000-0000-000000000001") == appliedIdentity(t, st, ctx, "c1000000-0000-0000-0000-000000000002") {
		t.Fatal("changed content must yield a different final IO3 identity")
	}
	present, err := st.ListCanonicalPresent(ctx, st.Pool(), pipeRoot)
	if err != nil {
		t.Fatalf("list present: %v", err)
	}
	if len(present) != 2 {
		t.Fatalf("provider continuity must UPDATE in place (still 2 resources), got %d", len(present))
	}
	if n := countIdentityEvidence(t, st, ctx, pipeRoot); n != 4 {
		t.Fatalf("snapshot 2 must append 2 more observations (4 total), got %d", n)
	}
}

func TestPipelineRemovalIndependenceEndToEnd(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1, RemovalGracePeriod: 0}
	mt := time.Now().UTC()

	v1 := []domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}
	keeper := []domain.SnapshotEntry{}
	for i, name := range []string{"k1.txt", "k2.txt", "k3.txt", "k4.txt", "k5.txt"} {
		e := entryWithProviderID(name, "/", "PK-"+name, "hk-"+name, int64(i+10), mt)
		v1 = append(v1, e)
		keeper = append(keeper, e)
	}
	insertSubmittedSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000001", v1)
	processSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000001", cfg)

	insertSubmittedSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000002", keeper)
	processSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000002", cfg)
	if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, "/a.txt"); len(rows) != 1 {
		t.Fatalf("first MISSING must NOT remove the resource, got %d PRESENT", len(rows))
	}

	insertSubmittedSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000003", keeper)
	processSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000003", cfg)
	if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, "/a.txt"); len(rows) != 0 {
		t.Fatalf("independent confirmation must remove the resource, got %d PRESENT", len(rows))
	}
}
