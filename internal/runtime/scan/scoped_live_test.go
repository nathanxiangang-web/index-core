package scan_test

import (
	"bytes"
	"context"
	"encoding/json"

	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Gated live P0 probes (Issue #62 + Architect live-evidence rework).
//
// Two phases over the SAME root / SAME database (Phase B must NOT ResetSchema),
// so that scoped refresh is proven to be an INCREMENT on an already-populated
// Canonical Inventory, not a first-time load.
//
// Phase A (baseline):  ResetSchema -> full Service.Scan() -> record resource_ids.
//   [operator, out-of-band via the 115 official channel] uploads a NEW file
//   T0 refresh=false = old set; T2 refresh=false = still old set (stale gate)
// Phase B (increment): no reset -> ScanScope("/") -> Q3/Q4/Q6 -> old resource_ids
//   unchanged, no removal evidence, T3->T5 and T3->T6, /api/fs/list latency+bytes.
//
// Opt-in:
//
//	INDEXCORE_P0_LIVE_BASE_URL=http://127.0.0.1:5244 \
//	INDEXCORE_P0_LIVE_USER=test INDEXCORE_P0_LIVE_PASS=... \
//	INDEXCORE_TEST_DATABASE_URL=postgres://... \
//	go test ./internal/runtime/scan -run TestLiveP0PhaseA -v -count=1
//	go test ./internal/runtime/scan -run TestLiveP0PhaseB -v -count=1

const liveRootID = "f0000000-0000-0000-0000-0000000000d1"

func liveEnv(t *testing.T) (base, user, pass, scope, expect string) {
	t.Helper()
	base = os.Getenv("INDEXCORE_P0_LIVE_BASE_URL")
	if base == "" {
		t.Skip("set INDEXCORE_P0_LIVE_BASE_URL/INDEXCORE_P0_LIVE_USER/INDEXCORE_P0_LIVE_PASS to run live P0 probes")
	}
	user = os.Getenv("INDEXCORE_P0_LIVE_USER")
	pass = os.Getenv("INDEXCORE_P0_LIVE_PASS")
	scope = os.Getenv("INDEXCORE_P0_LIVE_SCOPE")
	if scope == "" {
		scope = "/"
	}
	expect = os.Getenv("INDEXCORE_P0_LIVE_EXPECT")
	os.Setenv("P0_LIVE_USER", user)
	os.Setenv("P0_LIVE_PASS", pass)
	return
}

func liveStore(t *testing.T, reset bool) *postgres.Store {
	t.Helper()
	pool := testutil.Pool(t)
	if reset {
		testutil.ResetSchema(t, pool)
	}
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return postgres.New(pool)
}

func liveConfigureRoot(t *testing.T, st *postgres.Store, base string, create bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.GetRoot(ctx, st.Pool(), liveRootID); err != nil {
		if !create {
			t.Fatalf("live root %s not found; run TestLiveP0PhaseA first: %v", liveRootID, err)
		}
		if err := st.CreateRoot(ctx, st.Pool(), liveRootID, []byte(`{}`), domain.RootActive); err != nil {
			t.Fatalf("create root: %v", err)
		}
	}
	if err := st.UpsertRootPolicy(ctx, liveRootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	acfg, _ := json.Marshal(map[string]string{
		"base_url": base, "path": "/",
		"username_env": "P0_LIVE_USER", "password_env": "P0_LIVE_PASS",
	})
	if err := st.UpsertAdapterConfig(ctx, liveRootID, postgres.AdapterConfig{CollectorKind: "openlist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}
}

func livePresent(t *testing.T, qr query.Reader) map[string]string {
	t.Helper()
	ctx := context.Background()
	page, err := qr.ListActivePage(ctx, liveRootID, nil, 1000)
	if err != nil {
		t.Fatalf("Q6 list active: %v", err)
	}
	out := map[string]string{}
	for _, it := range page.Items {
		if it.CanonicalPath != nil {
			out[*it.CanonicalPath] = it.ResourceID
		}
	}
	return out
}

// liveListProbe logs in and issues one /api/fs/list observation, returning the
// HTTP latency and exact response body size (Round-2 evidence requirement).
func liveListProbe(t *testing.T, base, user, pass, path string, refresh bool) (time.Duration, int, int) {
	t.Helper()
	loginBody, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	loginResp, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	lb, _ := io.ReadAll(loginResp.Body)
	loginResp.Body.Close()
	var login struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lb, &login); err != nil || login.Data.Token == "" {
		t.Fatalf("login failed: %s", string(lb))
	}

	body, _ := json.Marshal(map[string]any{
		"path": path, "password": "", "page": 1, "per_page": 100, "refresh": refresh,
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/fs/list", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", login.Data.Token)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fs/list: %v", err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	latency := time.Since(start)

	var parsed struct {
		Code int `json:"code"`
		Data struct {
			Total int `json:"total"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rb, &parsed)
	return latency, len(rb), parsed.Data.Total
}

// TestLiveP0PhaseABaseline establishes the Canonical Inventory baseline via a
// full Service.Scan() (NOT scoped), recording each resource_id. Run this BEFORE
// the out-of-band upload.
func TestLiveP0PhaseABaseline(t *testing.T) {
	base, _, _, _, _ := liveEnv(t)
	ctx := context.Background()
	st := liveStore(t, true)
	liveConfigureRoot(t, st, base, true)

	svc := scan.New(st, "", "", 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t0 := time.Now()
	res, err := svc.Scan(ctx, liveRootID)
	if err != nil {
		t.Fatalf("baseline full scan: %v", err)
	}
	t.Logf("PHASE A baseline full scan: %d ms, outcome=%+v", time.Since(t0).Milliseconds(), res.Outcome)

	qr := postgres.NewQueryReader(st.Pool())
	for path, id := range livePresent(t, qr) {
		t.Logf("BASELINE RESOURCE %s -> %s", path, id)
	}
	t.Logf("PHASE A done: Canonical Inventory baseline established (run out-of-band upload, then TestLiveP0PhaseB)")
}

// TestLiveP0PhaseBScopedIncrement proves the scoped refresh is an increment:
// it must NOT ResetSchema, must see the baseline resource_ids unchanged, must
// add exactly the out-of-band file, must produce no removal evidence, and must
// report T3->T5, T3->T6 and the /api/fs/list latency/bytes.
func TestLiveP0PhaseBScopedIncrement(t *testing.T) {
	base, user, pass, scope, expect := liveEnv(t)
	ctx := context.Background()
	st := liveStore(t, false)
	liveConfigureRoot(t, st, base, false)

	qr := postgres.NewQueryReader(st.Pool())
	before := livePresent(t, qr)
	t.Logf("PHASE B before: %d PRESENT resources", len(before))
	if len(before) == 0 {
		t.Fatal("baseline is empty; run TestLiveP0PhaseA first")
	}

	svc := scan.New(st, "", "", 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// T3 -> T5: one canonical scoped refresh observation.
	t3 := time.Now()
	res, err := svc.ScanScope(ctx, liveRootID, scope, 100)
	t5 := time.Now()
	if err != nil {
		t.Fatalf("ScanScope: %v", err)
	}

	// T6: Q3 (get by id) + real Q4 (list children) + Q6 (active page) + Q7 (removed).
	t6a := time.Now()
	children, err := qr.ListResources(ctx, liveRootID, nil, query.ReadOptions{}, nil, 1000)
	if err != nil {
		t.Fatalf("Q4 list resources: %v", err)
	}
	after := livePresent(t, qr)
	removed, err := qr.ListRemovedPage(ctx, liveRootID, nil, 1000)
	if err != nil {
		t.Fatalf("Q7 removed page: %v", err)
	}
	var q3ok int
	for _, id := range after {
		if v, _ := qr.GetResource(ctx, id, query.ReadOptions{}); v != nil {
			q3ok++
		}
	}
	t6 := time.Now()

	// Report observed sets + metrics FIRST, so diagnostics print even if an
	// invariant below fails.
	var added []string
	for path := range after {
		if _, ok := before[path]; !ok {
			added = append(added, path)
		}
	}
	latency, bytesN, total := liveListProbe(t, base, user, pass, scope, true)
	t.Logf("PHASE B: outcome=%+v", res.Outcome)
	t.Logf("PHASE B: before=%d after=%d, Q4 children=%d, Q3 resolved=%d/%d",
		len(before), len(after), len(children.Items), q3ok, len(after))
	for p, id := range after {
		t.Logf("PHASE B after resource %s -> %s", p, id)
	}
	t.Logf("PHASE B: added=%v", added)
	t.Logf("METRIC T3->T5 = %d ms", t5.Sub(t3).Milliseconds())
	t.Logf("METRIC T3->T6 = %d ms (includes Q3/Q4/Q6/Q7)", t6.Sub(t6a).Milliseconds()+t5.Sub(t3).Milliseconds())
	t.Logf("METRIC /api/fs/list refresh=true: HTTP latency=%d ms, response_bytes=%d, total=%d",
		latency.Milliseconds(), bytesN, total)

	// Invariants.
	for path, id := range before {
		got, ok := after[path]
		if !ok {
			t.Fatalf("baseline resource %s disappeared after scoped refresh", path)
		}
		if got != id {
			t.Fatalf("baseline resource %s changed id: %s -> %s", path, id, got)
		}
	}
	if len(added) != 1 {
		t.Fatalf("scoped refresh must add exactly one out-of-band resource, added=%v", added)
	}
	if expect != "" && added[0] != expect {
		t.Fatalf("expected newly added %q, got %q", expect, added[0])
	}
	if len(removed.Items) != 0 {
		t.Fatalf("scoped refresh must produce no removal, got %d", len(removed.Items))
	}
	var badEvidence int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM index_canonical_resource
		  WHERE root_id=$1::uuid AND resource_presence='PRESENT'
		    AND (removal_evidence_state <> 'NONE' OR missing_since IS NOT NULL OR consecutive_complete_missing <> 0)`,
		liveRootID).Scan(&badEvidence); err != nil {
		t.Fatal(err)
	}
	if badEvidence != 0 {
		t.Fatalf("scoped refresh must not create removal evidence, %d PRESENT rows carry it", badEvidence)
	}
}
