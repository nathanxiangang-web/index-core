package postgres_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/internal/diagnostic"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func r3Has(r diagnostic.Report, code string) bool {
	for _, c := range r.Checks {
		if c.Code == code {
			return true
		}
	}
	return false
}
func r3Read(t *testing.T, st *postgres.Store, id string, o diagnostic.Options) diagnostic.Report {
	t.Helper()
	r, err := postgres.NewDiagnosticReader(st.Pool()).Read(context.Background(), id, o)
	if err != nil || !r.Complete {
		t.Fatalf("diagnostic incomplete: %+v %v", r.Checks, err)
	}
	return r
}

// Whole fixture tables (including CAS versions, snapshots and admissions) are
// compared, not just resource counts. This helper is test-only and uncapped.
func r3Fingerprint(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var out strings.Builder
	for _, table := range []string{"index_root", "index_root_adapter_config", "index_scope_watch_state", "index_dirty_scope_work", "index_generation", "index_canonical_resource", "index_journal_event", "index_snapshot", "index_reconcile_result", "index_admission"} {
		var value string
		if err := pool.QueryRow(context.Background(), `SELECT coalesce(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) v FROM `+table+` t) q`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		out.WriteString(value)
	}
	return out.String()
}

func TestP0R3PostgresStatesAndNoWrites(t *testing.T) {
	for _, kind := range []string{"healthy", "inactive", "disabled", "due", "late", "deferred", "auth", "retry-cap", "ordinary-internal", "blocked", "inflight", "stale-inflight", "work-cap", "partial-untrusted", "fresh-proof", "old-snapshot", "child-snapshot", "duplicate-path", "journal-gap", "generation-mismatch", "terminal-mismatch", "conflicts", "journal-cap", "canonical-cap"} {
		t.Run(kind, func(t *testing.T) {
			st, ctx := p3Store(t)
			now := time.Now().UTC()
			due := now.Add(time.Minute)
			watch := p3HotWatch(p3RootActive, "/", due)
			watch.CadenceClass = "SOURCE_MIRROR"
			switch kind {
			case "due":
				d := now.Add(-time.Second)
				watch.NextDueAt = &d
			case "late":
				d := now.Add(-time.Hour)
				watch.NextDueAt = &d
			case "deferred":
				d := now.Add(-time.Hour)
				watch.NextDueAt = &d
				watch.DeferredUntil = &due
			case "disabled":
				watch.WatchState = state.WatchDisabled
				watch.NextDueAt = nil
				watch.EffectiveIntervalSeconds = nil
			}
			if _, err := st.CreateWatch(ctx, watch); err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertAdapterConfig(ctx, p3RootActive, postgres.AdapterConfig{CollectorKind: "openlist", Config: []byte(`{"path":"/","mirror_presence":true,"topology_token_env":"SECRET_DO_NOT_EMIT","url":"https://secret.invalid/token","password_env":"SECRET_DO_NOT_EMIT"}`)}); err != nil {
				t.Fatal(err)
			}
			if kind == "inactive" {
				if _, err := st.TransitionRootLifecycle(ctx, p3RootActive, "DEPRECATED"); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "auth" || kind == "retry-cap" || kind == "ordinary-internal" || kind == "blocked" || kind == "inflight" || kind == "stale-inflight" || kind == "work-cap" {
				at := now
				if kind == "stale-inflight" {
					at = now.Add(-time.Hour)
				}
				if _, err := st.MergeSignal(ctx, p3Signal(p3RootActive, "/", state.SourceManualOperator, state.ReasonManualVerify, at)); err != nil {
					t.Fatal(err)
				}
				claim, err := p3Claim(t, st, ctx, p3RootActive, "/", at)
				if err != nil {
					t.Fatal(err)
				}
				if kind != "inflight" && kind != "stale-inflight" {
					class := state.ErrorInternal
					if kind == "auth" {
						class = state.ErrorAuthOrPermission
					}
					if kind == "blocked" {
						class = state.ErrorInvalidScope
					}
					if _, err = st.CompleteFailure(ctx, p3RootActive, "/", *claim.ClaimedSignalSeq, class, &due, now); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "retry-cap" || kind == "ordinary-internal" || kind == "auth" {
					if _, err = st.Pool().Exec(ctx, `UPDATE index_dirty_scope_work SET consecutive_failures=3 WHERE root_id=$1`, p3RootActive); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "ordinary-internal" {
					if _, err = st.Pool().Exec(ctx, `UPDATE index_root_adapter_config SET collector_kind='rclone' WHERE root_id=$1`, p3RootActive); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "work-cap" {
					if _, err = st.MergeSignal(ctx, p3Signal(p3RootActive, "/other", state.SourceManualOperator, state.ReasonManualVerify, now)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "partial-untrusted" || kind == "fresh-proof" || kind == "old-snapshot" || kind == "child-snapshot" {
				at := now
				if kind == "old-snapshot" {
					at = now.Add(-time.Hour)
				}
				prov := `{"mode":"scoped_refresh","scope":"/","directory_coverage":{"version":1},"token":"SECRET_DO_NOT_EMIT"}`
				if kind == "fresh-proof" {
					prov = `{"mode":"scoped_refresh","scope":"/","directory_coverage":{"version":2}}`
				}
				if kind == "child-snapshot" {
					prov = `{"mode":"scoped_refresh","scope":"/private-child"}`
				}
				if _, err := st.Pool().Exec(ctx, `INSERT INTO index_snapshot(snapshot_id,root_id,provenance,observed_at,traversal_status,completeness_flag,acceptance_state,lifecycle_state,freshness_evidence) VALUES ($1,$2,$3,$4,'PARTIAL','PARTIAL','PARTIAL','RECONCILED','FRESH_REFRESHED')`, reconcile.NewUUID(), p3RootActive, prov, at); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "duplicate-path" || kind == "journal-gap" || kind == "generation-mismatch" || kind == "terminal-mismatch" || kind == "journal-cap" || kind == "canonical-cap" {
				if _, err := st.Pool().Exec(ctx, `UPDATE index_root SET current_generation=1 WHERE root_id=$1`, p3RootActive); err != nil {
					t.Fatal(err)
				}
				if _, err := st.Pool().Exec(ctx, `INSERT INTO index_generation(root_id,generation_number,summary) VALUES ($1,1,'{}')`, p3RootActive); err != nil {
					t.Fatal(err)
				}
				for n := 1; n <= 2; n++ {
					id := reconcile.NewUUID()
					seq := n
					gen := 1
					kindEvent := "resource-added"
					path := "/private-" + id
					if kind == "duplicate-path" {
						path = "/secret-password-path"
					}
					if kind == "journal-gap" {
						seq += 1
					}
					if kind == "generation-mismatch" {
						gen = 2
					}
					if kind == "terminal-mismatch" {
						kindEvent = "resource-removed"
					}
					if _, err := st.Pool().Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path) VALUES ($1,$2,1,1,'PRESENT','NONE',$3)`, id, p3RootActive, path); err != nil {
						t.Fatal(err)
					}
					if _, err := st.Pool().Exec(ctx, `INSERT INTO index_journal_event(root_id,event_seq,generation_number,intra_generation_seq,event_type,resource_id,payload) VALUES ($1,$2,$3,$4,$5,$6,'{"token":"SECRET_DO_NOT_EMIT"}')`, p3RootActive, seq, gen, n, kindEvent, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "conflicts" {
				if _, err := st.Pool().Exec(ctx, `INSERT INTO index_reconcile_result(reconcile_id,root_id,admission_seq,outcome,conflicts) VALUES ($1,$2,1,'NOOP','[{"path":"SECRET_DO_NOT_EMIT"}]')`, reconcile.NewUUID(), p3RootActive); err != nil {
					t.Fatal(err)
				}
			}
			o := diagnostic.Defaults()
			if strings.HasSuffix(kind, "-cap") && kind != "retry-cap" {
				o.RowLimit = 1
			}
			before := r3Fingerprint(t, st.Pool())
			r := r3Read(t, st, p3RootActive, o)
			after := r3Fingerprint(t, st.Pool())
			if before != after {
				t.Fatal("diagnostic mutated durable state")
			}
			code := map[string]string{"healthy": "BOUNDED_CONSISTENCY", "inactive": "ROOT_INACTIVE", "disabled": "WATCH_DISABLED", "due": "WATCH_DUE", "late": "WATCH_LAG", "deferred": "WATCH_SCHEDULED", "auth": "SOURCE_UNAVAILABLE_WORK", "retry-cap": "RETRY_BUDGET_EXHAUSTED", "ordinary-internal": "RETRY_WAIT", "blocked": "BLOCKED_WORK", "inflight": "IN_FLIGHT", "stale-inflight": "STALE_IN_FLIGHT", "work-cap": "WORK_LIMIT", "partial-untrusted": "TOPOLOGY_UNTRUSTED", "fresh-proof": "HISTORICAL_COVERAGE", "old-snapshot": "SOURCE_STALE", "child-snapshot": "ROOT_FRESHNESS", "duplicate-path": "IDENTITY_AMBIGUITY", "journal-gap": "CANONICAL_JOURNAL_MISMATCH", "generation-mismatch": "CANONICAL_JOURNAL_MISMATCH", "terminal-mismatch": "CANONICAL_JOURNAL_MISMATCH", "conflicts": "IDENTITY_AMBIGUITY", "journal-cap": "CONSISTENCY_LIMIT", "canonical-cap": "IDENTITY_LIMIT"}[kind]
			if !r3Has(r, code) {
				t.Fatalf("missing %s: %+v", code, r.Checks)
			}
			if kind == "ordinary-internal" && r3Has(r, "RETRY_BUDGET_EXHAUSTED") {
				t.Fatal("invented generic INTERNAL budget")
			}
			if !r3Has(r, "CONSUMER_PROJECTION_LAG") || r.Overall == "OK" {
				t.Fatal("invented source/consumer health")
			}
			b, _ := json.Marshal(r)
			if kind == "auth" && (r.Work.SourceFailures != 1 || !strings.Contains(string(b), `"work_items_with_source_failures":1`)) {
				t.Fatal("three retained failures on one work item must count as one work item")
			}
			if kind == "retry-cap" && (r.Work.InternalRetryCap != 1 || !strings.Contains(string(b), `"mirror_internal_retry_cap_work_items":1`)) {
				t.Fatal("one exhausted work item must not count its three failure events")
			}
			if strings.Contains(string(b), `"source_failure_records"`) || strings.Contains(string(b), `"mirror_internal_retry_cap_records"`) {
				t.Fatal("ambiguous pre-review counter fields retained")
			}
			for _, secret := range []string{"SECRET_DO_NOT_EMIT", "secret.invalid", "secret-password-path", "private-child"} {
				if strings.Contains(string(b), secret) {
					t.Fatal("secret leakage")
				}
			}
		})
	}
}

type r3Tracer struct {
	mu       sync.Mutex
	bad      bool
	readonly bool
}

func (s *r3Tracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	sql := strings.ToLower(strings.TrimSpace(d.SQL))
	if strings.HasPrefix(sql, "begin") {
		s.readonly = strings.Contains(sql, "read only") && strings.Contains(sql, "repeatable read")
	} else if sql != "rollback" && !strings.HasPrefix(sql, "select ") {
		s.bad = true
	}
	return ctx
}
func (*r3Tracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestP0R3ReadOnlyTransactionAndFailureBoundaries(t *testing.T) {
	st, ctx := p3Store(t)
	cfg, err := pgxpool.ParseConfig(testutil.TestDSN())
	if err != nil {
		t.Fatal(err)
	}
	trace := &r3Tracer{}
	cfg.ConnConfig.Tracer = trace
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	r, err := postgres.NewDiagnosticReader(pool).Read(ctx, p3RootActive, diagnostic.Defaults())
	if err != nil || !r.Complete {
		t.Fatal("SELECT-only diagnostic failed under database-enforced read-only default")
	}
	trace.mu.Lock()
	bad, ro := trace.bad, trace.readonly
	trace.mu.Unlock()
	if bad || !ro {
		t.Fatal("non-SELECT or non-read-only transaction")
	}
	for _, kind := range []string{"missing-root", "deadline", "locked-query-deadline", "query-failure", "future-schema", "missing-schema"} {
		t.Run(kind, func(t *testing.T) {
			readCtx := ctx
			id := p3RootActive
			want := ""
			o := diagnostic.Defaults()
			switch kind {
			case "missing-root":
				id = "00000000-0000-0000-0000-999999999999"
				want = "ROOT_NOT_FOUND"
			case "deadline":
				var cancel context.CancelFunc
				readCtx, cancel = context.WithCancel(ctx)
				cancel()
				want = "DIAGNOSTIC_DEADLINE"
			case "locked-query-deadline":
				lock, e := st.Pool().Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer lock.Rollback(ctx)
				if _, e = lock.Exec(ctx, `LOCK TABLE index_dirty_scope_work IN ACCESS EXCLUSIVE MODE`); e != nil {
					t.Fatal(e)
				}
				o.Timeout = 50 * time.Millisecond
				want = "DIAGNOSTIC_DEADLINE"
			case "query-failure":
				if _, err := st.Pool().Exec(ctx, `ALTER TABLE index_dirty_scope_work RENAME TO hidden_work`); err != nil {
					t.Fatal(err)
				}
				defer st.Pool().Exec(ctx, `ALTER TABLE hidden_work RENAME TO index_dirty_scope_work`)
				want = "WORK_QUERY_FAILED"
			case "future-schema":
				if _, err := st.Pool().Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ('9999_SECRET_DO_NOT_EMIT.sql')`); err != nil {
					t.Fatal(err)
				}
				defer st.Pool().Exec(ctx, `DELETE FROM schema_migrations WHERE version='9999_SECRET_DO_NOT_EMIT.sql'`)
				want = "SCHEMA_INCOMPATIBLE"
			case "missing-schema":
				if _, err := st.Pool().Exec(ctx, `ALTER TABLE schema_migrations RENAME TO hidden_migrations`); err != nil {
					t.Fatal(err)
				}
				defer st.Pool().Exec(ctx, `ALTER TABLE hidden_migrations RENAME TO schema_migrations`)
				want = "SCHEMA_INCOMPATIBLE"
			}
			started := time.Now()
			r, err := postgres.NewDiagnosticReader(pool).Read(readCtx, id, o)
			if time.Since(started) > 2*time.Second {
				t.Fatal("diagnostic exceeded deadline/cleanup bound")
			}
			if err == nil || r.Complete || !r3Has(r, want) || r.Overall != "DEGRADED" {
				t.Fatalf("false green failure: %s %+v", kind, r.Checks)
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "SECRET_DO_NOT_EMIT") || strings.Contains(err.Error(), "SECRET_DO_NOT_EMIT") {
				t.Fatal("raw error leaked")
			}
		})
	}
}

func TestP0R3AcceptedCanonicalAndJournalEvidence(t *testing.T) {
	st, ctx := newStore(t)
	seedPipelineRoot(t, st, ctx)
	now := time.Now().UTC()
	entries := []domain.SnapshotEntry{
		entryWithProviderID("one.txt", "/", "P1", "h1", 1, now),
		entryWithProviderID("two.txt", "/", "P2", "h2", 2, now),
	}
	snap := reconcile.NewUUID()
	insertSubmittedSnapshot(t, st, ctx, snap, entries)
	processSnapshot(t, st, ctx, snap, reconcile.Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1})
	before := r3Fingerprint(t, st.Pool())
	r := r3Read(t, st, pipeRoot, diagnostic.Defaults())
	if r.Inventory.Mismatch || r.Inventory.CanonicalSample != 2 || r.Inventory.JournalHighWatermark != 2 || !r3Has(r, "BOUNDED_CONSISTENCY") {
		t.Fatalf("rejected accepted pipeline state: %+v", r.Inventory)
	}
	if r.Latest == nil || r.Latest.Acceptance != "COMPLETE" || r.Latest.Traversal != "SUCCESS" || r.Overall != "UNKNOWN" {
		t.Fatal("invented live health or lost complete observation")
	}
	second := r3Read(t, st, pipeRoot, diagnostic.Defaults())
	second.AsOf = r.AsOf // Only the independently sampled database clock changes.
	firstJSON, _ := json.Marshal(r)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("identical durable evidence produced unstable JSON")
	}
	if before != r3Fingerprint(t, st.Pool()) {
		t.Fatal("diagnostic changed accepted data")
	}
}
