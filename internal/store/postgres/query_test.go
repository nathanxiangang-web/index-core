package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func newQueryReader(st *postgres.Store) query.Reader {
	// Consumers receive only the query.Reader interface, never *postgres.Store (B5).
	return postgres.NewQueryReader(st.Pool())
}

func requirePresent(t *testing.T, st *postgres.Store, ctx context.Context, path string) domain.CanonicalResource {
	t.Helper()
	rows, err := st.PresentResourcesAtPath(ctx, st.Pool(), pipeRoot, path)
	if err != nil {
		t.Fatalf("present at path %s: %v", path, err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one PRESENT at %s, got %d", path, len(rows))
	}
	return rows[0]
}

func qEntry(name, hash string, size int64, mt time.Time) domain.SnapshotEntry {
	alg := "sha256"
	return domain.SnapshotEntry{EntryLocalID: name, Name: name, ParentRef: "/",
		Size: &size, Mtime: &mt, ContentHash: &hash, HashAlgorithm: &alg}
}

func TestQueryRootVisibilityDefaults(t *testing.T) {
	st, ctx := newStore(t)
	cases := map[string]domain.RootLifecycleState{
		"aaaa0000-0000-0000-0000-0000000000a1": domain.RootNew,
		"aaaa0000-0000-0000-0000-0000000000a2": domain.RootActive,
		"aaaa0000-0000-0000-0000-0000000000a3": domain.RootDeprecated,
		"aaaa0000-0000-0000-0000-0000000000a4": domain.RootDeleted,
	}
	for id, lc := range cases {
		if err := st.CreateRoot(ctx, st.Pool(), id, []byte(`{}`), lc); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	qr := newQueryReader(st)
	def, err := qr.ListRoots(ctx, false, false)
	if err != nil {
		t.Fatalf("list roots: %v", err)
	}
	if len(def) != 2 {
		t.Fatalf("default visibility must be NEW+ACTIVE only, got %d", len(def))
	}
	all, _ := qr.ListRoots(ctx, true, true)
	if len(all) != 4 {
		t.Fatalf("explicit include must return all 4, got %d", len(all))
	}
}

func TestQueryRemovedHiddenByDefault(t *testing.T) {
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
	insertSubmittedSnapshot(t, st, ctx, "d1000000-0000-0000-0000-000000000001", v1)
	processSnapshot(t, st, ctx, "d1000000-0000-0000-0000-000000000001", v1, cfg)
	a := requirePresent(t, st, ctx, "/a.txt")
	insertSubmittedSnapshot(t, st, ctx, "d1000000-0000-0000-0000-000000000002", keeper)
	processSnapshot(t, st, ctx, "d1000000-0000-0000-0000-000000000002", keeper, cfg)
	keeper3 := append([]domain.SnapshotEntry{}, keeper...)
	keeper3[0] = entryWithProviderID("k1.txt", "/", "PK-k1.txt", "hk-k1-v2", 99, mt.Add(time.Hour))
	insertSubmittedSnapshot(t, st, ctx, "d1000000-0000-0000-0000-000000000003", keeper3)
	processSnapshot(t, st, ctx, "d1000000-0000-0000-0000-000000000003", keeper3, cfg)

	qr := newQueryReader(st)
	def, err := qr.GetResource(ctx, a.ResourceID, false)
	if err != nil {
		t.Fatalf("get resource: %v", err)
	}
	if def != nil {
		t.Fatalf("REMOVED tombstone must be hidden by default, got %+v", def)
	}
	withRemoved, err := qr.GetResource(ctx, a.ResourceID, true)
	if err != nil {
		t.Fatalf("get resource includeRemoved: %v", err)
	}
	if withRemoved == nil || withRemoved.ResourcePresence != domain.ResourceRemoved {
		t.Fatalf("explicit history access must return the REMOVED tombstone, got %+v", withRemoved)
	}
}

func TestQueryResolvePathAmbiguity(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	path := "/x.txt"
	mk := func(id string) domain.CanonicalResource {
		return domain.CanonicalResource{
			ResourceID: id, RootID: pipeRoot, ResourcePresence: domain.ResourcePresent,
			RemovalEvidenceState: domain.RemovalEvidenceNone, CanonicalPath: &path, CurrentAttributes: []byte(`{}`),
		}
	}
	if err := st.InsertCanonicalResource(ctx, st.Pool(), mk("bbbb0000-0000-0000-0000-0000000000b1")); err != nil {
		t.Fatalf("insert 1: %v", err)
	}
	if err := st.InsertCanonicalResource(ctx, st.Pool(), mk("bbbb0000-0000-0000-0000-0000000000b2")); err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	qr := newQueryReader(st)
	res, err := qr.ResolvePath(ctx, pipeRoot, path, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Matches) != 2 || !res.Ambiguous {
		t.Fatalf("overlap must return both matches with ambiguous=true, got %d ambiguous=%v", len(res.Matches), res.Ambiguous)
	}
}

func TestQueryPaginationStaleCursor(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	mt := time.Now().UTC()
	qr := newQueryReader(st)

	v1 := []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt), qEntry("b.txt", "hb", 2, mt)}
	insertSubmittedSnapshot(t, st, ctx, "d2000000-0000-0000-0000-000000000001", v1)
	processSnapshot(t, st, ctx, "d2000000-0000-0000-0000-000000000001", v1, cfg)

	page1, err := qr.ListActivePage(ctx, pipeRoot, nil, 1)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Items) != 1 || page1.Next == nil {
		t.Fatalf("page1 must return 1 item + next cursor, got %+v", page1)
	}

	v2 := []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt), qEntry("b.txt", "hb", 2, mt), qEntry("c.txt", "hc", 3, mt)}
	insertSubmittedSnapshot(t, st, ctx, "d2000000-0000-0000-0000-000000000002", v2)
	processSnapshot(t, st, ctx, "d2000000-0000-0000-0000-000000000002", v2, cfg)

	if _, err := qr.ListActivePage(ctx, pipeRoot, page1.Next, 1); err != query.ErrStaleCursor {
		t.Fatalf("cursor from a stale generation must return STALE_CURSOR, got %v", err)
	}
}

func TestQueryReadJournalByCursor(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	mt := time.Now().UTC()
	qr := newQueryReader(st)

	v1 := []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}
	insertSubmittedSnapshot(t, st, ctx, "d3000000-0000-0000-0000-000000000001", v1)
	processSnapshot(t, st, ctx, "d3000000-0000-0000-0000-000000000001", v1, cfg)
	v2 := []domain.SnapshotEntry{qEntry("b.txt", "hb", 2, mt)}
	insertSubmittedSnapshot(t, st, ctx, "d3000000-0000-0000-0000-000000000002", v2)
	processSnapshot(t, st, ctx, "d3000000-0000-0000-0000-000000000002", v2, cfg)

	all, err := qr.ReadJournal(ctx, pipeRoot, 0, 10)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("expected >= 2 events, got %d", len(all))
	}
	tail, err := qr.ReadJournal(ctx, pipeRoot, all[len(all)-1].EventSeq, 10)
	if err != nil {
		t.Fatalf("read journal tail: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("cursor at tail must return no more events, got %d", len(tail))
	}
}
