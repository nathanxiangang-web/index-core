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
	def, err := st.QueryListRoots(ctx, false, false)
	if err != nil {
		t.Fatalf("list roots: %v", err)
	}
	if len(def) != 2 {
		t.Fatalf("default visibility must be NEW+ACTIVE only, got %d", len(def))
	}
	all, _ := st.QueryListRoots(ctx, true, true)
	if len(all) != 4 {
		t.Fatalf("explicit include must return all 4, got %d", len(all))
	}
}

func seedQueryRoot(t *testing.T, st *postgres.Store, ctx context.Context) {
	t.Helper()
	if err := st.CreateRoot(ctx, st.Pool(), pipeRoot, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
}

func qEntry(name, hash string, size int64, mt time.Time) domain.SnapshotEntry {
	alg := "sha256"
	return domain.SnapshotEntry{EntryLocalID: name, Name: name, ParentRef: "/",
		Size: &size, Mtime: &mt, ContentHash: &hash, HashAlgorithm: &alg}
}

func TestQueryRemovedHiddenByDefault(t *testing.T) {
	st, ctx := newStore(t)
	seedQueryRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1, RemovalGracePeriod: 0}
	mt := time.Now().UTC()

	runSnapshot(t, st, ctx, "f0000000-0000-0000-0000-000000000001",
		domain.AcceptanceComplete, "q1", []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}, cfg)
	a := requirePresence(t, st, ctx, "/a.txt")

	runSnapshot(t, st, ctx, "f0000000-0000-0000-0000-000000000002",
		domain.AcceptanceComplete, "q2", nil, cfg)

	def, err := st.QueryGetResource(ctx, a.ResourceID, false)
	if err != nil {
		t.Fatalf("get resource: %v", err)
	}
	if def != nil {
		t.Fatalf("REMOVED tombstone must be hidden by default, got %+v", def)
	}
	withRemoved, err := st.QueryGetResource(ctx, a.ResourceID, true)
	if err != nil {
		t.Fatalf("get resource includeRemoved: %v", err)
	}
	if withRemoved == nil || withRemoved.ResourcePresence != domain.ResourceRemoved {
		t.Fatalf("explicit history access must return the REMOVED tombstone, got %+v", withRemoved)
	}
}

func TestQueryResolvePathAmbiguity(t *testing.T) {
	st, ctx := newStore(t)
	seedQueryRoot(t, st, ctx)
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
	res, err := st.QueryResolvePath(ctx, pipeRoot, path, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Matches) != 2 || !res.Ambiguous {
		t.Fatalf("overlap must return both matches with ambiguous=true, got %d ambiguous=%v", len(res.Matches), res.Ambiguous)
	}
}

func TestQueryPaginationStaleCursor(t *testing.T) {
	st, ctx := newStore(t)
	seedQueryRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1}
	mt := time.Now().UTC()

	runSnapshot(t, st, ctx, "f0000000-0000-0000-0000-000000000011",
		domain.AcceptanceComplete, "pq1", []domain.SnapshotEntry{
			qEntry("a.txt", "ha", 1, mt),
			qEntry("b.txt", "hb", 2, mt),
		}, cfg)

	page1, err := st.QueryListActivePage(ctx, pipeRoot, nil, 1)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Items) != 1 || page1.Next == nil {
		t.Fatalf("page1 must return 1 item + next cursor, got %+v", page1)
	}

	// Advance the generation with a new resource.
	runSnapshot(t, st, ctx, "f0000000-0000-0000-0000-000000000012",
		domain.AcceptanceComplete, "pq2", []domain.SnapshotEntry{
			qEntry("a.txt", "ha", 1, mt),
			qEntry("b.txt", "hb", 2, mt),
			qEntry("c.txt", "hc", 3, mt),
		}, cfg)

	_, err = st.QueryListActivePage(ctx, pipeRoot, page1.Next, 1)
	if err != query.ErrStaleCursor {
		t.Fatalf("cursor from a stale generation must return STALE_CURSOR, got %v", err)
	}
}

func TestQueryReadJournalByCursor(t *testing.T) {
	st, ctx := newStore(t)
	seedQueryRoot(t, st, ctx)
	cfg := reconcile.Config{MinConsecutiveCompleteMissing: 1}
	mt := time.Now().UTC()
	runSnapshot(t, st, ctx, "f0000000-0000-0000-0000-000000000021",
		domain.AcceptanceComplete, "j1", []domain.SnapshotEntry{qEntry("a.txt", "ha", 1, mt)}, cfg)
	runSnapshot(t, st, ctx, "f0000000-0000-0000-0000-000000000022",
		domain.AcceptanceComplete, "j2", []domain.SnapshotEntry{qEntry("b.txt", "hb", 2, mt)}, cfg)

	all, err := st.QueryReadJournal(ctx, pipeRoot, 0, 10)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("expected >= 2 events, got %d", len(all))
	}
	tail, err := st.QueryReadJournal(ctx, pipeRoot, all[len(all)-1].EventSeq, 10)
	if err != nil {
		t.Fatalf("read journal tail: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("cursor at tail must return no more events, got %d", len(tail))
	}
}
