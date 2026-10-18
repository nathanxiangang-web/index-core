package scan_test

import (
	"context"
	"encoding/json"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestMirrorDeletedDirectoryAtomicJournalAndReplay(t *testing.T) {
	st, mock, rootID := setupScopedRoot(t)
	ctx := context.Background()
	t.Setenv("MIRROR_TOPOLOGY_TOKEN", "test-topology-token")
	var badIdentity, failList, truncated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/admin/storage/list" {
			io.WriteString(w, `{"code":200,"data":{"total":1,"content":[{"id":1,"mount_path":"/drive","driver":"115 Open","status":"work","disabled":false}]}}`)
			return
		}
		if r.URL.Path == "/api/me" {
			base := "/drive/library"
			if badIdentity.Load() {
				base = "/wrong"
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"base_path": base}})
			return
		}
		if failList.Load() {
			w.WriteHeader(503)
			return
		}
		if truncated.Load() {
			io.WriteString(w, `{"code":200,"data":{"content":[],"total":3}}`)
			return
		}
		mock.handler()(w, r)
	}))
	defer server.Close()
	config, _ := json.Marshal(map[string]any{"base_url": server.URL, "path": "/", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_token_env": "MIRROR_TOPOLOGY_TOKEN"})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "openlist", Config: config}); err != nil {
		t.Fatal(err)
	}
	svc := scan.New(st, "", "", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	observe := func(scope string) scan.Result {
		t.Helper()
		res, err := svc.ScanScope(ctx, rootID, scope, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	mock.set("/", dirEntry("gallery"), dirEntry("gallery-other"))
	observe("/")
	mock.set("/gallery", fileEntry("1d.jpg", 10, "abc"))
	observe("/gallery")
	mock.set("/gallery-other", fileEntry("keep.jpg", 20, "def"))
	observe("/gallery-other")
	changedDirectory := dirEntry("gallery-other")
	changedDirectory["modified"] = "2026-01-02T04:04:05Z"
	mock.set("/", changedDirectory)
	// Timeout/HTTP failure, truncation and namespace mismatch cannot remove.
	for _, kind := range []string{"identity", "http", "truncated"} {
		badIdentity.Store(kind == "identity")
		failList.Store(kind == "http")
		truncated.Store(kind == "truncated")
		if _, err := svc.ScanScope(ctx, rootID, "/", 1000); err == nil {
			t.Fatalf("%s accepted", kind)
		}
		var n int
		st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE canonical_path='/gallery/1d.jpg' AND resource_presence='PRESENT'`).Scan(&n)
		if n != 1 {
			t.Fatal("failure removed image")
		}
	}
	badIdentity.Store(false)
	failList.Store(false)
	truncated.Store(false)
	// Force Journal failure: generation and all tombstones must roll back.
	_, err := st.Pool().Exec(ctx, `CREATE FUNCTION mirror_abort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='resource-removed' THEN RAISE EXCEPTION 'rollback probe'; END IF; RETURN NEW; END $$; CREATE TRIGGER mirror_abort BEFORE INSERT ON index_journal_event FOR EACH ROW EXECUTE FUNCTION mirror_abort()`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ScanScope(ctx, rootID, "/", 1000)
	if err == nil {
		t.Fatal("expected rollback")
	}
	var alive int
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE canonical_path LIKE '/gallery/%' AND resource_presence='PRESENT'`).Scan(&alive)
	if alive != 1 {
		t.Fatal("partial removal escaped failed journal transaction")
	}
	st.Pool().Exec(ctx, `DROP TRIGGER mirror_abort ON index_journal_event; DROP FUNCTION mirror_abort()`)
	// Recovery drains the old pending snapshot; a new observation then replays safely.
	// Recovery may fence out the fresh observation collected before the old head
	// committed. It must retry with a new observation, never bypass the fence.
	svc.ScanScope(ctx, rootID, "/", 1000)
	observe("/")
	observe("/")
	var removed, kept, events int
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE canonical_path IN ('/gallery','/gallery/1d.jpg') AND resource_presence='REMOVED'`).Scan(&removed)
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE canonical_path='/gallery-other/keep.jpg' AND resource_presence='PRESENT'`).Scan(&kept)
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_journal_event WHERE event_type='resource-removed'`).Scan(&events)
	if removed != 2 || kept != 1 || events != 2 {
		t.Fatalf("removed=%d kept=%d events=%d", removed, kept, events)
	}
	// Existing directory, one file deleted: immediate bounded confirmation.
	mock.set("/gallery-other")
	observe("/gallery-other")
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE canonical_path='/gallery-other/keep.jpg' AND resource_presence='REMOVED'`).Scan(&removed)
	if removed != 1 {
		t.Fatal("empty directory did not converge")
	}
}
