package alist_test

import (
	"context"
	"encoding/json"
	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMirrorTopologyRejectsVirtualOnlySuccess(t *testing.T) {
	for _, scenario := range []string{"direct", "virtual-only", "hidden-virtual", "nested-virtual", "unrelated-mount", "missing-credential", "topology-denied", "truncated-topology", "topology-changed", "storage-unhealthy", "alias-cover", "unknown-driver"} {
		t.Run(scenario, func(t *testing.T) {
			topologyCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/me":
					json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"base_path": "/drive/library"}})
				case "/api/admin/storage/list":
					topologyCalls++
					if r.Method != "GET" || r.Header.Get("Authorization") != "protected-topology" {
						t.Error("topology credential used outside bounded GET")
					}
					if scenario == "topology-denied" {
						w.WriteHeader(403)
						return
					}
					status := "work"
					if scenario == "storage-unhealthy" {
						status = "error"
					}
					driver := "115 Open"
					if scenario == "alias-cover" {
						driver = "Alias"
					}
					if scenario == "unknown-driver" {
						driver = ""
					}
					mounts := []map[string]any{{"id": 1, "mount_path": "/drive", "status": status, "disabled": false, "driver": driver}}
					if scenario == "virtual-only" || scenario == "hidden-virtual" || scenario == "nested-virtual" {
						suffix := "/alias"
						if scenario == "nested-virtual" {
							suffix = "/nested/alias"
						}
						mounts = append(mounts, map[string]any{"id": 2, "mount_path": "/drive/library" + suffix, "status": "work", "disabled": false})
					}
					if scenario == "unrelated-mount" {
						mounts = append(mounts, map[string]any{"id": 2, "mount_path": "/drive/library-other/alias", "status": "work", "disabled": false})
					}
					if scenario == "topology-changed" && topologyCalls > 1 {
						mounts[0]["modified"] = "new-version"
					}
					total := len(mounts)
					if scenario == "truncated-topology" {
						total++
					}
					json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": total, "content": mounts}})
				case "/api/fs/list":
					// This exactly models stock OpenList masking a backend failure. No mount
					// details are present, as in the real server hide_storage_details=true.
					json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "alias", "is_dir": true, "size": 0}}}})
				default:
					t.Error("unexpected request", r.URL.Path)
				}
			}))
			defer server.Close()
			credential := "protected-topology"
			if scenario == "missing-credential" {
				credential = ""
			}
			raw, err := (alist.Adapter{BaseURL: server.URL, ExpectedBasePath: "/drive/library", TopologyToken: credential}).ScanScope(context.Background(), "/", 100)
			if err != nil || len(raw.Entries) != 1 {
				t.Fatalf("positive observation unavailable: %+v %v", raw, err)
			}
			want := scenario == "direct" || scenario == "unrelated-mount"
			if (raw.DirectoryTopologyDigest != "") != want {
				t.Fatalf("unsafe coverage=%q scenario=%s", raw.DirectoryTopologyDigest, scenario)
			}
		})
	}
}
