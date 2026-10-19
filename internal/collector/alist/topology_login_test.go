package alist_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
)

func TestMirrorTopologyRenewableLoginIsIsolatedAndFailClosed(t *testing.T) {
	for _, scenario := range []string{"renew", "denied", "partial-credential", "login-redirect", "topology-redirect"} {
		t.Run(scenario, func(t *testing.T) {
			logins, topologyCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/auth/login":
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["username"] != "topology-user" || body["password"] != "private-topology-password" || r.Header.Get("Authorization") != "" {
						t.Error("topology login leaked source token or changed credentials")
					}
					logins++
					if scenario == "login-redirect" {
						http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
						return
					}
					if scenario == "denied" {
						fmt.Fprint(w, `{"code":403}`)
						return
					}
					fmt.Fprintf(w, `{"code":200,"data":{"token":"topology-%d"}}`, logins)
				case "/api/admin/storage/list":
					topologyCalls++
					if scenario == "topology-redirect" {
						http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
						return
					}
					if r.Method != "GET" || r.Header.Get("Authorization") != fmt.Sprintf("topology-%d", logins) {
						t.Error("wrong topology token or method")
					}
					fmt.Fprint(w, `{"code":200,"data":{"total":1,"content":[{"id":1,"mount_path":"/drive","status":"work","driver":"115 Open","disabled":false}]}}`)
				case "/api/me", "/api/fs/list":
					if r.Header.Get("Authorization") != "source-token" {
						t.Error("privileged topology credential leaked to source endpoint")
					}
					if r.URL.Path == "/api/me" {
						fmt.Fprint(w, `{"code":200,"data":{"base_path":"/drive/library"}}`)
					} else {
						fmt.Fprint(w, `{"code":200,"data":{"content":[],"total":0}}`)
					}
				default:
					t.Error("unexpected endpoint", r.URL.Path)
				}
			}))
			defer server.Close()
			password := "private-topology-password"
			if scenario == "partial-credential" {
				password = ""
			}
			adapter := alist.Adapter{BaseURL: server.URL, Token: "source-token", ExpectedBasePath: "/drive/library", TopologyUsername: "topology-user", TopologyPassword: password}
			for i := 0; i < 2; i++ {
				raw, err := adapter.ScanScope(context.Background(), "/", 100)
				if err != nil || (raw.DirectoryTopologyDigest != "") != (scenario == "renew") {
					t.Fatalf("wrong authority for %s: %q %v", scenario, raw.DirectoryTopologyDigest, err)
				}
			}
			if scenario == "renew" && (logins != 2 || topologyCalls != 4) {
				t.Fatalf("session not renewed per observation: logins=%d topology=%d", logins, topologyCalls)
			}
			if scenario != "renew" && scenario != "topology-redirect" && topologyCalls != 0 {
				t.Fatal("failed credentials minted topology requests")
			}
			if scenario == "partial-credential" && logins != 0 {
				t.Fatal("partial credentials used")
			}
		})
	}
}
