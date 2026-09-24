package scan_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// TestLiveP0ScopedRefresh is the gated live P0 probe (Issue #62, runbook).
//
// It points the prototype at a REAL, non-production OpenList (a storage using
// the community `115 Open` driver) and the real test PostgreSQL, runs one
// targeted scoped refresh, and reports the observed synchronisation latency plus
// Q3/Q4/Q6 visibility of the out-of-band file.
//
// Opt-in (never run by default):
//
//	INDEXCORE_P0_LIVE_BASE_URL=http://127.0.0.1:5244 \
//	INDEXCORE_P0_LIVE_USER=test INDEXCORE_P0_LIVE_PASS=... \
//	INDEXCORE_TEST_DATABASE_URL=postgres://... \
//	go test ./internal/runtime/scan -run TestLiveP0ScopedRefresh -v
//
// The decisive stale-cache gate (T0 refresh=false -> T1 out-of-band write ->
// T2 refresh=false still stale) must be performed OUTSIDE this probe, because
// this probe itself issues the single canonical refresh=true observation.
func TestLiveP0ScopedRefresh(t *testing.T) {
	base := os.Getenv("INDEXCORE_P0_LIVE_BASE_URL")
	if base == "" {
		t.Skip("set INDEXCORE_P0_LIVE_BASE_URL/INDEXCORE_P0_LIVE_USER/INDEXCORE_P0_LIVE_PASS to run the live P0 probe")
	}
	scope := os.Getenv("INDEXCORE_P0_LIVE_SCOPE")
	if scope == "" {
		scope = "/"
	}
	expect := os.Getenv("INDEXCORE_P0_LIVE_EXPECT") // e.g. /ressa.txt
	os.Setenv("P0_LIVE_USER", os.Getenv("INDEXCORE_P0_LIVE_USER"))
	os.Setenv("P0_LIVE_PASS", os.Getenv("INDEXCORE_P0_LIVE_PASS"))

	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	const rootID = "f0000000-0000-0000-0000-0000000000c9"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	acfg, _ := json.Marshal(map[string]string{
		"base_url": base, "path": "/",
		"username_env": "P0_LIVE_USER", "password_env": "P0_LIVE_PASS",
	})
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "openlist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}

	svc := scan.New(st, "", "", 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// T3: one targeted scoped refresh (internally the single canonical refresh=true).
	start := time.Now()
	res, err := svc.ScanScope(ctx, rootID, scope, 100)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ScanScope failed (live): %v", err)
	}
	t.Logf("T3-T5 scoped refresh wall time = %d ms", elapsed.Milliseconds())
	t.Logf("outcome = %+v", res.Outcome)

	// T6: Q4 (children) / Q6 (active page) visibility + Q3 (get resource by id).
	qr := postgres.NewQueryReader(st.Pool())
	page, err := qr.ListActivePage(ctx, rootID, nil, 1000)
	if err != nil {
		t.Fatalf("Q6 list active: %v", err)
	}
	found := expect == ""
	var ids []string
	for _, it := range page.Items {
		if it.CanonicalPath != nil {
			t.Logf("Q6 PRESENT: %s", *it.CanonicalPath)
			if expect != "" && *it.CanonicalPath == expect {
				found = true
				ids = append(ids, it.ResourceID)
			}
		}
	}
	if expect != "" {
		if !found {
			t.Fatalf("out-of-band file %q NOT visible via Q6 after scoped refresh", expect)
		}
		for _, id := range ids {
			if v, _ := qr.GetResource(ctx, id, query.ReadOptions{}); v == nil {
				t.Fatalf("Q3 get resource %s returned nothing", id)
			}
		}
		t.Logf("Q3/Q4/Q6 all expose %q", expect)
	}
}
