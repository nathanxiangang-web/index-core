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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// P1 live hot-scope polling probe (Issue #66). Env-gated; test-only.
//
//	INDEXCORE_P1_LIVE_BASE_URL=http://127.0.0.1:5244 \
//	INDEXCORE_P1_LIVE_USER=... INDEXCORE_P1_LIVE_PASS=... \
//	INDEXCORE_P1_LIVE_SCOPES="/,/hotA,/hotB" \
//	INDEXCORE_P1_LIVE_EXPECT_SCOPE=/hotA INDEXCORE_P1_LIVE_EXPECT_PATH=/hotA/new.txt \
//	INDEXCORE_TEST_DATABASE_URL=postgres://... \
//	go test ./internal/runtime/scan -run TestLiveP1HotScopesPhaseA -v -count=1
//	[operator uploads the new file out-of-band into the HOT scope]
//	go test ./internal/runtime/scan -run TestLiveP1HotScopesPhaseB -v -count=1

func liveP1Env(t *testing.T) (base, user, pass string, scopes []string, expectPath string) {
	t.Helper()
	base = os.Getenv("INDEXCORE_P1_LIVE_BASE_URL")
	if base == "" {
		t.Skip("set INDEXCORE_P1_LIVE_BASE_URL / _USER / _PASS / _SCOPES to run the live P1 probe")
	}
	user = os.Getenv("INDEXCORE_P1_LIVE_USER")
	pass = os.Getenv("INDEXCORE_P1_LIVE_PASS")
	raw := os.Getenv("INDEXCORE_P1_LIVE_SCOPES")
	if raw == "" {
		t.Fatal("INDEXCORE_P1_LIVE_SCOPES is required (comma-separated root-relative HOT scope paths)")
	}
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			scopes = append(scopes, s)
		}
	}
	expectPath = os.Getenv("INDEXCORE_P1_LIVE_EXPECT_PATH")
	os.Setenv("P0_LIVE_USER", user)
	os.Setenv("P0_LIVE_PASS", pass)
	return
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// p1Call is one /api/fs/list observation captured by the recording proxy.
type p1Call struct {
	path    string
	refresh bool
	status  int
	latency time.Duration
	bytes   int
	total   int
}

type p1Proxy struct {
	target *url.URL
	mu     sync.Mutex
	calls  []p1Call
}

func newP1Proxy(t *testing.T, target string) (*p1Proxy, *httptest.Server) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	p := &p1Proxy{target: u}
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
				Path    string `json:"path"`
				Refresh bool   `json:"refresh"`
			}
			_ = json.Unmarshal(body, &q)
			var rr struct {
				Data struct {
					Total int `json:"total"`
				} `json:"data"`
			}
			_ = json.Unmarshal(rb, &rr)
			p.mu.Lock()
			p.calls = append(p.calls, p1Call{
				path: q.Path, refresh: q.Refresh, status: resp.StatusCode,
				latency: latency, bytes: len(rb), total: rr.Data.Total,
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

func (p *p1Proxy) refreshCallsByPath() map[string][]p1Call {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string][]p1Call{}
	for _, c := range p.calls {
		if c.refresh {
			out[c.path] = append(out[c.path], c)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestLiveP1HotScopesPhaseABaseline establishes the IndexCore baseline for all
// configured HOT scopes (one bounded cycle).
func TestLiveP1HotScopesPhaseABaseline(t *testing.T) {
	base, _, _, scopes, _ := liveP1Env(t)
	ctx := context.Background()
	st := liveStore(t, true)
	liveConfigureRoot(t, st, base, true)

	lim := defaultPollLimits()
	svc := scan.New(st, "", "", 30*time.Second, discardLogger())
	h := newPollHarness(lim, nil, func(ctx context.Context, scope string) (scan.Result, error) {
		return svc.ScanScope(ctx, liveRootID, scope, lim.maxEntriesPerScope)
	})
	for _, s := range scopes {
		if !h.addScope(s, cadenceHot) {
			t.Fatalf("add hot scope %s (max_hot_scopes=%d)", s, lim.maxHotScopes)
		}
	}
	r := h.runCycle(ctx)
	t.Logf("PHASE A baseline cycle: due=%d polled=%v wall=%s budget_exhausted=%v",
		r.due, r.polledPaths(), r.wallTime, r.budgetExhausted)

	qr := postgres.NewQueryReader(st.Pool())
	for p := range livePresent(t, qr) {
		t.Logf("BASELINE RESOURCE %s", p)
	}
	t.Logf("PHASE A done at %s — run the out-of-band upload, then Phase B within one interval",
		time.Now().UTC().Format(time.RFC3339))
}

// TestLiveP1HotScopesPhaseBCycle runs the next bounded poll cycle through a
// recording proxy and reports per-scope + whole-cycle metrics.
func TestLiveP1HotScopesPhaseBCycle(t *testing.T) {
	base, _, _, scopes, expectPath := liveP1Env(t)
	ctx := context.Background()
	st := liveStore(t, false)
	if _, err := st.GetRoot(ctx, st.Pool(), liveRootID); err != nil {
		t.Fatalf("run TestLiveP1HotScopesPhaseABaseline first: %v", err)
	}
	proxy, srv := newP1Proxy(t, base)
	defer srv.Close()
	liveConfigureRoot(t, st, srv.URL, false)

	qr := postgres.NewQueryReader(st.Pool())
	before := livePresent(t, qr)

	lim := defaultPollLimits()
	svc := scan.New(st, "", "", 30*time.Second, discardLogger())
	h := newPollHarness(lim, nil, func(ctx context.Context, scope string) (scan.Result, error) {
		return svc.ScanScope(ctx, liveRootID, scope, lim.maxEntriesPerScope)
	})
	for _, s := range scopes {
		if !h.addScope(s, cadenceHot) {
			t.Fatalf("add hot scope %s", s)
		}
	}

	t3 := time.Now()
	r := h.runCycle(ctx)
	t5 := time.Now()

	after := livePresent(t, qr)
	children, err := qr.ListResources(ctx, liveRootID, nil, query.ReadOptions{}, nil, 1000)
	if err != nil {
		t.Fatalf("Q4: %v", err)
	}
	removed, err := qr.ListRemovedPage(ctx, liveRootID, nil, 1000)
	if err != nil {
		t.Fatalf("Q7: %v", err)
	}
	var q3ok int
	for _, id := range after {
		if v, _ := qr.GetResource(ctx, id, query.ReadOptions{}); v != nil {
			q3ok++
		}
	}
	t6 := time.Now()

	var added []string
	for p := range after {
		if _, ok := before[p]; !ok {
			added = append(added, p)
		}
	}

	byPath := proxy.refreshCallsByPath()
	const pageSize = 200 // 115 Open prototype default (§6)
	t.Logf("PHASE B cycle: due=%d polled=%v wall=%s budget_exhausted=%v",
		r.due, r.polledPaths(), r.wallTime, r.budgetExhausted)
	for _, s := range scopes {
		calls := byPath[s]
		call := p1Call{}
		if len(calls) > 0 {
			call = calls[len(calls)-1]
		}
		est := 0
		if call.total > 0 {
			est = (call.total + pageSize - 1) / pageSize
		}
		t.Logf("METRIC scope=%s total=%d canonical_refresh_count=%d status=%d latency_ms=%d response_bytes=%d page_size=%d derived_provider_pages=%d (derived, not measured)",
			s, call.total, len(calls), call.status, call.latency.Milliseconds(), call.bytes, pageSize, est)
	}
	t.Logf("METRIC cycle due=%d polled=%d wall_ms=%d budget_exhausted=%v",
		r.due, len(r.polledPaths()), r.wallTime.Milliseconds(), r.budgetExhausted)
	t.Logf("METRIC T3->T5 = %d ms; T3->T6 = %d ms", t5.Sub(t3).Milliseconds(), t6.Sub(t3).Milliseconds())
	t.Logf("PHASE B added=%v (before=%d after=%d)", added, len(before), len(after))

	if expectPath != "" {
		if !contains(added, expectPath) {
			t.Fatalf("expected the out-of-band file %s to be discovered, added=%v", expectPath, added)
		}
		if len(added) != 1 {
			t.Fatalf("exactly one new resource must be added, got %v", added)
		}
	}
	if len(removed.Items) != 0 {
		t.Fatalf("polling must produce no removal evidence, got %d", len(removed.Items))
	}
	if q3ok != len(after) {
		t.Fatalf("Q3 must resolve all %d PRESENT resources, got %d", len(after), q3ok)
	}
	t.Logf("Q4 children=%d Q3 resolved=%d/%d — P1 cycle assertions passed", len(children.Items), q3ok, len(after))
}
