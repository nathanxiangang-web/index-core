package scan_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// R2 fixtures are synthetic independent source truth, not copied Canonical rows.
// No recursive scanner/scheduler is implemented: tests explicitly select scopes.
type r2Fixture struct {
	mu            sync.Mutex
	source        map[string]bool // absolute path -> directory; file metadata is deterministic
	fault         string
	topologyCalls int
	expectedToken string
}

func r2Source() map[string]bool {
	return map[string]bool{"/target": true, "/target/file.bin": false, "/target/child": true, "/target/child/deep.bin": false, "/target-other": true, "/target-other/keep.bin": false, "/deep": true, "/deep/n1": true, "/deep/n1/n2": true, "/deep/n1/n2/keep.bin": false, "/empty": true, "/copy.bin": false, "/root.bin": false}
}
func r2Paths(source map[string]bool) []string {
	out := []string{}
	for p := range source {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
func (f *r2Fixture) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	reply := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": data}) }
	switch r.URL.Path {
	case "/api/me":
		base := "/drive/library"
		if f.fault == "namespace-drift" {
			base = "/elsewhere"
		}
		reply(map[string]any{"base_path": base})
	case "/api/admin/storage/list":
		f.topologyCalls++
		if r.Method != "GET" || r.Header.Get("Authorization") != f.expectedToken {
			w.WriteHeader(401)
			return
		}
		if f.fault == "topology-expired" {
			w.WriteHeader(401)
			return
		}
		if f.fault == "topology-http" {
			w.WriteHeader(503)
			return
		}
		if f.fault == "topology-redirect" {
			http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
			return
		}
		driver, status := "115 Open", "work"
		if f.fault == "alias-cover" {
			driver = "Alias"
		}
		if f.fault == "unknown-cover" {
			driver = "unknown"
		}
		if f.fault == "failed-cover" {
			status = "error"
		}
		mounts := []map[string]any{{"id": 1, "mount_path": "/drive", "driver": driver, "status": status, "disabled": false}}
		if f.fault == "virtual-only" || f.fault == "hidden-mount" || f.fault == "nested-mount" {
			mount := "/drive/library/alias"
			if f.fault == "nested-mount" {
				mount = "/drive/library/deep/nested"
			}
			mounts = append(mounts, map[string]any{"id": 2, "mount_path": mount, "disabled": false, "status": "work"})
		}
		if f.fault == "topology-changed" {
			mounts[0]["modified"] = fmt.Sprint(f.topologyCalls)
		}
		total := len(mounts)
		if f.fault == "topology-truncated" {
			total++
		}
		reply(map[string]any{"content": mounts, "total": total})
	case "/api/fs/list":
		var body struct {
			Path    string `json:"path"`
			Refresh bool   `json:"refresh"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || !body.Refresh {
			w.WriteHeader(400)
			return
		}
		if f.fault == "list-http" {
			w.WriteHeader(503)
			return
		}
		if f.fault == "cancel" {
			<-r.Context().Done()
			return
		}
		if f.fault == "list-denied" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 403})
			return
		}
		entries := []map[string]any{}
		for _, p := range r2Paths(f.source) {
			if path.Dir(p) == body.Path {
				if f.source[p] {
					entries = append(entries, dirEntry(path.Base(p)))
				} else {
					hash := "unique-" + p
					if p == "/copy.bin" || p == "/target/file.bin" || p == "/deep/moved.bin" || p == "/target/another.bin" {
						hash = "same-content"
					}
					if p == "/renamed.bin" {
						hash = "unique-/root.bin"
					}
					entries = append(entries, fileEntry(path.Base(p), 10, hash))
				}
			}
		}
		if f.fault == "virtual-only" {
			entries = []map[string]any{dirEntry("alias")}
		}
		total := len(entries)
		if f.fault == "list-truncated" {
			total++
		}
		reply(map[string]any{"content": entries, "total": total})
	default:
		w.WriteHeader(500)
	}
}

type r2Harness struct {
	t       *testing.T
	st      *postgres.Store
	qr      query.Reader
	svc     *scan.Service
	root    string
	fixture *r2Fixture
	logger  *slog.Logger
}

func newR2(t *testing.T) *r2Harness {
	t.Helper()
	st, _, root := setupScopedRoot(t)
	f := &r2Fixture{source: r2Source(), expectedToken: "r2-topology"}
	server := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(server.Close)
	t.Setenv("P0_R2_TOPOLOGY", "r2-topology")
	config, _ := json.Marshal(map[string]any{"base_url": server.URL, "path": "/", "expected_base_path": "/drive/library", "mirror_presence": true, "topology_token_env": "P0_R2_TOPOLOGY"})
	if err := st.UpsertAdapterConfig(context.Background(), root, postgres.AdapterConfig{CollectorKind: "openlist", Config: config}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &r2Harness{t: t, st: st, qr: postgres.NewQueryReader(st.Pool()), svc: scan.New(st, "", "", time.Second, logger), root: root, fixture: f, logger: logger}
	for _, p := range []string{"/", "/target", "/target/child", "/target-other", "/deep", "/deep/n1", "/deep/n1/n2", "/empty"} {
		h.observe(p)
	}
	h.check("before", r2Paths(f.source))
	return h
}
func (h *r2Harness) observe(scope string) {
	h.t.Helper()
	if _, err := h.svc.ScanScope(context.Background(), h.root, scope, 100); err != nil {
		h.t.Fatalf("observe %s: %v", scope, err)
	}
}
func (h *r2Harness) events() []query.JournalEventView {
	h.t.Helper()
	events, err := h.qr.ReadJournal(context.Background(), h.root, 0, 1000)
	if err != nil {
		h.t.Fatal(err)
	}
	return events
}

// Validate Q4 hierarchy, Q5 uniqueness, and full Q8 -> Canonical presence replay,
// including every retained tombstone. Counts are fixture-derived, never274.
func (h *r2Harness) check(label string, want []string) map[string]string {
	h.t.Helper()
	ctx := context.Background()
	page, err := h.qr.ListActivePage(ctx, h.root, nil, 1000)
	if err != nil || page.Next != nil {
		h.t.Fatalf("Q6: %v", err)
	}
	actual := []string{}
	ids := map[string]string{}
	for _, row := range page.Items {
		if row.CanonicalPath == nil {
			h.t.Fatal("missing path")
		}
		p := *row.CanonicalPath
		actual = append(actual, p)
		ids[p] = row.ResourceID
		q5, err := h.qr.ResolvePath(ctx, h.root, p, query.ReadOptions{})
		if err != nil || q5.Ambiguous || len(q5.Matches) != 1 || q5.Matches[0].ResourceID != row.ResourceID || q5.Matches[0].ResourcePresence != domain.ResourcePresent {
			h.t.Fatalf("Q5 %s: %+v %v", p, q5, err)
		}
	}
	sort.Strings(actual)
	want = append([]string{}, want...)
	sort.Strings(want)
	if !reflect.DeepEqual(actual, want) {
		h.t.Fatalf("%s source oracle mismatch\nactual=%v\nwant=%v", label, actual, want)
	}
	parents := []string{"/"}
	for _, row := range page.Items {
		if row.IsDir != nil && *row.IsDir {
			parents = append(parents, *row.CanonicalPath)
		}
	}
	for _, parent := range parents {
		var id *string
		if parent != "/" {
			v := ids[parent]
			id = &v
		}
		children, err := h.qr.ListResources(ctx, h.root, id, query.ReadOptions{}, nil, 1000)
		if err != nil || children.Next != nil {
			h.t.Fatalf("Q4 %s: %v", parent, err)
		}
		got, expected := []string{}, []string{}
		for _, x := range children.Items {
			if x.CanonicalPath == nil {
				h.t.Fatal("Q4 path missing")
			}
			got = append(got, *x.CanonicalPath)
		}
		for _, p := range want {
			if path.Dir(p) == parent {
				expected = append(expected, p)
			}
		}
		sort.Strings(got)
		sort.Strings(expected)
		if !reflect.DeepEqual(got, expected) {
			h.t.Fatalf("Q4 %s got%v want%v", parent, got, expected)
		}
	}
	events := h.events()
	seen := map[int64]bool{}
	lastState := map[string]domain.ResourcePresence{}
	var seq, gen int64
	var intra int32
	counts := map[domain.EventType]int{}
	for _, e := range events {
		if e.EventSeq <= seq || seen[e.EventSeq] || e.GenerationNumber < gen || (e.GenerationNumber == gen && e.IntraGenerationSeq <= intra) {
			h.t.Fatal("Q8 ordering/uniqueness")
		}
		seen[e.EventSeq] = true
		seq = e.EventSeq
		gen = e.GenerationNumber
		intra = e.IntraGenerationSeq
		counts[e.EventType]++
		if e.ResourceID != nil {
			prior, exists := lastState[*e.ResourceID]
			switch e.EventType {
			case domain.EventResourceAdded:
				if exists {
					h.t.Fatal("Q8 duplicated ADD or resurrected a tombstone identity")
				}
				lastState[*e.ResourceID] = domain.ResourcePresent
			case domain.EventResourceRemoved:
				if !exists || prior != domain.ResourcePresent {
					h.t.Fatal("Q8 removed an unknown/already removed identity")
				}
				lastState[*e.ResourceID] = domain.ResourceRemoved
			case domain.EventResourceUpdated, domain.EventResourceMoved, domain.EventResourceRenamed:
				if !exists || prior != domain.ResourcePresent {
					h.t.Fatal("Q8 updated an unknown/removed identity")
				}
			default:
				h.t.Fatal("unexpected resource event")
			}
		}
	}
	for id, presence := range lastState {
		row, err := h.qr.GetResource(ctx, id, query.ReadOptions{IncludeRemoved: true})
		if err != nil || row.ResourcePresence != presence {
			h.t.Fatalf("Q8 replay differs from Canonical %s: %+v %v", id, row, err)
		}
	}
	var canonical, stored int
	if err := h.st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE root_id=$1`, h.root).Scan(&canonical); err != nil {
		h.t.Fatal(err)
	}
	if err := h.st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_journal_event WHERE root_id=$1`, h.root).Scan(&stored); err != nil {
		h.t.Fatal(err)
	}
	if canonical != len(lastState) || stored != len(events) {
		h.t.Fatal("Q8/Canonical record coverage mismatch")
	}
	root, err := h.qr.GetRoot(ctx, h.root, query.ReadOptions{})
	if err != nil || root.CurrentGeneration != gen {
		h.t.Fatalf("generation: %+v %v", root, err)
	}
	var work, watch int
	h.st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_dirty_scope_work WHERE root_id=$1`, h.root).Scan(&work)
	h.st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_scope_watch_state WHERE root_id=$1`, h.root).Scan(&watch)
	h.t.Logf("R2 checkpoint=%s source_entries=%d canonical_present=%d all_ids=%d Q8=%v generation=%d work_rows=%d watch_rows=%d", label, len(h.fixture.source), len(actual), canonical, counts, gen, work, watch)
	return ids
}
func (h *r2Harness) stable(scopes []string, want []string, ids map[string]string) {
	h.t.Helper()
	before := h.events()
	root, _ := h.qr.GetRoot(context.Background(), h.root, query.ReadOptions{})
	for i := 1; i <= 3; i++ {
		for _, scope := range scopes {
			h.observe(scope)
		}
		after := h.check(fmt.Sprintf("next-%d", i), want)
		if !reflect.DeepEqual(after, ids) || !reflect.DeepEqual(before, h.events()) {
			h.t.Fatal("replay changed identity or Q8")
		}
		now, _ := h.qr.GetRoot(context.Background(), h.root, query.ReadOptions{})
		if now.CurrentGeneration != root.CurrentGeneration {
			h.t.Fatal("NOOP advanced generation")
		}
	}
}

func TestP0R2TrustedSourceChangesAndThreeCycleReplay(t *testing.T) {
	for _, kind := range []string{"file-delete", "root-file-delete", "subtree-delete", "empty-directory-delete", "delete-recreate", "rename", "move", "same-content-copy"} {
		t.Run(kind, func(t *testing.T) {
			h := newR2(t)
			f := h.fixture
			before := h.check("baseline", r2Paths(f.source))
			scope := "/target"
			old := before["/target/file.bin"]
			removed := 0
			f.mu.Lock()
			switch kind {
			case "file-delete", "delete-recreate":
				delete(f.source, "/target/file.bin")
				removed = 1
			case "root-file-delete":
				delete(f.source, "/root.bin")
				scope = "/"
				removed = 1
			case "subtree-delete":
				for p := range f.source {
					if p == "/target" || strings.HasPrefix(p, "/target/") {
						delete(f.source, p)
					}
				}
				scope = "/"
				removed = 4
			case "empty-directory-delete":
				delete(f.source, "/empty")
				scope = "/"
				removed = 1
			case "rename":
				delete(f.source, "/root.bin")
				f.source["/renamed.bin"] = false
				scope = "/"
				// ADR-004: a live hash-only candidate is not object identity.
				// Confirm old absence and create the new path; do not guess a move.
				removed = 1
			case "move":
				delete(f.source, "/target/file.bin")
				f.source["/deep/moved.bin"] = false
				removed = 1
			case "same-content-copy":
				f.source["/target/another.bin"] = false
			}
			f.mu.Unlock()
			if kind == "move" {
				h.observe("/deep")
			}
			h.observe(scope)
			want := r2Paths(f.source)
			after := h.check("after", want)
			for p, id := range before {
				if _, exists := f.source[p]; exists && after[p] != id {
					t.Fatalf("survivor identity changed at %s", p)
				}
			}
			if after["/copy.bin"] == after["/target/file.bin"] && after["/target/file.bin"] != "" {
				t.Fatal("identical content copies merged")
			}
			n := 0
			for _, e := range h.events() {
				if e.EventType == domain.EventResourceRemoved {
					n++
				}
			}
			if n != removed {
				t.Fatalf("removal events %d want%d", n, removed)
			}
			h.stable([]string{scope}, want, after)
			if kind == "rename" && after["/renamed.bin"] == before["/root.bin"] {
				t.Fatal("hash-only rename invented identity continuity")
			}
			if kind == "delete-recreate" {
				f.mu.Lock()
				f.source["/target/file.bin"] = false
				f.mu.Unlock()
				h.observe(scope)
				after = h.check("recreated", r2Paths(f.source))
				if after["/target/file.bin"] == old {
					t.Fatal("tombstone resurrected")
				}
				h.stable([]string{scope}, r2Paths(f.source), after)
			}
		})
	}
}

func TestP0R2UntrustedFailureMatrixAndSourceRecovery(t *testing.T) {
	for _, fault := range []string{"virtual-only", "hidden-mount", "nested-mount", "alias-cover", "unknown-cover", "failed-cover", "topology-expired", "topology-http", "topology-redirect", "topology-truncated", "topology-changed", "missing-env", "wrong-credential", "credential-rotation", "list-http", "list-denied", "list-truncated", "namespace-drift", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			h := newR2(t)
			f := h.fixture
			baseline := h.check("baseline", r2Paths(f.source))
			want := r2Paths(f.source)
			events := h.events()
			f.mu.Lock()
			for p := range f.source {
				if p == "/target" || strings.HasPrefix(p, "/target/") {
					delete(f.source, p)
				}
			}
			delete(f.source, "/root.bin")
			f.fault = fault
			if fault == "credential-rotation" {
				f.expectedToken = "r2-rotated"
			}
			f.mu.Unlock()
			if fault == "missing-env" {
				t.Setenv("P0_R2_TOPOLOGY", "")
			}
			if fault == "wrong-credential" {
				t.Setenv("P0_R2_TOPOLOGY", "invalid")
			}
			for i := 0; i < 4; i++ {
				ctx := context.Background()
				var cancel context.CancelFunc
				if fault == "cancel" {
					ctx, cancel = context.WithTimeout(ctx, 40*time.Millisecond)
				}
				_, err := h.svc.ScanScope(ctx, h.root, "/", 100)
				if cancel != nil {
					cancel()
				}
				mustFail := fault == "list-http" || fault == "list-denied" || fault == "list-truncated" || fault == "namespace-drift" || fault == "cancel"
				if (err != nil) != mustFail {
					t.Fatalf("%s wrong error: %v", fault, err)
				}
				expected := append([]string{}, want...)
				if fault == "virtual-only" {
					expected = append(expected, "/alias")
				}
				ids := h.check(fmt.Sprintf("fault-%d", i), expected)
				for p, id := range baseline {
					if ids[p] != id {
						t.Fatalf("untrusted observation removed/reidentified %s", p)
					}
				}
				if fault != "virtual-only" && !reflect.DeepEqual(events, h.events()) {
					t.Fatal("failed/untrusted omission changed Q8")
				}
			}
			f.mu.Lock()
			f.source = r2Source()
			f.fault = ""
			f.mu.Unlock()
			t.Setenv("P0_R2_TOPOLOGY", f.expectedToken)
			h.observe("/")
			after := h.check("source-recovered", r2Paths(f.source))
			for p, id := range baseline {
				if after[p] != id {
					t.Fatal("positive recovery lost identity")
				}
			}
			h.stable([]string{"/"}, r2Paths(f.source), after)
		})
	}
}

func TestP0R2ParentCoverageCannotInferDescendantAbsence(t *testing.T) {
	h := newR2(t)
	want := r2Paths(h.fixture.source)
	before := h.check("before-descendant-change", want)
	events := h.events()
	h.fixture.mu.Lock()
	delete(h.fixture.source, "/target/child/deep.bin")
	h.fixture.mu.Unlock()
	// Root can confirm its direct children, not hidden changes below a survivor.
	for cycle := 0; cycle < 4; cycle++ {
		h.observe("/")
		if !reflect.DeepEqual(before, h.check(fmt.Sprintf("parent-only-%d", cycle), want)) || !reflect.DeepEqual(events, h.events()) {
			t.Fatal("root direct-child coverage invented descendant absence")
		}
	}
	h.observe("/target/child")
	after := h.check("child-confirmed-empty", r2Paths(h.fixture.source))
	if len(h.events()) != len(events)+1 || h.events()[len(events)].EventType != domain.EventResourceRemoved {
		t.Fatal("child absence must emit exactly one removal")
	}
	h.stable([]string{"/", "/target/child"}, r2Paths(h.fixture.source), after)
}

func TestP0R2DelayedObservationFencesAndThreeCycleReplay(t *testing.T) {
	for _, kind := range []string{"old-generation", "adapter-drift", "old-time"} {
		t.Run(kind, func(t *testing.T) {
			h := newR2(t)
			ctx := context.Background()
			before := h.check("before-delayed", r2Paths(h.fixture.source))
			events := h.events()
			cfg, err := h.st.GetAdapterConfig(ctx, h.root)
			if err != nil {
				t.Fatal(err)
			}
			root, err := h.qr.GetRoot(ctx, h.root, query.ReadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC()
			if kind == "old-time" {
				at = at.Add(-3 * time.Minute)
			}
			coverage := domain.DirectoryCoverage{Version: 2, Path: "/", Generation: root.CurrentGeneration, EntryCount: 0, AdapterDigest: postgres.AdapterConfigDigest(cfg), TopologyDigest: strings.Repeat("a", 64)}
			provenance, _ := json.Marshal(map[string]any{"scope": "/", "directory_coverage": coverage})
			fresh := domain.FreshRefreshed
			count := int64(0)
			snap := domain.Snapshot{SnapshotID: reconcile.NewUUID(), RootID: h.root, Provenance: provenance, ObservedAt: at, TraversalStatus: domain.TraversalPartial, CompletenessFlag: domain.CompletenessFlagPartial, FreshnessEvidence: &fresh, LifecycleState: domain.SnapshotDraft, EntryCount: &count}
			// Old empty response is retained as an immutable draft; newer positive input
			// or config wins before this response is admitted. No Canonical SQL mutation.
			if err := h.st.CreateDraftSnapshot(ctx, snap, nil); err != nil {
				t.Fatal(err)
			}
			if kind == "old-generation" {
				h.fixture.mu.Lock()
				h.fixture.source["/newer.bin"] = false
				h.fixture.mu.Unlock()
				h.observe("/")
				before = h.check("newer-positive", r2Paths(h.fixture.source))
				events = h.events()
			}
			if kind == "adapter-drift" {
				var config map[string]any
				if err := json.Unmarshal(cfg.Config, &config); err != nil {
					t.Fatal(err)
				}
				config["r2_fixture_revision"] = 1
				cfg.Config, _ = json.Marshal(config)
				if err := h.st.UpsertAdapterConfig(ctx, h.root, cfg); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.st.SubmitAndAdmitSnapshot(ctx, h.root, snap.SnapshotID); err != nil {
				t.Fatal(err)
			}
			coord := postgres.NewCoordinator(h.st, reconcile.Config{})
			out, err := coord.ProcessSnapshot(ctx, h.root, snap.SnapshotID)
			if err != nil || out.Status != domain.AdmissionStaleInput || out.Mutated {
				t.Fatalf("late %s: %+v %v", kind, out, err)
			}
			after := h.check("late-rejected", r2Paths(h.fixture.source))
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(events, h.events()) {
				t.Fatal("late response changed Canonical/Q8")
			}
			h.stable([]string{"/"}, r2Paths(h.fixture.source), after)
		})
	}
}

func TestP0R2JournalRollbackAndFreshStoreRecovery(t *testing.T) {
	h := newR2(t)
	ctx := context.Background()
	want := r2Paths(h.fixture.source)
	before := h.check("before-failure", want)
	events := h.events()
	h.fixture.mu.Lock()
	delete(h.fixture.source, "/target/file.bin")
	h.fixture.mu.Unlock()
	if _, err := h.st.Pool().Exec(ctx, `CREATE FUNCTION r2_abort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='resource-removed' THEN RAISE EXCEPTION 'R2 rollback probe'; END IF; RETURN NEW; END $$; CREATE TRIGGER r2_abort BEFORE INSERT ON index_journal_event FOR EACH ROW EXECUTE FUNCTION r2_abort()`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ScanScope(ctx, h.root, "/target", 100); err == nil {
		t.Fatal("expected Journal transaction failure")
	}
	if !reflect.DeepEqual(before, h.check("rollback", want)) || !reflect.DeepEqual(events, h.events()) {
		t.Fatal("partial transaction escaped")
	}
	if _, err := h.st.Pool().Exec(ctx, `DROP TRIGGER r2_abort ON index_journal_event; DROP FUNCTION r2_abort()`); err != nil {
		t.Fatal(err)
	}
	// Reopen every client-side persistence/runtime object against retained DB.
	// This is a durable fresh-client recovery test, not a process-kill/DB restart.
	h.st.Pool().Close()
	h.st = postgres.New(testutil.Pool(t))
	h.qr = postgres.NewQueryReader(h.st.Pool())
	h.svc = scan.New(h.st, "", "", time.Second, h.logger)
	h.observe("/target")
	h.observe("/target")
	after := h.check("fresh-store-recovery", r2Paths(h.fixture.source))
	removed := 0
	for _, e := range h.events() {
		if e.EventType == domain.EventResourceRemoved {
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("recovery duplicated removal: %d", removed)
	}
	h.stable([]string{"/target"}, r2Paths(h.fixture.source), after)
}
