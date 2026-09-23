package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SchemaState reports schema compatibility for a binary.
type SchemaState struct {
	Applied    int
	Required   int
	Missing    []string // required migrations not applied
	Unexpected []string // applied migrations this binary does not know (future schema)
}

// Compatible reports whether the database schema is exactly compatible with this
// binary: all required migrations applied AND no unknown/future migrations
// (Gate 3 G3-R7). An old binary must not start against a newer schema.
func (s SchemaState) Compatible() bool {
	return len(s.Missing) == 0 && len(s.Unexpected) == 0
}

// RequiredMigrations returns the embedded migration filenames in apply order.
func RequiredMigrations() ([]string, error) {
	return migrationNames()
}

// SchemaStatus reports applied/required migrations, missing required versions,
// and unexpected (future) applied versions. It never mutates schema.
func SchemaStatus(ctx context.Context, pool *pgxpool.Pool) (SchemaState, error) {
	names, err := migrationNames()
	if err != nil {
		return SchemaState{}, err
	}
	state := SchemaState{Required: len(names)}

	known := make(map[string]bool, len(names))
	for _, n := range names {
		known[n] = true
	}

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables
		                WHERE table_schema = 'public' AND table_name = 'schema_migrations')`).Scan(&exists); err != nil {
		return SchemaState{}, err
	}
	if !exists {
		state.Missing = append(state.Missing, names...)
		return state, nil
	}

	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return SchemaState{}, err
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return SchemaState{}, err
		}
		present[v] = true
		if !known[v] {
			state.Unexpected = append(state.Unexpected, v)
		}
	}
	if err := rows.Err(); err != nil {
		return SchemaState{}, err
	}
	for _, n := range names {
		if present[n] {
			state.Applied++
		} else {
			state.Missing = append(state.Missing, n)
		}
	}
	return state, nil
}

// SchemaError renders an incompatible schema as an actionable error.
func SchemaError(s SchemaState) error {
	return fmt.Errorf("schema incompatible: applied %d/%d, missing %v, unexpected %v; run `indexcore migrate` with a matching binary",
		s.Applied, s.Required, s.Missing, s.Unexpected)
}
