package scan_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/runtime/worker"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Driver tracing is test-local. It pauses AFTER a real COMMIT (locks released),
// not a production failpoint or a fake scanner/coordinator. Channels establish
// the interleaving; no scheduler-probability or sleep chooses the winner.
type handoffTrace struct {
	armed   atomic.Bool
	mu      sync.Mutex
	marked  map[*pgx.Conn]bool
	match   string
	paused  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *handoffTrace) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if h.armed.Load() && strings.Contains(d.SQL, h.match) {
		h.mu.Lock()
		h.marked[c] = true
		h.mu.Unlock()
	}
	return ctx
}
func (h *handoffTrace) TraceQueryEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryEndData) {
	if !h.armed.Load() || d.CommandTag.String() != "COMMIT" || d.Err != nil {
		return
	}
	h.mu.Lock()
	marked := h.marked[c]
	delete(h.marked, c)
	h.mu.Unlock()
	if marked {
		h.once.Do(func() {
			h.armed.Store(false)
			close(h.paused)
			select {
			case <-h.release:
			case <-ctx.Done():
			}
		})
	}
}

type pendingReadTrace struct {
	seen chan struct{}
	once sync.Once
}
type pendingReadKey struct{}

func (p *pendingReadTrace) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, pendingReadKey{}, strings.Contains(d.SQL, "SELECT DISTINCT root_id::text FROM index_admission"))
}
func (p *pendingReadTrace) TraceQueryEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryEndData) {
	// Specifically observe PendingRoots, not the pool's initial identity check.
	if d.Err == nil && ctx.Value(pendingReadKey{}) == true {
		p.once.Do(func() { close(p.seen) })
	}
}

type workerReceiptHandler struct {
	slog.Handler
	done chan struct{}
	once *sync.Once
}

func (h workerReceiptHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.Handler.Handle(ctx, r)
	if r.Message == "worker: admission processed" {
		h.once.Do(func() { close(h.done) })
	}
	return err
}

func TestIssue130RealScanWorkerHandoff(t *testing.T) {
	cases := []struct {
		name, match string
		workerWins  bool
	}{
		{"worker before admission lookup", "INSERT INTO index_admission(root_id, admission_seq, snapshot_id, status)", true},
		{"worker after admission lookup", "SELECT admission_seq FROM index_admission", true},
		{"synchronous scan wins", "INSERT INTO index_reconcile_result", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			trace := &handoffTrace{marked: map[*pgx.Conn]bool{}, match: tc.match, paused: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			unpause := func() { release.Do(func() { close(trace.release) }) }
			defer unpause()
			pool := testutil.Issue130Pool(t, func(c *pgxpool.Config) { c.ConnConfig.Tracer = trace })
			testutil.ResetSchema(t, pool)
			if err := postgres.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			st := postgres.New(pool)
			root := reconcileIDForHandoff(t, st, ctx)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"code":200,"data":{"content":[{"name":"alive.txt","is_dir":false,"size":10,"modified":"2026-01-01T00:00:00Z","hash_info":{"sha1":"safe-synthetic-hash"}}],"total":1}}`)
			}))
			defer srv.Close()
			if err := st.UpsertAdapterConfig(ctx, root, postgres.AdapterConfig{CollectorKind: "alist", Config: []byte(fmt.Sprintf(`{"base_url":%q,"path":"/"}`, srv.URL))}); err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			processed := make(chan struct{})
			var processedOnce sync.Once
			handler := workerReceiptHandler{Handler: slog.NewJSONHandler(&log, nil), done: processed, once: &processedOnce}
			logger := slog.New(handler)
			svc := scan.New(st, "", "", 5*time.Second, logger)
			if _, err := svc.ScanScope(ctx, root, "/", 10); err != nil {
				t.Fatal(err)
			}
			var original string
			var before int
			var journalBefore string
			if err := pool.QueryRow(ctx, `SELECT resource_id::text FROM index_canonical_resource WHERE root_id=$1 AND resource_presence='PRESENT'`, root).Scan(&original); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM index_journal_event`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT md5(string_agg(row_to_json(j)::text,'|' ORDER BY root_id,event_seq)) FROM index_journal_event j`).Scan(&journalBefore); err != nil {
				t.Fatal(err)
			}
			if _, err := st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify, Priority: state.PriorityNormal, SeenAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			one, err := incrementalexec.New(st, svc, incrementalexec.Config{MaxEntriesPerScope: 10, Retry: incrementalexec.RetryPolicy{TransientProvider: time.Minute, Throttled: time.Minute, Internal: time.Minute}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			cycle, err := incrementalexec.NewCycleRunner(one)
			if err != nil {
				t.Fatal(err)
			}
			orch, err := incrementalorch.NewRunner(st, cycle, nil)
			if err != nil {
				t.Fatal(err)
			}
			rt, err := incrementalruntime.New(st, orch, incrementalruntime.Config{WakeInterval: time.Second, MaxRetryPromotions: 1, Cycle: incrementalruntime.DefaultCycleConfig()}, logger, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Model serve's single writer owner for BOTH in-process orchestration paths.
			lock, err := st.AcquireWriterLock(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Release(context.Background())
			trace.armed.Store(true)
			runCtx, stop := context.WithCancel(ctx)
			defer stop()
			runtimeDone := make(chan error, 1)
			go func() { runtimeDone <- rt.Run(runCtx) }()
			select {
			case <-trace.paused:
			case err := <-runtimeDone:
				t.Fatalf("runtime ended before boundary: %v", err)
			case <-ctx.Done():
				t.Fatal("boundary not reached")
			}
			pending := &pendingReadTrace{seen: make(chan struct{})}
			wp := testutil.Issue130Pool(t, func(c *pgxpool.Config) { c.ConnConfig.Tracer = pending })
			ws := postgres.New(wp)
			workerDone := make(chan error, 1)
			go func() {
				workerDone <- worker.NewWithIntervals(ws, 1, time.Millisecond, time.Millisecond, logger).Run(runCtx)
			}()
			if tc.workerWins {
				select {
				case <-processed:
				case <-ctx.Done():
					t.Fatal("background worker did not complete real admission")
				}
			} else {
				select {
				case <-pending.seen:
				case <-ctx.Done():
					t.Fatal("worker did not start")
				}
			}
			unpause()
			// Observe a durable condition; polling does not decide the race ordering.
			verified := func(minCalls int32) {
				t.Helper()
				for {
					var state string
					err := pool.QueryRow(ctx, `SELECT work_state FROM index_dirty_scope_work WHERE root_id=$1 AND scope_key='/'`, root).Scan(&state)
					if err != nil {
						t.Fatal(err)
					}
					if state == "VERIFIED" && calls.Load() >= minCalls {
						return
					}
					select {
					case err := <-runtimeDone:
						t.Fatalf("unexpected runtime fatal: %v", err)
					case <-ctx.Done():
						t.Fatal("work not verified")
					case <-time.After(2 * time.Millisecond):
					}
				}
			}
			verified(2)
			if _, err := st.MergeSignal(ctx, state.DirtySignal{RootID: root, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify, Priority: state.PriorityNormal, SeenAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			rt.Wake()
			verified(3)
			stop()
			if err := <-runtimeDone; err != nil {
				t.Fatalf("runtime did not stay alive: %v", err)
			}
			if err := <-workerDone; err != nil {
				t.Fatal(err)
			}
			var events, present, bad int
			var journalAfter string
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM index_journal_event),(SELECT count(*) FROM index_canonical_resource WHERE resource_id=$1 AND resource_presence='PRESENT'),(SELECT count(*) FROM index_dirty_scope_work WHERE last_error_class='INTERNAL' OR work_state='RETRY_WAIT')`, original).Scan(&events, &present, &bad); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT md5(string_agg(row_to_json(j)::text,'|' ORDER BY root_id,event_seq)) FROM index_journal_event j`).Scan(&journalAfter); err != nil {
				t.Fatal(err)
			}
			if events != before || journalAfter != journalBefore || present != 1 || bad != 0 || calls.Load() != 3 || strings.Contains(log.String(), "incremental_runtime_fatal") {
				t.Fatalf("handoff/regression failure: events=%d/%d present=%d bad=%d calls=%d log=%s", events, before, present, bad, calls.Load(), log.String())
			}
		})
	}
}

func reconcileIDForHandoff(t *testing.T, st *postgres.Store, ctx context.Context) string {
	t.Helper()
	root := "13000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), root, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertRootPolicy(ctx, root, postgres.RootPolicy{RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour, MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}); err != nil {
		t.Fatal(err)
	}
	return root
}
