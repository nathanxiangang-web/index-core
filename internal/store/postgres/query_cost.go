package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryCosts is a bounded pgx QueryTracer. It contains no SQL, arguments,
// responses, error text or DSN. Elapsed includes row consumption, not pool
// acquisition, CPU time, EXPLAIN time or untraced CopyFrom/Batch operations.
type QueryCosts struct {
	mu    sync.Mutex
	stats map[string]QueryCost
}

type QueryCost struct {
	Fingerprint                        string `json:"fingerprint"`
	Class                              string `json:"class"`
	Calls, Errors, Rows                int64
	TotalMicroseconds, MaxMicroseconds int64
}

type queryCostKey struct{}
type queryStarted struct {
	at                 time.Time
	fingerprint, class string
}

func NewQueryCosts() *QueryCosts { return &QueryCosts{stats: map[string]QueryCost{}} }

func queryClass(sql string) string {
	if strings.Contains(sql, "indexcore:mirror_") {
		return "mirror_policy"
	}
	work := strings.Contains(sql, "index_dirty_scope_work")
	canonical := strings.Contains(sql, "index_canonical_resource")
	if work && canonical {
		return "work_canonical"
	}
	if work {
		return "work"
	}
	if canonical {
		return "canonical"
	}
	if strings.Contains(sql, "index_scope_watch_state") {
		return "watch"
	}
	if strings.Contains(sql, "index_snapshot") || strings.Contains(sql, "index_admission") {
		return "snapshot_admission"
	}
	if strings.Contains(sql, "index_journal_event") {
		return "journal"
	}
	if strings.Contains(sql, "index_root") {
		return "root"
	}
	return "other"
}

func (q *QueryCosts) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	sum := sha256.Sum256([]byte(data.SQL))
	return context.WithValue(ctx, queryCostKey{}, queryStarted{time.Now(), hex.EncodeToString(sum[:8]), queryClass(data.SQL)})
}

func (q *QueryCosts) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryCostKey{}).(queryStarted)
	if !ok {
		return
	}
	d := time.Since(start.at).Microseconds()
	q.mu.Lock()
	defer q.mu.Unlock()
	key := start.fingerprint
	if _, found := q.stats[key]; !found && len(q.stats) >= 64 {
		key = "OVERFLOW"
		start.class = "other"
	}
	s := q.stats[key]
	s.Fingerprint = key
	s.Class = start.class
	s.Calls++
	s.Rows += data.CommandTag.RowsAffected()
	s.TotalMicroseconds += d
	if data.Err != nil {
		s.Errors++
	}
	if d > s.MaxMicroseconds {
		s.MaxMicroseconds = d
	}
	q.stats[key] = s
}

func (q *QueryCosts) Snapshot() []QueryCost {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]QueryCost, 0, len(q.stats))
	for _, s := range q.stats {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

func OpenWithQueryCosts(ctx context.Context, dsn string, q *QueryCosts) (*pgxpool.Pool, error) {
	if q == nil {
		return Open(ctx, dsn)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.Tracer = q
	return pgxpool.NewWithConfig(ctx, cfg)
}
