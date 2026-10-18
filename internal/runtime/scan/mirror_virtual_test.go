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

func TestMirrorVirtualOnlySuccessNeverRemovesUnrelatedInventory(t *testing.T) {
	st, mock, id := setupScopedRoot(t)
	ctx := context.Background()
	t.Setenv("TOPOLOGY_KEY", "test")
	var broken atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/me" {
			io.WriteString(w, `{"code":200,"data":{"base_path":"/drive/library"}}`)
			return
		}
		if r.URL.Path == "/api/admin/storage/list" {
			mounts := []map[string]any{{"id": 1, "mount_path": "/drive", "status": "work", "disabled": false}}
			if broken.Load() {
				mounts = append(mounts, map[string]any{"id": 2, "mount_path": "/drive/library/alias", "status": "work", "disabled": false})
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": mounts, "total": len(mounts)}})
			return
		}
		mock.handler()(w, r)
	}))
	defer server.Close()
	config, _ := json.Marshal(map[string]any{"base_url": server.URL, "path": "/", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_token_env": "TOPOLOGY_KEY"})
	if err := st.UpsertAdapterConfig(ctx, id, postgres.AdapterConfig{CollectorKind: "openlist", Config: config}); err != nil {
		t.Fatal(err)
	}
	svc := scan.New(st, "", "", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, scope := range []string{"/", "/gallery", "/software"} {
		if scope == "/" {
			mock.set(scope, dirEntry("gallery"), dirEntry("software"))
		} else {
			mock.set(scope, fileEntry("keep.txt", 10, ""))
		}
		if _, err := svc.ScanScope(ctx, id, scope, 100); err != nil {
			t.Fatal(err)
		}
	}
	broken.Store(true)
	mock.set("/", dirEntry("alias"))
	for i := 0; i < 3; i++ {
		if _, err := svc.ScanScope(ctx, id, "/", 100); err != nil {
			t.Fatal(err)
		}
	}
	var alive, removed int
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE canonical_path IN ('/gallery','/gallery/keep.txt','/software','/software/keep.txt') AND resource_presence='PRESENT'`).Scan(&alive)
	st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_journal_event WHERE event_type='resource-removed'`).Scan(&removed)
	if alive != 4 || removed != 0 {
		t.Fatalf("virtual-only response damaged inventory: alive=%d removed=%d", alive, removed)
	}
	// Source recovers: normal positive observations remain available; the same
	// resource coordinates remain PRESENT. No reset/resurrection SQL is needed.
	broken.Store(false)
	mock.set("/", dirEntry("gallery"), dirEntry("software"), dirEntry("alias"))
	if _, err := svc.ScanScope(ctx, id, "/", 100); err != nil {
		t.Fatal(err)
	}
}
