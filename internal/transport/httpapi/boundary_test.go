package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/transport/httpapi"
)

// P9: the mutation-hint write route must never appear on the read-only Query
// listener.
func TestP9QueryListenerDoesNotExposeHintRoute(t *testing.T) {
	srv := httpapi.New(httpapi.Deps{})
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/internal/v1/mutation-hints", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Query listener must not expose the hint route, got %d", resp.StatusCode)
	}
}

// G3-R2: the HTTP transport must not receive any raw writable DB capability.
// This API-level test fails if Deps ever regains a pool/store/exec-typed field.
func TestDepsExposeNoWriteCapability(t *testing.T) {
	rt := reflect.TypeOf(httpapi.Deps{})
	banned := []string{"pgxpool", "postgres.Store", "pgx.Conn", "database/sql", "*sql.DB"}
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		ft := field.Type.String()
		for _, b := range banned {
			if strings.Contains(ft, b) {
				t.Fatalf("httpapi.Deps.%s exposes write-capable type %s (Gate 3 G3-R2)", field.Name, ft)
			}
		}
	}
}
