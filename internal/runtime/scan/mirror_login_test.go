package scan_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func TestMirrorRenewableCredentialConfigCanCommitBoundedDeletion(t *testing.T) {
	st, mock, root := setupScopedRoot(t)
	ctx := context.Background()
	t.Setenv("TEST_TOPO_USER", "topology")
	t.Setenv("TEST_TOPO_PASS", "private")
	logins := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			logins++
			fmt.Fprint(w, `{"code":200,"data":{"token":"topology-session"}}`)
		case "/api/me":
			fmt.Fprint(w, `{"code":200,"data":{"base_path":"/drive/library"}}`)
		case "/api/admin/storage/list":
			fmt.Fprint(w, `{"code":200,"data":{"total":1,"content":[{"id":1,"mount_path":"/drive","driver":"115 Open","status":"work","disabled":false}]}}`)
		default:
			mock.handler()(w, r)
		}
	}))
	defer server.Close()
	cfg, _ := json.Marshal(map[string]any{"base_url": server.URL, "path": "/", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_username_env": "TEST_TOPO_USER", "topology_password_env": "TEST_TOPO_PASS"})
	if err := st.UpsertAdapterConfig(ctx, root, postgres.AdapterConfig{CollectorKind: "openlist", Config: cfg}); err != nil {
		t.Fatal(err)
	}
	svc := scan.New(st, "", "", 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	observe := func(p string) {
		t.Helper()
		if _, err := svc.ScanScope(ctx, root, p, 100); err != nil {
			t.Fatal(err)
		}
	}
	mock.set("/", dirEntry("gone"), dirEntry("kept"))
	observe("/")
	mock.set("/gone", fileEntry("deleted.mp4", 10, "one"))
	observe("/gone")
	mock.set("/kept", fileEntry("keep.txt", 20, "two"))
	observe("/kept")
	mock.set("/", dirEntry("kept"))
	observe("/")
	var removed, kept int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE resource_presence='REMOVED'`).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE canonical_path='/kept/keep.txt' AND resource_presence='PRESENT'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if removed != 2 || kept != 1 || logins != 4 {
		t.Fatalf("removed=%d kept=%d renewable logins=%d", removed, kept, logins)
	}
	// A partial credential pair cannot authorize a forged coverage proof; the
	// runtime itself no longer mints one, and ordinary omission stays additive.
	deleteConfig, _ := json.Marshal(map[string]any{"base_url": server.URL, "path": "/", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_username_env": "TEST_TOPO_USER"})
	if err := st.UpsertAdapterConfig(ctx, root, postgres.AdapterConfig{CollectorKind: "openlist", Config: deleteConfig}); err != nil {
		t.Fatal(err)
	}
	mock.set("/")
	observe("/")
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_canonical_resource WHERE canonical_path='/kept/keep.txt' AND resource_presence='PRESENT'`).Scan(&kept); err != nil || kept != 1 {
		t.Fatal("missing credential deleted sibling", err)
	}
}
