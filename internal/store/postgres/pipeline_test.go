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

// seedPipelineRoot creates the fixture root.
func seedPipelineRoot(t *testing.T, st *postgres.Store, ctx context.Context) {
	t.Helper()
	if err := st.CreateRoot(ctx, st.Pool(), pipeRoot, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
}

// insertSubmittedSnapshot inserts a SUBMITTED, COMPLETE, strongly-assured
// snapshot with the given entries and returns the entries with snapshot ids set.
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

// processSnapshot runs the full frozen pipeline end to end: Kernel evaluation
// (acceptance + final DETERMINISTIC_DIGEST), admission, Stage-2 reconcile. No
// identity or acceptance verdict is injected (B4), and IdentityEvidence is
// appended automatically inside the transaction (B3).
func processSnapshot(t *testing.T, st *postgres.Store, ctx context.Context, snapID string,
	entries []domain.SnapshotEntry, cfg reconcile.Config) (postgres.ReconcileOutcome, domain.SnapshotIdentity) {
	t.Helper()
	eval, err := st.EvaluateSnapshot(ctx, snapID, nil)
	if err != nil {
		t.Fatalf("evaluate snapshot %s: %v", snapID, err)
	}
	seq, err := st.AllocateAdmission(ctx, st.Pool(), pipeRoot, snapID)
	if err != nil {
		t.Fatalf("allocate admission: %v", err)
	}
	out, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: pipeRoot, AdmissionSeq: seq, SnapshotID: snapID, Identity: eval.Identity,
	}, func(prior []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		res := reconcile.Reconcile(prior, entries, eval.Acceptance, cfg, time.Now().UTC(), snapID)
		return st.PlanFromResult(pipeRoot, snapID, res), nil
	})
	if err != nil {
		t.Fatalf("reconcile %s: %v", snapID, err)
	}
	return out, eval.Identity
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

// TestPipelineLearnsProviderContinuity shows provider-id continuity learned from
// snapshot 1 is used automatically by snapshot 2, with NO manual DB seeding (B3).
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
	out1, id1 := processSnapshot(t, st, ctx, "c1000000-0000-0000-0000-000000000001", v1, cfg)
	if out1.Status != domain.AdmissionApplied || out1.AppliedGeneration != 1 {
		t.Fatalf("snapshot 1 must APPLY at generation 1, got %+v", out1)
	}
	if n := countIdentityEvidence(t, st, ctx, pipeRoot); n != 2 {
		t.Fatalf("snapshot 1 must append 2 IdentityEvidence observations automatically, got %d", n)
	}

	// Snapshot 2: a.txt content changed but provider id is stable (learned from S1).
	mt2 := mt.Add(time.Hour)
	v2 := []domain.SnapshotEntry{
		entryWithProviderID("a.txt", "/", "P1", "ha2", 9, mt2),
		entryWithProviderID("b.txt", "/", "P2", "hb", 2, mt),
	}
	insertSubmittedSnapshot(t, st, ctx, "c1000000-0000-0000-0000-000000000002", v2)
	out2, id2 := processSnapshot(t, st, ctx, "c1000000-0000-0000-0000-000000000002", v2, cfg)
	if out2.Status != domain.AdmissionApplied {
		t.Fatalf("snapshot 2 must APPLY, got %+v", out2)
	}
	if id1.Equal(id2) {
		t.Fatal("changed content must yield a different final IO3 identity")
	}
	// a.txt must have been matched by provider continuity (no extra resource added).
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

// TestPipelineRemovalIndependenceEndToEnd verifies removal confirmation requires
// a later independent admitted snapshot (B2).
func TestPipelineRemovalIndependenceEndToEnd(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1, RemovalGracePeriod: 0}
	mt := time.Now().UTC()

	// Keepers keep later snapshots non-empty AND keep the drop non-significant
	// (an empty scope triggers C-8, and a >=50% drop triggers C-7 SUSPICIOUS).
	v1 := []domain.SnapshotEntry{entryWithProviderID("a.txt", "/", "P1", "ha", 1, mt)}
	keeper := []domain.SnapshotEntry{}
	for i, name := range []string{"k1.txt", "k2.txt", "k3.txt", "k4.txt", "k5.txt"} {
		e := entryWithProviderID(name, "/", "PK-"+name, "hk-"+name, int64(i+10), mt)
		v1 = append(v1, e)
		keeper = append(keeper, e)
	}
	insertSubmittedSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000001", v1)
	processSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000001", v1, cfg)

	// Snapshot 2 omits a.txt: first MISSING only, resource must still be PRESENT.
	insertSubmittedSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000002", keeper)
	processSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000002", keeper, cfg)
	if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, "/a.txt"); len(rows) != 1 {
		t.Fatalf("first MISSING must NOT remove the resource, got %d PRESENT", len(rows))
	}

	// Snapshot 3 is a later, independent snapshot. It must differ from snapshot 2
	// in evaluated content, otherwise an identical identity at the same generation
	// is an IO3 NOOP and would not reprocess the missing resource.
	keeper3 := append([]domain.SnapshotEntry{}, keeper...)
	keeper3[0] = entryWithProviderID("k1.txt", "/", "PK-k1.txt", "hk-k1-v2", 99, mt.Add(time.Hour))
	insertSubmittedSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000003", keeper3)
	processSnapshot(t, st, ctx, "c2000000-0000-0000-0000-000000000003", keeper3, cfg)
	if rows, _ := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, "/a.txt"); len(rows) != 0 {
		t.Fatalf("independent confirmation must remove the resource, got %d PRESENT", len(rows))
	}
}
