package postgres

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/internal/diagnostic"
)

// DiagnosticReader is an operator-only facade. It exposes neither generic SQL
// nor any writer/collector. Every data read shares one READ ONLY REPEATABLE READ
// transaction. Nothing here acquires the daemon writer lock or row/claim locks.
type DiagnosticReader struct{ pool *pgxpool.Pool }

func NewDiagnosticReader(pool *pgxpool.Pool) *DiagnosticReader { return &DiagnosticReader{pool} }

var diagnosticUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func ValidDiagnosticRoot(id string) bool { return diagnosticUUID.MatchString(id) }

func (d *DiagnosticReader) Read(ctx context.Context, root string, o diagnostic.Options) (r diagnostic.Report, err error) {
	r = diagnostic.New("", o)
	if o.Validate() != nil || !ValidDiagnosticRoot(root) {
		r.Add("INVALID_ARGUMENT", "ERROR", "Invalid root UUID or diagnostic limits")
		r.Finish()
		return r, errors.New("invalid diagnostic arguments")
	}
	r.RootID = strings.ToLower(root)
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	stage := "DATABASE_UNAVAILABLE"
	// Never return driver errors: connection and SQL errors can contain secrets.
	defer func() {
		if err != nil {
			if ctx.Err() != nil {
				stage = "DIAGNOSTIC_DEADLINE"
			}
			r.Complete = false
			r.Add(stage, "ERROR", "Diagnostic could not complete this read; raw database error omitted")
			r.Finish()
			err = errors.New("root diagnostic incomplete; inspect JSON status")
		}
	}()
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return r, err
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = tx.Rollback(cleanup)
	}()
	if err = tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&r.AsOf); err != nil {
		return r, err
	}
	r.AsOf = r.AsOf.UTC()
	stage = "SCHEMA_UNAVAILABLE"
	compatible, e := diagnosticSchema(ctx, tx, &r)
	if e != nil {
		return r, e
	}
	if !compatible {
		stage = "SCHEMA_INCOMPATIBLE"
		return r, errors.New("incompatible diagnostic schema")
	}
	r.Add("SCHEMA_COMPATIBLE", "OK", "Embedded migration versions match; this is not serve readiness")
	stage = "ROOT_QUERY_FAILED"
	err = tx.QueryRow(ctx, `SELECT lifecycle_state,current_generation FROM public.index_root WHERE root_id=$1::uuid`, root).Scan(&r.Lifecycle, &r.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		stage = "ROOT_NOT_FOUND"
	}
	if err != nil {
		return r, err
	}
	stage = "WATCH_QUERY_FAILED"
	if err = diagnosticWatch(ctx, tx, root, &r); err != nil {
		return r, err
	}
	stage = "WORK_QUERY_FAILED"
	if err = diagnosticWork(ctx, tx, root, o.RowLimit, &r); err != nil {
		return r, err
	}
	stage = "OBSERVATION_QUERY_FAILED"
	if err = diagnosticObservation(ctx, tx, root, &r); err != nil {
		return r, err
	}
	stage = "INVENTORY_QUERY_FAILED"
	if err = diagnosticInventory(ctx, tx, root, o.RowLimit, &r); err != nil {
		return r, err
	}
	r.Complete = true
	r.Evaluate(o)
	return r, nil
}

func diagnosticSchema(ctx context.Context, tx pgx.Tx, r *diagnostic.Report) (bool, error) {
	names, err := RequiredMigrations()
	if err != nil {
		return false, err
	}
	r.SchemaRequired = len(names)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT CASE WHEN octet_length(version)<=256 THEN version ELSE '' END FROM public.schema_migrations ORDER BY version LIMIT $1`, len(names)+1)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	seen := map[string]bool{}
	compatible := true
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			return false, err
		}
		if !known[v] || seen[v] {
			compatible = false
		}
		if known[v] && !seen[v] {
			r.SchemaApplied++
		}
		seen[v] = true
	}
	return compatible && r.SchemaApplied == r.SchemaRequired, rows.Err()
}

func diagnosticWatch(ctx context.Context, tx pgx.Tx, root string, r *diagnostic.Report) error {
	w := &diagnostic.Watch{}
	err := tx.QueryRow(ctx, `SELECT watch_state,priority_class,CASE WHEN cadence_class='SOURCE_MIRROR' THEN 'SOURCE_MIRROR' ELSE 'OTHER' END,
 effective_interval_seconds,next_due_at,deferred_until,last_attempt_started_at,last_attempt_finished_at,last_success_at,
 consecutive_failures,coalesce(last_error_class,'NONE') FROM public.index_scope_watch_state WHERE root_id=$1::uuid AND scope_key='/'`, root).Scan(&w.State, &w.Priority, &w.Cadence, &w.IntervalSeconds, &w.NextDueAt, &w.DeferredUntil, &w.LastStartedAt, &w.LastFinishedAt, &w.LastSuccessAt, &w.Failures, &w.LastError)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		r.Watch = w
	}
	// Only booleans leave adapter JSON. Do not return raw config or env names.
	var mirror, topology, collector bool
	err = tx.QueryRow(ctx, `SELECT coalesce(config->'mirror_presence'='true'::jsonb,false),
 (coalesce(config->>'topology_token_env','')<>'' OR (coalesce(config->>'topology_username_env','')<>'' AND coalesce(config->>'topology_password_env','')<>'')),
 collector_kind IN ('alist','openlist') FROM public.index_root_adapter_config WHERE root_id=$1::uuid`, root).Scan(&mirror, &topology, &collector)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	r.Mirror = mirror
	r.MirrorRetryPolicy = r.Lifecycle == "ACTIVE" && mirror && topology && collector && w.Cadence == "SOURCE_MIRROR" && (w.State == "HOT" || w.State == "WARM")
	if r.MirrorRetryPolicy {
		var operator bool
		if err = tx.QueryRow(ctx, `SELECT 'OPERATOR_POLICY'=ANY(source_set) FROM public.index_scope_watch_state WHERE root_id=$1::uuid AND scope_key='/'`, root).Scan(&operator); err != nil {
			return err
		}
		r.MirrorRetryPolicy = operator
	}
	return nil
}

func diagnosticWork(ctx context.Context, tx pgx.Tx, root string, limit int, r *diagnostic.Report) error {
	rows, err := tx.Query(ctx, `SELECT work_state,scope_key='/',coalesce(last_error_class,'NONE'),consecutive_failures,
 coalesce(pending_not_before,pending_first_seen_at),last_attempt_started_at FROM public.index_dirty_scope_work
 WHERE root_id=$1::uuid ORDER BY scope_key LIMIT $2`, root, limit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.Work.Exact = true
	count := 0
	for rows.Next() {
		count++
		if count > limit {
			r.Work.Exact = false
			break
		}
		var state, class string
		var isRoot bool
		var failures int64
		var due, started *time.Time
		if err = rows.Scan(&state, &isRoot, &class, &failures, &due, &started); err != nil {
			return err
		}
		r.Work.Counts[state]++
		if (state == "PENDING" || state == "RETRY_WAIT") && due != nil && (r.Work.OldestDueAt == nil || due.Before(*r.Work.OldestDueAt)) {
			r.Work.OldestDueAt = due
		}
		if state == "IN_FLIGHT" && started != nil && (r.Work.OldestInflightAt == nil || started.Before(*r.Work.OldestInflightAt)) {
			r.Work.OldestInflightAt = started
		}
		if failures > 0 && (class == "AUTH_OR_PERMISSION" || class == "TRANSIENT_PROVIDER" || class == "THROTTLED") {
			r.Work.SourceFailures++
		}
		if isRoot && r.MirrorRetryPolicy && state == "RETRY_WAIT" && class == "INTERNAL" && failures >= 3 {
			r.Work.InternalRetryCap++
		}
	}
	return rows.Err()
}

func diagnosticObservation(ctx context.Context, tx pgx.Tx, root string, r *diagnostic.Report) error {
	s := &diagnostic.Observation{}
	// No raw provenance/error_summary. Scope comparison is evaluated in SQL.
	err := tx.QueryRow(ctx, `SELECT s.observed_at,s.traversal_status,coalesce(s.acceptance_state,'UNEVALUATED'),s.completeness_flag,
 coalesce(s.freshness_evidence,'UNKNOWN'),s.lifecycle_state='RECONCILED',
 coalesce(s.provenance->'directory_coverage'->'version'='2'::jsonb,false),
 CASE WHEN s.provenance->>'mode'='scoped_refresh' THEN
   CASE WHEN s.provenance->>'scope'=a.config->>'path' THEN 'ROOT' ELSE 'CHILD' END
 WHEN s.provenance->>'path'=a.config->>'path' THEN 'ROOT' ELSE 'UNTRACKED' END
 FROM public.index_snapshot s LEFT JOIN public.index_root_adapter_config a ON a.root_id=s.root_id
 WHERE s.root_id=$1::uuid ORDER BY s.observed_at DESC,s.snapshot_id DESC LIMIT 1`, root).Scan(&s.At, &s.Traversal, &s.Acceptance, &s.Completeness, &s.Freshness, &s.Reconciled, &s.HistoricalCoverageV2, &s.Scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.At = s.At.UTC()
	r.Latest = s
	return nil
}

func diagnosticInventory(ctx context.Context, tx pgx.Tx, root string, limit int, r *diagnostic.Report) error {
	i := &r.Inventory
	var generationExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.index_generation WHERE root_id=$1::uuid AND generation_number=$2)`, root, r.Generation).Scan(&generationExists); err != nil {
		return err
	}
	if r.Generation > 0 && !generationExists {
		i.Mismatch = true
	}
	// Capped rows, bounded-width paths held only inside this function, never output.
	rows, err := tx.Query(ctx, `SELECT resource_id::text,resource_presence,
 CASE WHEN octet_length(canonical_path)<=4096 THEN canonical_path END,
 introduced_at_generation,last_confirmed_generation FROM public.index_canonical_resource
 WHERE root_id=$1::uuid ORDER BY resource_id LIMIT $2`, root, limit+1)
	if err != nil {
		return err
	}
	presence := map[string]string{}
	paths := map[string]int{}
	i.CanonicalExact = true
	for rows.Next() {
		if i.CanonicalSample == limit {
			i.CanonicalExact = false
			break
		}
		var id, p string
		var path *string
		var intro, last int64
		if err = rows.Scan(&id, &p, &path, &intro, &last); err != nil {
			rows.Close()
			return err
		}
		i.CanonicalSample++
		presence[id] = p
		if intro > r.Generation || last > r.Generation || intro > last {
			i.Mismatch = true
		}
		if p == "PRESENT" && path != nil {
			paths[*path]++
		}
		if p == "PRESENT" && path == nil {
			i.CanonicalExact = false
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range paths {
		if n > 1 {
			i.DuplicatePresentPaths++
		}
	}
	// Indexed tail read remains exact even if the bounded full-journal scan caps.
	err = tx.QueryRow(ctx, `SELECT event_seq FROM public.index_journal_event WHERE root_id=$1::uuid ORDER BY event_seq DESC LIMIT 1`, root).Scan(&i.JournalHighWatermark)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT generation_number FROM public.index_journal_event WHERE root_id=$1::uuid ORDER BY generation_number DESC LIMIT 1`, root).Scan(&i.JournalMaxGeneration)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if i.JournalMaxGeneration > r.Generation {
		i.Mismatch = true
	}
	rows, err = tx.Query(ctx, `SELECT event_seq,generation_number,event_type,resource_id::text FROM public.index_journal_event WHERE root_id=$1::uuid ORDER BY event_seq LIMIT $2`, root, limit+1)
	if err != nil {
		return err
	}
	i.JournalExact = true
	terminal := map[string]string{}
	var previous int64
	for rows.Next() {
		if i.JournalSample == limit {
			i.JournalExact = false
			break
		}
		var seq, gen int64
		var kind string
		var id *string
		if err = rows.Scan(&seq, &gen, &kind, &id); err != nil {
			rows.Close()
			return err
		}
		i.JournalSample++
		if seq != previous+1 || gen > r.Generation {
			i.Mismatch = true
		}
		previous = seq
		if gen > i.JournalMaxGeneration {
			i.JournalMaxGeneration = gen
		}
		if id != nil {
			if kind == "resource-removed" {
				terminal[*id] = "REMOVED"
			} else if strings.HasPrefix(kind, "resource-") {
				terminal[*id] = "PRESENT"
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if i.JournalExact && i.CanonicalExact {
		for id, p := range presence {
			if terminal[id] != p {
				i.Mismatch = true
			}
		}
		for id := range terminal {
			if _, ok := presence[id]; !ok {
				i.Mismatch = true
			}
		}
	}
	// Read the last durable reconcile's conflict count, not sensitive conflict data.
	err = tx.QueryRow(ctx, `SELECT CASE WHEN jsonb_typeof(conflicts)='array' THEN jsonb_array_length(conflicts) ELSE 0 END
 FROM public.index_reconcile_result WHERE root_id=$1::uuid ORDER BY admission_seq DESC,reconcile_id DESC LIMIT 1`, root).Scan(&i.LatestConflictCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}
