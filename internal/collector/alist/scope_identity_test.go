package alist_test

import (
	"context"
	"fmt"
	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMirrorIdentityCheckedBeforeAndAfterList(t *testing.T) {
	for _, test := range []string{"ok", "changed-before", "changed-after", "invalid-name", "duplicate-name", "http-error-success-body"} {
		t.Run(test, func(t *testing.T) {
			var identityCalls, listCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/me" {
					base := "/drive/library"
					n := identityCalls.Add(1)
					if test == "changed-before" || (test == "changed-after" && n == 2) {
						base = "/other"
					}
					fmt.Fprintf(w, `{"code":200,"data":{"base_path":%q}}`, base)
					return
				}
				listCalls.Add(1)
				if test == "http-error-success-body" {
					w.WriteHeader(500)
				}
				switch test {
				case "invalid-name":
					fmt.Fprint(w, `{"code":200,"data":{"content":[{"name":"../outside"}],"total":1}}`)
				case "duplicate-name":
					fmt.Fprint(w, `{"code":200,"data":{"content":[{"name":"a"},{"name":"a"}],"total":2}}`)
				default:
					fmt.Fprint(w, `{"code":200,"data":{"content":null,"total":0}}`)
				}
			}))
			defer srv.Close()
			_, err := (alist.Adapter{BaseURL: srv.URL, ExpectedBasePath: "/drive/library"}).ScanScope(context.Background(), "/", 1000)
			if (err == nil) != (test == "ok") {
				t.Fatalf("case=%s err=%v", test, err)
			}
			if test == "changed-before" && listCalls.Load() != 0 {
				t.Fatal("listed changed namespace")
			}
			if test == "ok" && (listCalls.Load() != 1 || identityCalls.Load() != 2) {
				t.Fatal("coverage/identity calls not bounded")
			}
		})
	}
}
