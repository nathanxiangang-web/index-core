package scan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Gated live P0 probes (Issue #62 + Architect live-evidence rework, Round 5).
//
// Phase B routes IndexCore through a TEST-ONLY transparent proxy in front of the
// real OpenList, so the metrics captured are for the ONE canonical
// `POST /api/fs/list refresh=true` observation that ScanScope actually issues —
// not for an extra probe request. The proxy also proves that exactly one
// canonical refresh=true call happens per scope attempt.
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

func liveConfigureRoot(t *testing.T, st *postgres.Store, baseURL string, create bool) {
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
		"base_url": baseURL, "path": "/",
		"username_env": "P0_LIVE_USER", "password_env": "P0_LIVE_PASS",
	})
	if err := st.UpsertAdapterConfig(ctx, liveRootID, postgres.AdapterConfig{CollectorKind: "openlist", Config: acfg}); err != nil {
		t.Fatalf("adapter: %v", err)
	}
}

func livePresent(t *testing.T, qr query.Reader) map[string]string {
	t.Helper()
	page, err := qr.ListActivePage(context.Background(), liveRootID, nil, 1000)
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

// listProxy is a test-only transparent reverse proxy that records the /api/fs/list
// observations passing through it (status, latency, response bytes, total, refresh).
type listProxy struct {
	target *url.URL
	mu     sync.Mutex
	calls  []proxyCall
}

type proxyCall struct {
	refresh bool
	status  int
	latency time.Duration
	bytes   int
	total   int
}

func newListProxy(t *testing.T, target string) (*listProxy, *httptest.Server) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	p := &listProxy{target: u}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, err := http.NewRequestWithContext(r.Context(), r.Method, p.target.String()+r.URL.Path, bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		req.Header = r.Header.Clone()
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		latency := time.Since(start)

		if r.URL.Path == "/api/fs/list" {
			var q struct {
				Refresh bool `json:"refresh"`
			}
			_ = json.Unmarshal(body, &q)
			var rr struct {
				Data struct {
					Total int `json:"total"`
				} `json:"data"`
			}
			_ = json.Unmarshal(rb, &rr)
			p.mu.Lock()
			p.calls = append(p.calls, proxyCall{
				refresh: q.Refresh, status: resp.StatusCode, latency: latency,
				bytes: len(rb), total: rr.Data.Total,
			})
			p.mu.Unlock()
		}

		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(rb)
	}))
	return p, srv
}

func (p *listProxy) refreshCalls() []proxyCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []proxyCall
	for _, c := range p.calls {
		if c.refresh {
			out = append(out, c)
		}
	}
	return out
}

// TestLiveP0PhaseABaseline establishes the Canonical Inventory baseline via a
// full Service.Scan() (NOT scoped), recording each resource_id. Run BEFORE the
// out-of-band upload.
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
	t.Logf("PHASE A done: baseline established (run out-of-band upload, then TestLiveP0PhaseB)")
}

// TestLiveP0PhaseBScopedIncrement proves the scoped refresh is an increment, via
// a transparent proxy that captures the single canonical refresh=true observation.
func TestLiveP0PhaseBScopedIncrement(t *testing.T) {
	base, _, _, scope, expect := liveEnv(t)
	ctx := context.Background()
	st := liveStore(t, false)

	// Route IndexCore through the recording proxy (test-only instrumentation).
	proxy, srv := newListProxy(t, base)
	defer srv.Close()
	liveConfigureRoot(t, st, srv.URL, false)

	qr := postgres.NewQueryReader(st.Pool())
	before := livePresent(t, qr)
	t.Logf("PHASE B before: %d PRESENT resources", len(before))
	if len(before) == 0 {
		t.Fatal("baseline is empty; run TestLiveP0PhaseA first")
	}

	svc := scan.New(st, "", "", 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// T3 -> T5: one canonical scoped refresh observation (through the proxy).
	t3 := time.Now()
	res, err := svc.ScanScope(ctx, liveRootID, scope, 100)
	t5 := time.Now()
	if err != nil {
		t.Fatalf("ScanScope: %v", err)
	}

	// T6: Q3 (get by id) + real Q4 (list children) + Q6 (active page) + Q7 (removed).
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

	var added []string
	for path := range after {
		if _, ok := before[path]; !ok {
			added = append(added, path)
		}
	}

	// Metrics + observed sets first.
	canonical := proxy.refreshCalls()
	t.Logf("PHASE B: outcome=%+v", res.Outcome)
	t.Logf("PHASE B: before=%d after=%d, Q4 children=%d, Q3 resolved=%d/%d",
		len(before), len(after), len(children.Items), q3ok, len(after))
	for p, id := range after {
		t.Logf("PHASE B after resource %s -> %s", p, id)
	}
	t.Logf("PHASE B: added=%v", added)
	t.Logf("METRIC canonical refresh=true count = %d", len(canonical))
	if len(canonical) == 1 {
		c := canonical[0]
		t.Logf("METRIC canonical /api/fs/list refresh=true: status=%d latency=%d ms response_bytes=%d total=%d",
			c.status, c.latency.Milliseconds(), c.bytes, c.total)
	}
	t.Logf("METRIC T3->T5 = %d ms", t5.Sub(t3).Milliseconds())
	t.Logf("METRIC T3->T6 = %d ms (exact window, includes Q3/Q4/Q6/Q7)", t6.Sub(t3).Milliseconds())

	// Invariants.
	if len(canonical) != 1 {
		t.Fatalf("exactly one canonical refresh=true observation required, got %d", len(canonical))
	}
	if canonical[0].status != http.StatusOK {
		t.Fatalf("canonical refresh status = %d, want 200", canonical[0].status)
	}
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
	// Q4 must expose the new resource, with the SAME id as Q3 and Q6.
	var q4ID string
	for _, it := range children.Items {
		if it.CanonicalPath != nil && *it.CanonicalPath == added[0] {
			q4ID = it.ResourceID
		}
	}
	q6ID := after[added[0]]
	if q4ID == "" {
		t.Fatalf("Q4 must expose the newly added resource %q", added[0])
	}
	if q4ID != q6ID {
		t.Fatalf("Q4/Q6 disagree on %q id: %s vs %s", added[0], q4ID, q6ID)
	}
	if q3, _ := qr.GetResource(ctx, q4ID, query.ReadOptions{}); q3 == nil || q3.ResourceID != q4ID {
		t.Fatalf("Q3 must resolve the same resource id %s", q4ID)
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
	t.Logf("PHASE B PASS: Q3/Q4/Q6 agree on %q id=%s; canonical refresh count=1", added[0], q4ID)
}
