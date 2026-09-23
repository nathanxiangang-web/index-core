package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/httpapi"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestQueryTransportEndpoints(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	const rootID = "f0000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(context.Background(), st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}

	srv := httpapi.New(httpapi.Deps{Query: postgres.NewQueryReader(pool), Readiness: postgres.NewReadiness(pool), Version: "test"})

	// GET /v1/roots returns the active root.
	code, body := do(t, srv, http.MethodGet, "/v1/roots", nil)
	if code != http.StatusOK {
		t.Fatalf("/v1/roots must be 200, got %d", code)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 root, got %d", len(items))
	}

	// Q1 get_root.
	if code, _ := do(t, srv, http.MethodGet, "/v1/roots/"+rootID, nil); code != http.StatusOK {
		t.Fatalf("get_root must be 200, got %d", code)
	}
	// Q9 root status.
	if code, _ := do(t, srv, http.MethodGet, "/v1/roots/"+rootID+"/status", nil); code != http.StatusOK {
		t.Fatalf("root status must be 200, got %d", code)
	}

	// Unknown root -> 404.
	if code, _ := do(t, srv, http.MethodGet, "/v1/roots/f0000000-0000-0000-0000-0000000000ff", nil); code != http.StatusNotFound {
		t.Fatalf("unknown root must be 404, got %d", code)
	}
	// Invalid cursor -> 400 invalid_cursor.
	code, body = do(t, srv, http.MethodGet, "/v1/roots/"+rootID+"/active?cursor=not-base64!!", nil)
	if code != http.StatusBadRequest || body["error"] != "invalid_cursor" {
		t.Fatalf("invalid cursor must be 400 invalid_cursor, got %d %v", code, body)
	}
	// No canonical mutation endpoint: POST is not routed -> 405.
	if code, _ := do(t, srv, http.MethodPost, "/v1/roots", nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /v1/roots must be 405, got %d", code)
	}
}

func do(t *testing.T, srv *http.Server, method, path string, _ []byte) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}
