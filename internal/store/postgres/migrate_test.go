package postgres_test

import (
	"context"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestMigrateFromEmptyDatabaseIsReproducibleAndIdempotent(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate from empty: %v", err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate idempotent rerun: %v", err)
	}

	var tables int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		  WHERE table_schema = 'public' AND table_name LIKE 'index\_%'`).Scan(&tables); err != nil {
		t.Fatalf("count index_ tables: %v", err)
	}
	if tables < 12 {
		t.Fatalf("expected >= 12 index_ tables, got %d", tables)
	}
}

func TestJournalAndEvidenceAreAppendOnly(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const rootID = "00000000-0000-0000-0000-000000000001"
	if _, err := pool.Exec(ctx,
		`INSERT INTO index_root(root_id, scope_descriptor, lifecycle_state)
		 VALUES ($1, '{}'::jsonb, 'ACTIVE')`, rootID); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO index_journal_event(root_id, event_seq, generation_number,
		        intra_generation_seq, event_type, payload)
		 VALUES ($1, 1, 1, 1, 'resource-added', '{}'::jsonb)`, rootID); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE index_journal_event SET event_type = 'resource-updated'
		  WHERE root_id = $1 AND event_seq = 1`, rootID); err == nil {
		t.Fatal("expected append-only trigger to reject UPDATE on index_journal_event")
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM index_journal_event WHERE root_id = $1 AND event_seq = 1`, rootID); err == nil {
		t.Fatal("expected append-only trigger to reject DELETE on index_journal_event")
	}

	row := pool.QueryRow(ctx,
		`SELECT count(*) FROM index_journal_event WHERE root_id = $1 AND event_seq = 1`, rootID)
	var n int
	if err := row.Scan(&n); err != nil || n != 1 {
		t.Fatalf("original journal event must survive rejected mutation: n=%d err=%v", n, err)
	}
}

func TestPathUniquenessHasNoFakeConstraint(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// C-C3 is REJECTED: two PRESENT rows MAY share the same canonical_path (R8 overlap).
	const rootID = "00000000-0000-0000-0000-000000000002"
	if _, err := pool.Exec(ctx,
		`INSERT INTO index_root(root_id, scope_descriptor, lifecycle_state)
		 VALUES ($1, '{}'::jsonb, 'ACTIVE')`, rootID); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	insert := `INSERT INTO index_canonical_resource(resource_id, root_id, introduced_at_generation,
	               last_confirmed_generation, resource_presence, removal_evidence_state, canonical_path)
	           VALUES ($1, $2, 0, 0, 'PRESENT', 'NONE', '/a/b.txt')`
	if _, err := pool.Exec(ctx, insert, "00000000-0000-0000-0000-0000000000a1", rootID); err != nil {
		t.Fatalf("insert first resource: %v", err)
	}
	if _, err := pool.Exec(ctx, insert, "00000000-0000-0000-0000-0000000000a2", rootID); err != nil {
		t.Fatalf("second PRESENT row with same path must be allowed (C-C3a): %v", err)
	}
}
