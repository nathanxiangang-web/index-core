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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// P1 live hot-scope polling probe (Issue #66, Round-1 rework). Env-gated; test-only.
//
// Phase A (baseline):
//
//	INDEXCORE_P1_LIVE_BASE_URL=http://127.0.0.1:5244 \
//	INDEXCORE_P1_LIVE_USER=... INDEXCORE_P1_LIVE_PASS=... \
//	INDEXCORE_P1_LIVE_SCOPES="/,/hotA,/hotB,/hotC" \
//	go test ./internal/runtime/scan -run TestLiveP1HotScopesPhaseABaseline -v -count=1
//
// [operator uploads the new file out-of-band, then records T1 / prev-poll timestamps]
//
// Phase B (next due poll, with stale-cache gate + change attribution):
//
//	INDEXCORE_P1_LIVE_BASE_URL=... INDEXCORE_P1_LIVE_USER=... INDEXCORE_P1_LIVE_PASS=... \
//	INDEXCORE_P1_LIVE_SCOPES=... INDEXCORE_P1_LIVE_EXPECT_SCOPE=/hotA \
//	INDEXCORE_P1_LIVE_EXPECT_PATH=/hotA/new.txt \
//	INDEXCORE_P1_LIVE_PAGE_SIZE=200 \
//	INDEXCORE_P1_LIVE_PREV_POLL_TS=<Phase-A RFC3339> INDEXCORE_P1_LIVE_T1_TS=<upload RFC3339> \
//	INDEXCORE_P1_LIVE_HOT_INTERVAL=120 \
//	go test ./internal/runtime/scan -run TestLiveP1HotScopesPhaseBCycle -v -count=1

func p1Base(t *testing.T) (base, user, pass string, scopes []string, expectScope, expectPath string, pageSize int) {
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
	expectScope = os.Getenv("INDEXCORE_P1_LIVE_EXPECT_SCOPE")
	expectPath = os.Getenv("INDEXCORE_P1_LIVE_EXPECT_PATH")
	pageSize = atoiOr(t, "INDEXCORE_P1_LIVE_PAGE_SIZE", 0)
	os.Setenv("P0_LIVE_USER", user)
	os.Setenv("P0_LIVE_PASS", pass)
	return
}

func atoiOr(t *testing.T, key string, def int) int {
	t.Helper()
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("%s must be an integer: %v", key, err)
		}
		return n
	}
	return def
}

func mustParseTS(t *testing.T, key string) time.Time {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s is required (RFC3339 timestamp)", key)
	}
	ts, err := time.Parse(time.RFC3339, v)
	if err != nil {
		t.Fatalf("%s must be RFC3339: %v", key, err)
	}
	return ts
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
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

// refreshTotal counts EVERY canonical refresh=true observation seen by the proxy,
// across all paths — used to prove there is no extra refresh path beyond the
// polled scopes.
func (p *p1Proxy) refreshTotal() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if c.refresh {
			n++
		}
	}
	return n
}

// p1ReadOnlyList performs ONE read-only refresh=false observation (never mutating
// the cache) and returns status, total, names, bytes.
func p1ReadOnlyList(t *testing.T, base, user, pass, path string, perPage int) (int, int, int, []string, int) {
	t.Helper()
	loginBody, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	lr, err := http.Post(base+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	lb, _ := io.ReadAll(lr.Body)
	lr.Body.Close()
	var login struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lb, &login); err != nil || login.Data.Token == "" {
		t.Fatalf("login failed: %s", string(lb))
	}

	body, _ := json.Marshal(map[string]any{"path": path, "password": "", "page": 1, "per_page": perPage, "refresh": false})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/fs/list", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", login.Data.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fs/list: %v", err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var parsed struct {
		Data struct {
			Total   int `json:"total"`
			Content []struct {
				Name string `json:"name"`
			} `json:"content"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rb, &parsed)
	names := make([]string, 0, len(parsed.Data.Content))
	for _, c := range parsed.Data.Content {
		names = append(names, c.Name)
	}
	return resp.StatusCode, parsed.Data.Total, len(parsed.Data.Content), names, len(rb)
}

// TestLiveP1HotScopesPhaseABaseline establishes the IndexCore baseline for all
// configured HOT scopes (one bounded cycle) and prints the poll timestamp.
func TestLiveP1HotScopesPhaseABaseline(t *testing.T) {
	base, _, _, scopes, _, _, _ := p1Base(t)
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
			t.Fatalf("add hot scope %s (max_hot_scopes=%d; 3-5 expected)", s, lim.maxHotScopes)
		}
	}
	r := h.runCycle(ctx)
	t.Logf("PHASE A baseline cycle: due=%d polled=%v wall=%s budget_exhausted=%v",
		r.due, r.polledPaths(), r.wallTime, r.budgetExhausted)

	qr := postgres.NewQueryReader(st.Pool())
	for p, id := range livePresent(t, qr) {
		t.Logf("BASELINE RESOURCE %s -> %s", p, id)
	}
	t.Logf("PHASE_A_POLL_TS=%s", time.Now().UTC().Format(time.RFC3339))
	t.Logf("PHASE A done — upload out-of-band, record T1, then run Phase B with PREV_POLL_TS/T1_TS env")
}

// TestLiveP1HotScopesPhaseBCycle runs the NEXT DUE poll cycle through a recording
// proxy, with a read-only stale-cache gate, change attribution and full safety
// assertions (Issue #66 Round-1 BLOCKERs 1-6).
func TestLiveP1HotScopesPhaseBCycle(t *testing.T) {
	base, user, pass, scopes, expectScope, expectPath, pageSize := p1Base(t)
	if len(scopes) < 3 || len(scopes) > 5 {
		t.Fatalf("live HOT set must be 3-5 scopes (Issue #66), got %d: %v", len(scopes), scopes)
	}
	if expectScope == "" || expectPath == "" {
		t.Fatal("INDEXCORE_P1_LIVE_EXPECT_SCOPE and INDEXCORE_P1_LIVE_EXPECT_PATH are required")
	}
	if pageSize <= 0 {
		t.Fatal("INDEXCORE_P1_LIVE_PAGE_SIZE is required (the actual 115 Open page_size); do not guess")
	}
	// Input sanity (Round 2): the expected scope must be one of the HOT scopes,
	// and the expected path must be a direct child of it.
	if !contains(scopes, expectScope) {
		t.Fatalf("INDEXCORE_P1_LIVE_EXPECT_SCOPE %q must be one of the HOT scopes %v", expectScope, scopes)
	}
	if got := parentOf(expectPath); got != expectScope {
		t.Fatalf("parentOf(EXPECT_PATH)=%q must equal EXPECT_SCOPE %q", got, expectScope)
	}
	prevPoll := mustParseTS(t, "INDEXCORE_P1_LIVE_PREV_POLL_TS")
	t1 := mustParseTS(t, "INDEXCORE_P1_LIVE_T1_TS")
	interval := time.Duration(atoiOr(t, "INDEXCORE_P1_LIVE_HOT_INTERVAL", 120)) * time.Second
	ctx := context.Background()
	lim := defaultPollLimits()
	lim.minimumScopeInterval = interval

	st := liveStore(t, false)
	if _, err := st.GetRoot(ctx, st.Pool(), liveRootID); err != nil {
		t.Fatalf("run TestLiveP1HotScopesPhaseABaseline first: %v", err)
	}

	// BLOCKER 1 — read-only stale-cache gate: the expected file must still be
	// absent via refresh=false BEFORE the P1 cycle. This must not refresh the
	// cache. It must fetch the WHOLE directory in one coherent response
	// (per_page = maxEntries+1) and require total == len(content), so a page-2
	// target cannot be mis-read as "not visible" (Round 2).
	gateAt := time.Now().UTC()
	status, total, contentCount, names, bytesN := p1ReadOnlyList(t, base, user, pass, expectScope, lim.maxEntriesPerScope+1)
	gateNames := make([]string, len(names))
	copy(gateNames, names)
	t.Logf("STALE GATE at %s: scope=%s status=%d total=%d content=%d bytes=%d names=%v",
		gateAt.Format(time.RFC3339), expectScope, status, total, contentCount, bytesN, gateNames)
	if status != http.StatusOK {
		t.Fatalf("stale-gate refresh=false must be 200, got %d", status)
	}
	if total != contentCount {
		t.Fatalf("stale-gate must be a coherent single response: total=%d content=%d", total, contentCount)
	}
	if contains(names, baseName(expectPath)) {
		t.Fatalf("STALE-GATE FAILED: %s already visible via refresh=false; experiment is NOT JUDICABLE", expectPath)
	}

	proxy, srv := newP1Proxy(t, base)
	defer srv.Close()
	liveConfigureRoot(t, st, srv.URL, false)

	qr := postgres.NewQueryReader(st.Pool())
	before := livePresent(t, qr)

	// BLOCKER 2 — represent the NEXT DUE poll: seed lastPolled from the previous
	// poll and require the interval to have actually elapsed (not time-zero due).
	now := time.Now().UTC()
	if now.Sub(prevPoll) < interval {
		t.Fatalf("interval not elapsed: now-prevPoll=%s < %s; Phase B must be the next due poll",
			now.Sub(prevPoll), interval)
	}
	pollAt := time.Now().UTC()
	svc := scan.New(st, "", "", 30*time.Second, discardLogger())
	h := newPollHarness(lim, func() time.Time { return time.Now().UTC() }, func(ctx context.Context, scope string) (scan.Result, error) {
		return svc.ScanScope(ctx, liveRootID, scope, lim.maxEntriesPerScope)
	})
	for _, s := range scopes {
		if !h.addScope(s, cadenceHot) {
			t.Fatalf("add hot scope %s", s)
		}
	}
	// Seed each scope's lastPolled with the previous poll timestamp.
	for _, s := range h.scopes {
		s.lastPolled = prevPoll
	}

	due := h.dueScopes(now)
	if len(due) != len(scopes) {
		t.Fatalf("expected all %d HOT scopes due at the next poll, got %d", len(scopes), len(due))
	}

	t3 := time.Now().UTC()
	r := h.runCycle(ctx)
	t5 := time.Now().UTC()

	after := livePresent(t, qr)
	removed, err := qr.ListRemovedPage(ctx, liveRootID, nil, 1000)
	if err != nil {
		t.Fatalf("Q7: %v", err)
	}
	// BLOCKER 4 — real Q4 for the expected PARENT directory (not root-level only).
	parentID := ""
	parentPath := parentOf(expectPath)
	if parentPath != "/" && parentPath != "" {
		prows, err := st.PresentResourcesAtPath(ctx, st.Pool(), liveRootID, parentPath)
		if err != nil || len(prows) != 1 {
			t.Fatalf("expected parent %s must be exactly one PRESENT canonical directory, got %d (%v)", parentPath, len(prows), err)
		}
		parentID = prows[0].ResourceID
	}
	var q4Params *string
	if parentID != "" {
		q4Params = &parentID
	}
	children, err := qr.ListResources(ctx, liveRootID, q4Params, query.ReadOptions{}, nil, 1000)
	if err != nil {
		t.Fatalf("Q4 for parent %q: %v", parentPath, err)
	}
	var q4ID string
	for _, it := range children.Items {
		if it.CanonicalPath != nil && *it.CanonicalPath == expectPath {
			q4ID = it.ResourceID
		}
	}
	var q3ok bool
	if q4ID != "" {
		if v, _ := qr.GetResource(ctx, q4ID, query.ReadOptions{}); v != nil && v.ResourceID == q4ID {
			q3ok = true
		}
	}
	t6 := time.Now().UTC()

	// Metrics + observed sets first.
	var added []string
	for p := range after {
		if _, ok := before[p]; !ok {
			added = append(added, p)
		}
	}
	byPath := proxy.refreshCallsByPath()
	polled := r.polledPaths()
	t.Logf("PHASE B cycle: due=%d polled=%v wall=%s budget_exhausted=%v", r.due, polled, r.wallTime, r.budgetExhausted)
	for _, s := range polled {
		calls := byPath[s]
		c := p1Call{}
		if len(calls) > 0 {
			c = calls[len(calls)-1]
		}
		est := 0
		if c.total > 0 {
			est = (c.total + pageSize - 1) / pageSize
		}
		t.Logf("METRIC scope=%s total=%d canonical_refresh_count=%d status=%d latency_ms=%d response_bytes=%d page_size=%d derived_provider_pages=%d (derived, not measured)",
			s, c.total, len(calls), c.status, c.latency.Milliseconds(), c.bytes, pageSize, est)
	}
	t.Logf("METRIC cycle due=%d polled=%d wall_ms=%d budget_exhausted=%v", r.due, len(polled), r.wallTime.Milliseconds(), r.budgetExhausted)
	t.Logf("PHASE B added=%v (before=%d after=%d)", added, len(before), len(after))

	// Round 2 — the cycle must actually poll EVERY due HOT scope within budget.
	if r.due != len(scopes) {
		t.Fatalf("cycle due=%d must equal all %d HOT scopes", r.due, len(scopes))
	}
	if len(polled) != r.due || len(polled) != len(scopes) {
		t.Fatalf("cycle must poll all due HOT scopes: due=%d polled=%d scopes=%d", r.due, len(polled), len(scopes))
	}
	if r.budgetExhausted {
		t.Fatal("cycle wall-time budget must not be exhausted in this bounded live run")
	}
	// Round 2 — no extra refresh path anywhere: total refresh=true observations
	// must equal exactly the number of polled scopes.
	if got := proxy.refreshTotal(); got != len(polled) {
		t.Fatalf("total canonical refresh=true observations=%d must equal polled scopes=%d", got, len(polled))
	}

	// BLOCKER 3 — change attribution: expected scope mutated; others did not.
	mutated := r.mutatedByPath()
	if !mutated[expectScope] {
		t.Fatalf("expected changed scope %s must report Mutated=true, got %v", expectScope, mutated)
	}
	for _, s := range polled {
		if s == expectScope {
			continue
		}
		if mutated[s] {
			t.Fatalf("unchanged hot scope %s must not canonically mutate, got %v", s, mutated)
		}
	}

	// BLOCKER 5 — baseline identity / no-removal.
	for p, id := range before {
		got, ok := after[p]
		if !ok {
			t.Fatalf("baseline resource %s disappeared after the poll cycle", p)
		}
		if got != id {
			t.Fatalf("baseline resource %s changed id: %s -> %s", p, id, got)
		}
	}
	if len(added) != 1 || added[0] != expectPath {
		t.Fatalf("exactly the expected new resource %s must be added, got %v", expectPath, added)
	}
	if len(removed.Items) != 0 {
		t.Fatalf("polling must produce no removal evidence, got %d", len(removed.Items))
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
		t.Fatalf("polling must not create removal evidence, %d PRESENT rows carry it", badEvidence)
	}

	// BLOCKER 6 — per-scope canonical request assertions.
	for _, s := range scopes {
		n := len(byPath[s])
		if contains(polled, s) {
			if n != 1 {
				t.Fatalf("polled scope %s must issue exactly one canonical refresh=true, got %d", s, n)
			}
			if byPath[s][0].status != http.StatusOK {
				t.Fatalf("polled scope %s canonical refresh must be 200, got %d", s, byPath[s][0].status)
			}
		} else if n != 0 {
			t.Fatalf("scope %s was not polled but issued %d canonical refresh(es)", s, n)
		}
	}

	// BLOCKER 4 — nested real Q4 must expose the new resource; Q3/Q4/Q6 agree.
	if q4ID == "" {
		t.Fatalf("real Q4 for parent %q must expose %s, got %d children", parentPath, expectPath, len(children.Items))
	}
	if !q3ok {
		t.Fatalf("Q3 must resolve Q4's resource id %s", q4ID)
	}
	if after[expectPath] != q4ID {
		t.Fatalf("Q3/Q4/Q6 must agree on %s id: Q4=%s Q6=%s", expectPath, q4ID, after[expectPath])
	}

	// BLOCKER 2 — detection within one configured interval.
	visibility := t6.Sub(t1)
	t.Logf("METRIC T1=%s pollAt=%s T3=%s T5=%s T6=%s", t1.Format(time.RFC3339), pollAt.Format(time.RFC3339),
		t3.Format(time.RFC3339), t5.Format(time.RFC3339), t6.Format(time.RFC3339))
	t.Logf("METRIC T6-T1 = %s (hot interval %s)", visibility, interval)
	if visibility > interval {
		t.Fatalf("detection latency %s exceeds one HOT interval %s", visibility, interval)
	}
	t.Logf("PHASE B PASS: %s discovered within one interval; Q3/Q4/Q6 agree; no removal; baseline preserved", expectPath)
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func parentOf(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/"
	}
	return p[:i]
}
