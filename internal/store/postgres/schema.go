package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RequiredMigrations returns the embedded migration filenames in apply order.
func RequiredMigrations() ([]string, error) {
	return migrationNames()
}

// SchemaStatus reports how many required migrations are applied and which are
// missing. It never mutates schema; serve/readiness use it to reject an
// incompatible database instead of auto-migrating (Gate 3 P2).
func SchemaStatus(ctx context.Context, pool *pgxpool.Pool) (applied int, required int, missing []string, err error) {
	names, err := migrationNames()
	if err != nil {
		return 0, 0, nil, err
	}
	required = len(names)

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM information_schema.tables
		                WHERE table_schema = 'public' AND table_name = 'schema_migrations')`).Scan(&exists); err != nil {
		return 0, required, nil, err
	}
	if !exists {
		return 0, required, names, nil
	}

	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return 0, required, nil, err
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return 0, required, nil, err
		}
		present[v] = true
	}
	if err := rows.Err(); err != nil {
		return 0, required, nil, err
	}
	for _, n := range names {
		if present[n] {
			applied++
		} else {
			missing = append(missing, n)
		}
	}
	return applied, required, missing, nil
}
