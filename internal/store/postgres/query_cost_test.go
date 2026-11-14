package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestIssue134RuntimeQueryCostsBoundedAndRedacted(t *testing.T) {
	q := postgres.NewQueryCosts()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := q.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: fmt.Sprintf("SELECT %d /* do-not-print */", i), Args: []any{"secret-value"}})
			q.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
		}(i)
	}
	wg.Wait()
	stats := q.Snapshot()
	var calls int64
	for _, s := range stats {
		calls += s.Calls
	}
	raw, _ := json.Marshal(stats)
	if len(stats) > 65 || calls != 100 || strings.Contains(string(raw), "do-not-print") || strings.Contains(string(raw), "secret-value") {
		t.Fatal("unbounded or leaking query tracer", string(raw))
	}
	pool, err := postgres.OpenWithQueryCosts(context.Background(), testutil.TestDSN(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var value string
	if err = pool.QueryRow(context.Background(), `SELECT $1::text`, "secret-value").Scan(&value); err != nil || value != "secret-value" {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), `SELECT 1/0`); err == nil {
		t.Fatal("error fixture succeeded")
	}
	var errors, rows int64
	for _, s := range q.Snapshot() {
		errors += s.Errors
		rows += s.Rows
	}
	if errors != 1 || rows < 1 {
		t.Fatal("actual pgx rows/errors not observed", q.Snapshot())
	}
}
