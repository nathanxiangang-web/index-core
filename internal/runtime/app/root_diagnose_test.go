package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/diagnostic"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestP0R3CLIReadOnlyNoProviderOrWriter(t *testing.T) {
	p7PrepareSchema(t)
	pool := testutil.Pool(t)
	ctx := context.Background()
	st := postgres.New(pool)
	const id = "f3000000-0000-0000-0000-000000000123"
	if err := st.CreateRoot(ctx, pool, id, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	adapter, _ := json.Marshal(map[string]any{"url": server.URL, "path": "/", "token_env": "DO_NOT_RESOLVE_R3", "password_env": "SECRET_DO_NOT_EMIT"})
	if err := st.UpsertAdapterConfig(ctx, id, postgres.AdapterConfig{CollectorKind: "openlist", Config: adapter}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DO_NOT_RESOLVE_R3", "SECRET_DO_NOT_EMIT")
	// A live writer lock must not block this reader, and must stay held.
	lock, err := st.AcquireWriterLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release(ctx)
	cfg := p7BaseConfig()
	cfg.RclonePath = "/must-not-run-rclone"
	cfg.HintAddr = "not-used"
	cfg.HintToken = "SECRET_DO_NOT_EMIT"
	var out bytes.Buffer
	if runRootDiagnose(ctx, cfg, []string{"--root-id", id}, &out) == nil {
		t.Fatal("unknown source/consumer evidence must not exit green")
	}
	var r diagnostic.Report
	if json.Unmarshal(out.Bytes(), &r) != nil || !r.Complete || r.Overall != "UNKNOWN" {
		t.Fatalf("bad JSON %s", out.String())
	}
	if calls.Load() != 0 || strings.Contains(out.String(), "SECRET_DO_NOT_EMIT") || strings.Contains(out.String(), server.URL) {
		t.Fatal("provider call or secret leakage")
	}
	if second, err := st.AcquireWriterLock(ctx); err == nil {
		second.Release(ctx)
		t.Fatal("diagnostic released daemon lock")
	}
}

func TestP0R3CLISecretFreeDatabaseAndFlagFailures(t *testing.T) {
	for _, dsn := range []string{"SECRET_DO_NOT_EMIT", "postgres://secret:SECRET_DO_NOT_EMIT@127.0.0.1:1/unknown?sslmode=disable", ""} {
		cfg := config.Defaults()
		cfg.DatabaseURL = dsn
		var out bytes.Buffer
		started := time.Now()
		err := runRootDiagnose(context.Background(), cfg, []string{"--root-id", "00000000-0000-0000-0000-000000000123", "--timeout", "100ms"}, &out)
		if err == nil || time.Since(started) > 2*time.Second {
			t.Fatal("database failure not bounded")
		}
		if strings.Contains(out.String(), "SECRET_DO_NOT_EMIT") || strings.Contains(err.Error(), "SECRET_DO_NOT_EMIT") {
			t.Fatal("DSN leak")
		}
		var r diagnostic.Report
		if json.Unmarshal(out.Bytes(), &r) != nil || r.Complete || r.Overall != "DEGRADED" {
			t.Fatal("false green database failure")
		}
	}
	for _, args := range [][]string{{"--root-id", "SECRET_DO_NOT_EMIT"}, {"--database-url=SECRET_DO_NOT_EMIT", "--unknown=SECRET_DO_NOT_EMIT"}, {"--root-id", "00000000-0000-0000-0000-000000000123", "--row-limit", "10001"}} {
		var out bytes.Buffer
		err := runRootDiagnose(context.Background(), config.Defaults(), args, &out)
		if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "SECRET_DO_NOT_EMIT") {
			t.Fatal("unsafe argument handling")
		}
	}
}

func TestP0R3CLIPrefersCatalogBuiltinsOverPublicShadows(t *testing.T) {
	p7PrepareSchema(t)
	pool := testutil.Pool(t)
	ctx := context.Background()
	const id = "f3000000-0000-0000-0000-000000000124"
	if err := postgres.New(pool).CreateRoot(ctx, pool, id, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	// Test-only DDL in the disposable database. These exact-signature shadows
	// would corrupt diagnostics under public,pg_catalog, even in READ ONLY.
	for _, ddl := range []string{
		`CREATE FUNCTION public.transaction_timestamp() RETURNS timestamptz LANGUAGE sql IMMUTABLE AS $$ SELECT '2000-01-01T00:00:00Z'::timestamptz $$`,
		`CREATE FUNCTION public.octet_length(text) RETURNS integer LANGUAGE sql IMMUTABLE AS $$ SELECT 999 $$`,
		`CREATE FUNCTION public.jsonb_typeof(jsonb) RETURNS text LANGUAGE sql IMMUTABLE AS $$ SELECT 'shadow'::text $$`,
		`CREATE FUNCTION public.jsonb_array_length(jsonb) RETURNS integer LANGUAGE sql IMMUTABLE AS $$ SELECT -7 $$`,
	} {
		if _, err := pool.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	// Positive control: prove the shadows really intercept the unsafe order;
	// a test which merely creates unused functions would not establish this.
	control, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Rollback(ctx)
	if _, err = control.Exec(ctx, `SET LOCAL search_path TO public,pg_catalog`); err != nil {
		t.Fatal(err)
	}
	var at time.Time
	var length, arrayLength int
	var kind string
	if err = control.QueryRow(ctx, `SELECT transaction_timestamp(),octet_length('abc'::text),jsonb_typeof('[]'::jsonb),jsonb_array_length('[]'::jsonb)`).Scan(&at, &length, &kind, &arrayLength); err != nil {
		t.Fatal(err)
	}
	if at.Year() != 2000 || length != 999 || kind != "shadow" || arrayLength != -7 {
		t.Fatal("unsafe-order control did not resolve all four public shadows")
	}
	if err = control.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Force the JSON type/array functions to execute in the actual diagnostic.
	if _, err = pool.Exec(ctx, `INSERT INTO index_reconcile_result(reconcile_id,root_id,admission_seq,outcome,conflicts) VALUES ('f3000000-0000-0000-0000-000000000125',$1,1,'NOOP','[{},{}]')`, id); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Second)
	var out bytes.Buffer
	if runRootDiagnose(ctx, p7BaseConfig(), []string{"--root-id", id}, &out) == nil {
		t.Fatal("ambiguous evidence must not exit green")
	}
	var report diagnostic.Report
	if err = json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Complete || report.SchemaApplied != report.SchemaRequired || report.Inventory.LatestConflictCount != 2 || report.AsOf.Before(started) || report.AsOf.After(time.Now().Add(time.Second)) {
		t.Fatal("diagnostic used public shadows instead of catalog built-ins")
	}
}
