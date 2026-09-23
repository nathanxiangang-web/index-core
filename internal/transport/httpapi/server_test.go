package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/httpapi"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestHealthAndReadiness(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)

	srv := httpapi.New(httpapi.Deps{Pool: pool, Query: postgres.NewQueryReader(pool), Version: "test"})

	// /healthz is process-alive only.
	if code, _ := get(t, srv, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz must be 200, got %d", code)
	}
	// /readyz fails while the schema is unmigrated.
	if code, body := get(t, srv, "/readyz"); code != http.StatusServiceUnavailable || body["reason"] != "schema_incompatible" {
		t.Fatalf("/readyz must be 503 schema_incompatible before migrate, got %d %v", code, body)
	}

	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if code, _ := get(t, srv, "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz must be 200 after migrate, got %d", code)
	}
	// /v1 is not implemented until P6.
	if code, _ := get(t, srv, "/v1/roots"); code != http.StatusNotImplemented {
		t.Fatalf("/v1 must be 501 at this stage, got %d", code)
	}
}

func get(t *testing.T, srv *http.Server, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}
