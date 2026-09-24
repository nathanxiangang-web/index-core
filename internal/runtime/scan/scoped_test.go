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
// always answers with a coherent single page (total == len(content)), keyed by
// the provider path it is asked for.
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

func fileEntryAt(name string, size int64, sha1, modified string) map[string]any {
	return map[string]any{
		"name": name, "size": size, "is_dir": false,
		"modified":  modified,
		"hash_info": map[string]string{"sha1": sha1},
	}
}

func fileEntry(name string, size int64, sha1 string) map[string]any {
	return fileEntryAt(name, size, sha1, "2026-01-02T03:04:05Z")
}

func dirEntry(name string) map[string]any {
	return map[string]any{
		"name": name, "size": 0, "is_dir": true,
		"modified": "2026-01-02T03:04:05Z",
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

func setupScopedRootPath(t *testing.T, rootPath string) (*postgres.Store, *alistScopeMock, string) {
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
	acfg, _ := json.Marshal(map[string]string{"base_url": srv.URL, "path": rootPath})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "alist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}
	return st, mock, rootID
}

func setupScopedRoot(t *testing.T) (*postgres.Store, *alistScopeMock, string) {
	return setupScopedRootPath(t, "/")
}

// assertRemovalEvidenceClean asserts a PRESENT path carries no removal evidence:
// state NONE, no missing_since, zero consecutive-missing counter.
func assertRemovalEvidenceClean(t *testing.T, st *postgres.Store, rootID, path string) {
	t.Helper()
	var state string
	var missingSince *time.Time
	var consec int
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT removal_evidence_state, missing_since, consecutive_complete_missing
		   FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND canonical_path=$2 AND resource_presence='PRESENT'`,
		rootID, path).Scan(&state, &missingSince, &consec); err != nil {
		t.Fatalf("load removal evidence for %q: %v", path, err)
	}
	if state != "NONE" || missingSince != nil || consec != 0 {
		t.Fatalf("path %q must have no removal evidence, got state=%s missing_since=%v consec=%d",
			path, state, missingSince, consec)
	}
}

// assertSnapshotScopedSemantics asserts the persisted PARTIAL Snapshot semantics,
// including the Kernel acceptance_state.
func assertSnapshotScopedSemantics(t *testing.T, st *postgres.Store, snapID string) {
	t.Helper()
	var traversal, completeness, freshness, assurance, acceptance string
	var skipped []byte
	var knownEmpty bool
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT traversal_status, completeness_flag, freshness_evidence,
		        collector_completeness_assurance, skipped_scopes, skipped_scopes_known_empty,
		        coalesce(acceptance_state, '')
		   FROM index_snapshot WHERE snapshot_id=$1::uuid`, snapID).
		Scan(&traversal, &completeness, &freshness, &assurance, &skipped, &knownEmpty, &acceptance); err != nil {
		t.Fatalf("load snapshot %s: %v", snapID, err)
	}
	if traversal != "PARTIAL" || completeness != "PARTIAL" {
		t.Fatalf("scoped snapshot must be PARTIAL/PARTIAL, got %s/%s", traversal, completeness)
	}
	if freshness != "FRESH_REFRESHED" {
		t.Fatalf("scoped snapshot must be FRESH_REFRESHED, got %s", freshness)
	}
	if assurance != "WEAK_FAILURE_VISIBILITY" {
		t.Fatalf("scoped snapshot must be WEAK_FAILURE_VISIBILITY, got %s", assurance)
	}
	if acceptance != "PARTIAL" {
		t.Fatalf("scoped snapshot acceptance_state must be PARTIAL, got %q", acceptance)
	}
	if skipped != nil || knownEmpty {
		t.Fatalf("scoped snapshot skip evidence must stay UNKNOWN, got %v/%v", skipped, knownEmpty)
	}
}

func admissionSeqs(t *testing.T, st *postgres.Store, rootID string) []int64 {
	t.Helper()
	rows, err := st.Pool().Query(context.Background(),
		`SELECT admission_seq FROM index_admission WHERE root_id=$1::uuid ORDER BY admission_seq`, rootID)
	if err != nil {
		t.Fatalf("load admissions: %v", err)
	}
	defer rows.Close()
	var seqs []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, s)
	}
	return seqs
}

func journalEventTypes(t *testing.T, st *postgres.Store, rootID string) []string {
	t.Helper()
	rows, err := st.Pool().Query(context.Background(),
		`SELECT event_type FROM index_journal_event WHERE root_id=$1::uuid ORDER BY event_seq`, rootID)
	if err != nil {
		t.Fatalf("load journal: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func resourceIDAtPath(t *testing.T, st *postgres.Store, rootID, path string) string {
	t.Helper()
	var id string
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT resource_id::text FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND canonical_path=$2 AND resource_presence='PRESENT'`, rootID, path).Scan(&id); err != nil {
		t.Fatalf("load resource id for %q: %v", path, err)
	}
	return id
}

// TestScanScopeReconcileIsAdditiveSafe proves the P0 Kernel/reconcile contract on
// real PostgreSQL: scoped observations add/update, never remove, produce no
// removal evidence, keep real parent links, and update metadata under identity
// continuity without changing resource_id.
func TestScanScopeReconcileIsAdditiveSafe(t *testing.T) {
	st, mock, rootID := setupScopedRoot(t)
	ctx := context.Background()
	svc := scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	qr := postgres.NewQueryReader(st.Pool())

	// 1) baseline: a.txt + sub/ + b.txt.
	mock.set("/", fileEntry("a.txt", 5, "aaa"), dirEntry("sub"), fileEntry("b.txt", 6, "bbb"))
	res1, err := svc.ScanScope(ctx, rootID, "/", 100)
	if err != nil {
		t.Fatalf("baseline scoped scan: %v", err)
	}
	if res1.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("baseline scoped observation must APPLY, got %+v", res1.Outcome)
	}
	assertSnapshotScopedSemantics(t, st, res1.SnapshotID)
	got := activePaths(t, qr, rootID)
	if !got["/a.txt"] || !got["/b.txt"] || !got["/sub"] || len(got) != 3 {
		t.Fatalf("expected /a.txt,/b.txt,/sub canonical, got %v", got)
	}
	subID := resourceIDAtPath(t, st, rootID, "/sub")
	aID := resourceIDAtPath(t, st, rootID, "/a.txt")

	// 2) identity continuity: SAME path + SAME hash + SAME size, changed mtime ->
	//    canonical UPDATE, resource_id unchanged, Journal records resource-updated.
	mock.set("/", fileEntryAt("a.txt", 5, "aaa", "2026-01-09T00:00:00Z"), dirEntry("sub"), fileEntry("b.txt", 6, "bbb"))
	res2, err := svc.ScanScope(ctx, rootID, "/", 100)
	if err != nil {
		t.Fatalf("metadata update scoped scan: %v", err)
	}
	if res2.Outcome.Status != domain.AdmissionApplied || !res2.Outcome.Mutated {
		t.Fatalf("mtime change must APPLY and mutate, got %+v", res2.Outcome)
	}
	if got := resourceIDAtPath(t, st, rootID, "/a.txt"); got != aID {
		t.Fatalf("identity continuity broken: /a.txt resource_id changed %s -> %s", aID, got)
	}
	if n := len(activePaths(t, qr, rootID)); n != 3 {
		t.Fatalf("update must not create new resources, got %d PRESENT", n)
	}
	var updated bool
	for _, e := range journalEventTypes(t, st, rootID) {
		if e == "resource-updated" {
			updated = true
		}
	}
	if !updated {
		t.Fatalf("metadata update must emit resource-updated, got %v", journalEventTypes(t, st, rootID))
	}

	// Same-root FIFO: admission_seq strictly increasing 1,2,...
	if seqs := admissionSeqs(t, st, rootID); len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("same-root FIFO admission_seq must be [1 2], got %v", seqs)
	}

	// 3) non-root scope /sub: its direct child is added with a real parent link.
	mock.set("/sub", fileEntry("c.txt", 7, "ccc"))
	res3, err := svc.ScanScope(ctx, rootID, "/sub", 100)
	if err != nil {
		t.Fatalf("sub-scope scoped scan: %v", err)
	}
	if res3.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("adding /sub/c.txt must APPLY, got %+v", res3.Outcome)
	}
	if got := activePaths(t, qr, rootID); !got["/a.txt"] || !got["/b.txt"] || !got["/sub"] || !got["/sub/c.txt"] {
		t.Fatalf("scoped refresh must add only its own child, got %v", got)
	}
	var parentID *string
	if err := st.Pool().QueryRow(ctx,
		`SELECT parent_resource_id::text FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND canonical_path='/sub/c.txt' AND resource_presence='PRESENT'`, rootID).Scan(&parentID); err != nil {
		t.Fatal(err)
	}
	if parentID == nil || *parentID != subID {
		t.Fatalf("/sub/c.txt must have /sub as parent, got %v want %s", parentID, subID)
	}

	// 4) empty scoped observation removes nothing and produces no evidence.
	mock.set("/sub")
	res4, err := svc.ScanScope(ctx, rootID, "/sub", 100)
	if err != nil {
		t.Fatalf("empty scoped scan: %v", err)
	}
	if got := activePaths(t, qr, rootID); !got["/sub/c.txt"] {
		t.Fatalf("empty scoped observation must remove nothing, got %v (%+v)", got, res4.Outcome)
	}
	assertRemovalEvidenceClean(t, st, rootID, "/sub/c.txt")

	// 5) a previously known file missing from the response must not be removed and
	//    must not accrue any removal evidence.
	mock.set("/sub") // c.txt absent this round
	res5, err := svc.ScanScope(ctx, rootID, "/sub", 100)
	if err != nil {
		t.Fatalf("missing-file scoped scan: %v", err)
	}
	if got := activePaths(t, qr, rootID); !got["/sub/c.txt"] {
		t.Fatalf("PARTIAL absence must not describe removal, got %v (%+v)", got, res5.Outcome)
	}
	assertRemovalEvidenceClean(t, st, rootID, "/sub/c.txt")
	assertRemovalEvidenceClean(t, st, rootID, "/a.txt")

	removed, err := qr.ListRemovedPage(ctx, rootID, nil, 100)
	if err != nil {
		t.Fatalf("Q7 removed page: %v", err)
	}
	if len(removed.Items) != 0 {
		t.Fatalf("PARTIAL scoped refresh must produce no removal evidence, got %d", len(removed.Items))
	}
}

// TestScanScopeConfiguredRootPathNamespace proves the scoped refresh works when
// the OpenList root path is NOT "/": the canonical parent check must use the same
// provider-path namespace that Adapter.Scan() records ("/library/sub"), not the
// root-relative scope ("/sub").
func TestScanScopeConfiguredRootPathNamespace(t *testing.T) {
	st, mock, rootID := setupScopedRootPath(t, "/library")
	ctx := context.Background()
	svc := scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	qr := postgres.NewQueryReader(st.Pool())

	mock.set("/library", fileEntry("a.txt", 5, "aaa"), dirEntry("sub"))
	res1, err := svc.ScanScope(ctx, rootID, "/", 100)
	if err != nil {
		t.Fatalf("baseline scoped scan under root /library: %v", err)
	}
	if res1.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("baseline must APPLY, got %+v", res1.Outcome)
	}
	if got := activePaths(t, qr, rootID); !got["/library/a.txt"] || !got["/library/sub"] {
		t.Fatalf("canonical paths must carry the configured root prefix, got %v", got)
	}
	subRows, err := st.PresentResourcesAtPath(ctx, st.Pool(), rootID, "/library/sub")
	if err != nil || len(subRows) != 1 || subRows[0].IsDir == nil || !*subRows[0].IsDir {
		t.Fatalf("/library/sub must be a unique PRESENT canonical directory, got %+v (%v)", subRows, err)
	}

	// scope "/sub" must resolve to provider "/library/sub" and pass the parent guard.
	mock.set("/library/sub", fileEntry("c.txt", 7, "ccc"))
	res2, err := svc.ScanScope(ctx, rootID, "/sub", 100)
	if err != nil {
		t.Fatalf("non-root scope must resolve inside the configured root, got %v", err)
	}
	if res2.Outcome.Status != domain.AdmissionApplied {
		t.Fatalf("adding /library/sub/c.txt must APPLY, got %+v", res2.Outcome)
	}
	if got := activePaths(t, qr, rootID); !got["/library/sub/c.txt"] {
		t.Fatalf("expected /library/sub/c.txt canonical, got %v", got)
	}
	var parentID *string
	if err := st.Pool().QueryRow(ctx,
		`SELECT parent_resource_id::text FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND canonical_path='/library/sub/c.txt' AND resource_presence='PRESENT'`, rootID).Scan(&parentID); err != nil {
		t.Fatal(err)
	}
	if parentID == nil || *parentID != subRows[0].ResourceID {
		t.Fatalf("/library/sub/c.txt must have /library/sub as parent, got %v want %s", parentID, subRows[0].ResourceID)
	}
}

// TestScanScopeDrainsOlderPendingHead proves absolute same-root FIFO: a stranded
// older PENDING admission is drained before the scoped observation is admitted,
// and the new admission never leapfrogs it.
func TestScanScopeDrainsOlderPendingHead(t *testing.T) {
	st, mock, rootID := setupScopedRoot(t)
	ctx := context.Background()

	// Interrupted prior work: SUBMITTED + PENDING seq=1, never processed.
	oldSnap := "e4000000-0000-0000-0000-0000000000a1"
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	count := int64(1)
	if err := st.InsertSnapshotStub(ctx, st.Pool(), domain.Snapshot{
		SnapshotID: oldSnap, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft, EntryCount: &count,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), oldSnap); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.AdmitOrResumeSnapshot(ctx, rootID, oldSnap); err != nil {
		t.Fatal(err)
	}

	mock.set("/", fileEntry("a.txt", 5, "aaa"))
	svc := scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := svc.ScanScope(ctx, rootID, "/", 100); err != nil {
		t.Fatalf("scoped scan after stranded pending: %v", err)
	}

	var pending int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM index_admission WHERE root_id=$1::uuid AND status='PENDING'`, rootID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("scoped scan must drain the older pending head, %d still PENDING", pending)
	}
	seqs := admissionSeqs(t, st, rootID)
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("older work must be admitted first (seq 1 then 2), got %v", seqs)
	}
	if got := activePaths(t, postgres.NewQueryReader(st.Pool()), rootID); !got["/a.txt"] {
		t.Fatalf("scoped observation must be canonical after draining, got %v", got)
	}
}

// TestScanScopeFailsClosedOnBadScope proves scope containment (no "." / "..")
// and the orphan guard (a non-root scope must be a PRESENT canonical directory).
func TestScanScopeFailsClosedOnBadScope(t *testing.T) {
	st, mock, rootID := setupScopedRoot(t)
	ctx := context.Background()
	svc := scan.New(st, "", "", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	mock.set("/", fileEntry("a.txt", 5, "aaa"))
	if _, err := svc.ScanScope(ctx, rootID, "/", 100); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	for _, scope := range []string{"../etc", "/a/../../etc", "..", "/sub/.."} {
		if _, err := svc.ScanScope(ctx, rootID, scope, 100); err == nil {
			t.Fatalf("traversal scope %q must fail closed", scope)
		}
	}
	mock.set("/ghost", fileEntry("x", 1, "x"))
	if _, err := svc.ScanScope(ctx, rootID, "/ghost", 100); err == nil {
		t.Fatal("scoped refresh of a non-existent canonical directory must fail closed")
	}
	if _, err := svc.ScanScope(ctx, rootID, "/", 1<<30); err == nil {
		t.Fatal("max_entries above the P0 hard cap must fail closed")
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
