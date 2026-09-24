package scan_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// alistScopeMock is a deterministic in-memory AList /api/fs/list endpoint. It
// always answers with a coherent single page (total == len(content)).
type alistScopeMock struct {
	mu   sync.Mutex
	dirs map[string][]map[string]any
}

func newAListScopeMock() *alistScopeMock {
	return &alistScopeMock{dirs: map[string][]map[string]any{}}
}

func (m *alistScopeMock) set(dir string, entries ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entries == nil {
		entries = []map[string]any{}
	}
	m.dirs[dir] = entries
}

func (m *alistScopeMock) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		entries := m.dirs[req.Path]
		out := make([]map[string]any, len(entries))
		copy(out, entries)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"content": out, "total": len(out)},
		})
	}
}

func fileEntry(name string, size int64, sha1 string) map[string]any {
	return map[string]any{
		"name": name, "size": size, "is_dir": false,
		"modified":  "2026-01-02T03:04:05Z",
		"hash_info": map[string]string{"sha1": sha1},
	}
}

func activePaths(t *testing.T, qr query.Reader, rootID string) map[string]bool {
	t.Helper()
	page, err := qr.ListActivePage(context.Background(), rootID, nil, 1000)
	if err != nil {
		t.Fatalf("list active page: %v", err)
	}
	out := map[string]bool{}
	for _, it := range page.Items {
		if it.CanonicalPath != nil {
			out[*it.CanonicalPath] = true
		}
	}
	return out
}

func setupScopedRoot(t *testing.T) (*postgres.Store, *alistScopeMock, string) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	const rootID = "f0000000-0000-0000-0000-0000000000a1"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	mock := newAListScopeMock()
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)
	acfg, _ := json.Marshal(map[string]string{"base_url": srv.URL, "path": "/"})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "alist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}
	return st, mock, rootID
}

// TestScanScopeReconcileIsAdditiveSafe proves the P0 Kernel/reconcile contract on
// real PostgreSQL: scoped observations add/update, never remove, and never touch
// unrelated resources.
func TestScanScopeReconcileIsAdditiveSafe(t *testing.T) {
	st, mock, rootID := setupScopedRoot(t)
	ctx := context.Background()
	svc := scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	qr := postgres.NewQueryReader(st.Pool())

	// 1) baseline: only a.txt is observed and added.
	mock.set("/", fileEntry("a.txt", 5, "aaa"))
	res1, err := svc.ScanScope(ctx, rootID, "/", 100)
	if err != nil {
		t.Fatalf("baseline scoped scan: %v", err)
	}
	if res1.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("baseline scoped observation must APPLY, got %+v", res1.Outcome)
	}
	if got := activePaths(t, qr, rootID); !got["/a.txt"] || len(got) != 1 {
		t.Fatalf("expected exactly /a.txt canonical, got %v", got)
	}

	// 2) a new direct child is added; unrelated a.txt is untouched.
	mock.set("/", fileEntry("a.txt", 5, "aaa"), fileEntry("b.txt", 6, "bbb"))
	res2, err := svc.ScanScope(ctx, rootID, "/", 100)
	if err != nil {
		t.Fatalf("add scoped scan: %v", err)
	}
	if res2.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("adding b.txt must APPLY, got %+v", res2.Outcome)
	}
	if res2.Outcome.AppliedGeneration <= res1.Outcome.AppliedGeneration {
		t.Fatalf("same-root generation must advance (FIFO), got %d then %d",
			res1.Outcome.AppliedGeneration, res2.Outcome.AppliedGeneration)
	}
	if got := activePaths(t, qr, rootID); !got["/a.txt"] || !got["/b.txt"] {
		t.Fatalf("a.txt and b.txt must both be canonical, got %v", got)
	}

	// Q3 (get resource by id) and Q4 (list children) see the new resource.
	rootPage, err := qr.ListResources(ctx, rootID, nil, query.ReadOptions{}, nil, 100)
	if err != nil {
		t.Fatalf("Q4 list resources: %v", err)
	}
	var bID string
	for _, it := range rootPage.Items {
		if it.CanonicalPath != nil && *it.CanonicalPath == "/b.txt" {
			bID = it.ResourceID
		}
	}
	if bID == "" {
		t.Fatalf("Q4 must expose /b.txt, got %+v", rootPage.Items)
	}
	if res, _ := qr.GetResource(ctx, bID, query.ReadOptions{}); res == nil {
		t.Fatal("Q3 must expose the new resource")
	}

	// 3) an empty scoped observation must NOT remove anything.
	mock.set("/")
	res3, err := svc.ScanScope(ctx, rootID, "/", 100)
	if err != nil {
		t.Fatalf("empty scoped scan: %v", err)
	}
	if got := activePaths(t, qr, rootID); !got["/a.txt"] || !got["/b.txt"] {
		t.Fatalf("empty scoped observation must remove nothing, got %v (outcome=%+v)", got, res3.Outcome)
	}

	// 4) a previously known file missing from the response must NOT be removed.
	mock.set("/", fileEntry("a.txt", 5, "aaa")) // b.txt absent
	res4, err := svc.ScanScope(ctx, rootID, "/", 100)
	if err != nil {
		t.Fatalf("missing-file scoped scan: %v", err)
	}
	if got := activePaths(t, qr, rootID); !got["/a.txt"] || !got["/b.txt"] {
		t.Fatalf("PARTIAL absence must not describe removal, got %v (outcome=%+v)", got, res4.Outcome)
	}
	removed, err := qr.ListRemovedPage(ctx, rootID, nil, 100)
	if err != nil {
		t.Fatalf("Q7 removed page: %v", err)
	}
	if len(removed.Items) != 0 {
		t.Fatalf("PARTIAL scoped refresh must produce no removal evidence, got %d", len(removed.Items))
	}

	// 5) an unrelated direct child in a separate scope does not disturb root-level
	// resources, and is itself added as an independent positive observation.
	mock.set("/sub", fileEntry("c.txt", 7, "ccc"))
	res5, err := svc.ScanScope(ctx, rootID, "/sub", 100)
	if err != nil {
		t.Fatalf("sub-scope scoped scan: %v", err)
	}
	if res5.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("adding sub/c.txt must APPLY, got %+v", res5.Outcome)
	}
	got := activePaths(t, qr, rootID)
	if !got["/a.txt"] || !got["/b.txt"] || !got["/sub/c.txt"] {
		t.Fatalf("scoped refresh must add only its own child and leave others intact, got %v", got)
	}
}

// TestScanScopeFailsClosedForUnsupportedAndDeletedRoot proves the prototype
// refuses non-AList collectors and DELETED roots without submitting a Snapshot.
func TestScanScopeFailsClosedForUnsupportedAndDeletedRoot(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	const rootID = "f0000000-0000-0000-0000-0000000000b2"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "rclone", Config: []byte(`{"remote":"","path":"/tmp"}`)}); err != nil {
		t.Fatal(err)
	}
	svc := scan.New(st, "", "", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := svc.ScanScope(ctx, rootID, "/", 10); err == nil {
		t.Fatal("scoped refresh must fail closed for a non-alist collector")
	}

	if _, err := st.TransitionRootLifecycle(ctx, rootID, domain.RootDeprecated); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TransitionRootLifecycle(ctx, rootID, domain.RootDeleted); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ScanScope(ctx, rootID, "/", 10); err == nil {
		t.Fatal("scoped refresh of a DELETED root must be rejected")
	}
}
