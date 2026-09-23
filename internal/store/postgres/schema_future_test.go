package postgres_test

import (
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// G3-R7: a database carrying an unknown/future migration must be rejected, so an
// old binary cannot start against a newer schema.
func TestSchemaFutureMigrationDetected(t *testing.T) {
	st, ctx := newStore(t)

	state, err := postgres.SchemaStatus(ctx, st.Pool())
	if err != nil {
		t.Fatalf("schema status: %v", err)
	}
	if !state.Compatible() {
		t.Fatalf("freshly migrated schema must be compatible, got %+v", state)
	}

	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO schema_migrations(version) VALUES ('9999_future.sql')`); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	future, err := postgres.SchemaStatus(ctx, st.Pool())
	if err != nil {
		t.Fatalf("schema status: %v", err)
	}
	if future.Compatible() {
		t.Fatal("a future/unknown migration must make the schema incompatible")
	}
	if len(future.Unexpected) != 1 || future.Unexpected[0] != "9999_future.sql" {
		t.Fatalf("future migration must be reported, got %v", future.Unexpected)
	}
	if err := postgres.SchemaError(future); err == nil {
		t.Fatal("SchemaError must be non-nil for an incompatible schema")
	}
}
