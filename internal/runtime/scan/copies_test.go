package scan_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Real HTTP adapter -> Kernel -> PostgreSQL -> Q5/Q6/Q8. No media is fetched.
func TestLiveCopiesScopedPipelineRollbackReplayAndTombstones(t *testing.T) {
	st, mock, root := setupScopedRoot(t)
	ctx := context.Background()
	svc := scan.New(st, "", "", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	qr := postgres.NewQueryReader(st.Pool())
	observe := func(scope string) scan.Result {
		t.Helper()
		r, err := svc.ScanScope(ctx, root, scope, 100)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	count := func(sql string) int64 {
		t.Helper()
		var n int64
		if err := st.Pool().QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	resolve := func(path string) string {
		t.Helper()
		r, err := qr.ResolvePath(ctx, root, path, query.ReadOptions{})
		if err != nil || r.Ambiguous || len(r.Matches) != 1 {
			t.Fatalf("Q5 %s: %+v %v", path, r, err)
		}
		return r.Matches[0].ResourceID
	}
	mock.set("/", dirEntry("first"), dirEntry("second"), dirEntry("third"), dirEntry("untouched"))
	observe("/")
	mock.set("/first", fileEntry("original.txt", 10, "identical"))
	observe("/first")
	mock.set("/untouched", fileEntry("keep.txt", 20, "unrelated"))
	observe("/untouched")
	originalID, untouchedID := resolve("/first/original.txt"), resolve("/untouched/keep.txt")
	baselineGen := count("SELECT current_generation FROM index_root")
	baselineEvents := count("SELECT count(*) FROM index_journal_event")
	mock.set("/second", fileEntry("copy.txt", 10, "identical"))
	// Journal failure must also roll back the new copy and generation.
	if _, err := st.Pool().Exec(ctx, `CREATE FUNCTION copy_abort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='resource-added' THEN RAISE EXCEPTION 'copy rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER copy_abort BEFORE INSERT ON index_journal_event FOR EACH ROW EXECUTE FUNCTION copy_abort()`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ScanScope(ctx, root, "/second", 100); err == nil {
		t.Fatal("expected journal failure")
	}
	if count("SELECT current_generation FROM index_root") != baselineGen || count("SELECT count(*) FROM index_journal_event") != baselineEvents || activePaths(t, qr, root)["/second/copy.txt"] {
		t.Fatal("copy transaction escaped rollback")
	}
	if _, err := st.Pool().Exec(ctx, `DROP TRIGGER copy_abort ON index_journal_event; DROP FUNCTION copy_abort()`); err != nil {
		t.Fatal(err)
	}
	// Drain failed head using the existing recovery path; retry fresh observation.
	_, _ = svc.ScanScope(ctx, root, "/second", 100)
	observe("/second")
	secondID := resolve("/second/copy.txt")
	mock.set("/third", fileEntry("copy-again.txt", 10, "identical"))
	observe("/third")
	thirdID := resolve("/third/copy-again.txt")
	if originalID == secondID || originalID == thirdID || secondID == thirdID {
		t.Fatal("distinct copies merged")
	}
	gen, events := count("SELECT current_generation FROM index_root"), count("SELECT count(*) FROM index_journal_event")
	for _, scope := range []string{"/first", "/second", "/third"} {
		observe(scope)
		observe(scope)
	}
	if count("SELECT current_generation FROM index_root") != gen || count("SELECT count(*) FROM index_journal_event") != events {
		t.Fatal("copy replay mutated generation or journal")
	}
	if n := len(activePaths(t, qr, root)); n != 8 {
		t.Fatalf("Q6 missing copies: %d", n)
	}
	// Ordinary PARTIAL omission still cannot delete anything.
	mock.set("/second")
	observe("/second")
	if resolve("/second/copy.txt") != secondID {
		t.Fatal("PARTIAL omission removed copy")
	}
	// Independent trusted coverage in this disposable fixture authorizes deletion.
	// Production remains positive-only; no topology credential is added there.
	t.Setenv("COPY_TOPOLOGY", "isolated-test-only")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			io.WriteString(w, `{"code":200,"data":{"base_path":"/drive/library"}}`)
		case "/api/admin/storage/list":
			io.WriteString(w, `{"code":200,"data":{"total":1,"content":[{"id":1,"mount_path":"/drive","driver":"115 Open","status":"work","disabled":false}]}}`)
		default:
			mock.handler()(w, r)
		}
	}))
	defer server.Close()
	config, _ := json.Marshal(map[string]any{"base_url": server.URL, "path": "/", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_token_env": "COPY_TOPOLOGY"})
	if err := st.UpsertAdapterConfig(ctx, root, postgres.AdapterConfig{CollectorKind: "openlist", Config: config}); err != nil {
		t.Fatal(err)
	}
	observe("/second")
	var presence string
	if err := st.Pool().QueryRow(ctx, `SELECT resource_presence FROM index_canonical_resource WHERE resource_id=$1::uuid`, secondID).Scan(&presence); err != nil || presence != "REMOVED" {
		t.Fatalf("copy not tombstoned: %s %v", presence, err)
	}
	if resolve("/first/original.txt") != originalID || resolve("/third/copy-again.txt") != thirdID || resolve("/untouched/keep.txt") != untouchedID {
		t.Fatal("deleting one copy changed other identities")
	}
	mock.set("/second", fileEntry("copy.txt", 10, "identical"))
	observe("/second")
	if resolve("/second/copy.txt") == secondID {
		t.Fatal("resurrected tombstone instead of adding fresh copy")
	}
	if count("SELECT count(*) FROM index_journal_event WHERE event_type='resource-removed'") != 1 {
		t.Fatal("unrelated removal event")
	}
	journal, err := qr.ReadJournal(ctx, root, 0, 100)
	if err != nil || len(journal) != int(count("SELECT count(*) FROM index_journal_event")) {
		t.Fatalf("Q8 mismatch: %v", err)
	}
}
