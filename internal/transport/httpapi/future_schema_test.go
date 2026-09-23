package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/httpapi"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// G3-R7: /readyz fails closed when the database has a future/unknown migration.
func TestReadyzRejectsFutureSchema(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO schema_migrations(version) VALUES ('9999_future.sql')`); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	srv := httpapi.New(httpapi.Deps{
		Query: postgres.NewQueryReader(pool), Readiness: postgres.NewReadiness(pool), Version: "test",
	})
	code, body := do(t, srv, http.MethodGet, "/readyz", nil)
	if code != http.StatusServiceUnavailable || body["reason"] != "schema_incompatible" {
		t.Fatalf("/readyz must be 503 schema_incompatible for a future schema, got %d %v", code, body)
	}
}
