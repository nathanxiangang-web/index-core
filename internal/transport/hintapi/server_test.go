package hintapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalhint"
	"github.com/nathanxiangang-web/index-core/internal/transport/hintapi"
)

const p9Token = "0123456789abcdef0123456789abcdef"
const p9RootID = "11111111-1111-4111-8111-111111111111"

type fakeIngester struct {
	mu      sync.Mutex
	calls   int
	last    incrementalhint.Request
	err     error
	work    state.DirtyScopeWork
	block   chan struct{}
	entered chan struct{}
}

func (f *fakeIngester) IngestOne(ctx context.Context, req incrementalhint.Request) (state.DirtyScopeWork, error) {
	f.mu.Lock()
	f.calls++
	f.last = req
	block, entered, err, work := f.block, f.entered, f.err, f.work
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return state.DirtyScopeWork{}, ctx.Err()
		}
	}
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if work.RootID == "" {
		work = state.DirtyScopeWork{RootID: req.RootID, ScopeKey: req.ScopeKey, WorkState: state.WorkPending, SignalSeq: 12}
	}
	return work, nil
}

func (f *fakeIngester) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeIngester) lastRequest() incrementalhint.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func (f *fakeIngester) reset() {
	f.mu.Lock()
	f.calls = 0
	f.last = incrementalhint.Request{}
	f.mu.Unlock()
}

func p9Start(t *testing.T, ing hintapi.Ingester) string {
	t.Helper()
	s, err := hintapi.New(hintapi.Deps{Ingester: ing, Token: p9Token})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return "http://" + ln.Addr().String()
}

func p9PostErr(base, token, contentType string, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, base+hintapi.HintPath, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b, nil
}

func p9Post(t *testing.T, base, token, contentType string, body []byte) (*http.Response, []byte) {
	t.Helper()
	resp, b, err := p9PostErr(base, token, contentType, body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp, b
}

func p9Body(rootID, scope, reason string) []byte {
	m := map[string]string{"root_id": rootID, "scope_key": scope}
	if reason != "" {
		m["reason"] = reason
	}
	b, _ := json.Marshal(m)
	return b
}

func TestP9NewRejectsBadDeps(t *testing.T) {
	if _, err := hintapi.New(hintapi.Deps{Token: p9Token}); err == nil {
		t.Fatal("nil ingester must be rejected")
	}
	if _, err := hintapi.New(hintapi.Deps{Ingester: &fakeIngester{}, Token: "short"}); err == nil {
		t.Fatal("short token must be rejected")
	}
}

func TestP9AuthFailuresNeverReachIngester(t *testing.T) {
	ing := &fakeIngester{}
	base := p9Start(t, ing)
	cases := []struct{ name, token string }{
		{"missing", ""},
		{"wrong", strings.Repeat("z", 32)},
		{"short", "BearerX"},
	}
	for _, tc := range cases {
		resp, body := p9Post(t, base, tc.token, "application/json", p9Body(p9RootID, "/a", ""))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", tc.name, resp.StatusCode)
		}
		if got := resp.Header.Get("WWW-Authenticate"); got != "Bearer" {
			t.Fatalf("%s: WWW-Authenticate = %q", tc.name, got)
		}
		var out map[string]string
		if err := json.Unmarshal(body, &out); err != nil || out["error"] != "unauthorized" {
			t.Fatalf("%s: unexpected body %s", tc.name, body)
		}
		if strings.Contains(string(body), p9Token) {
			t.Fatalf("%s: response must never include the token", tc.name)
		}
	}
	if ing.callCount() != 0 {
		t.Fatalf("auth failures must not call the ingester, got %d", ing.callCount())
	}
}

func TestP9MalformedAuthorizationScheme(t *testing.T) {
	ing := &fakeIngester{}
	base := p9Start(t, ing)
	req, _ := http.NewRequest(http.MethodPost, base+hintapi.HintPath, bytes.NewReader(p9Body(p9RootID, "/a", "")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+p9Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if ing.callCount() != 0 {
		t.Fatal("malformed scheme must not call the ingester")
	}
}

func TestP9ValidRequestAcceptedOnce(t *testing.T) {
	ing := &fakeIngester{}
	base := p9Start(t, ing)
	resp, body := p9Post(t, base, p9Token, "application/json", p9Body(p9RootID, "/a/b", ""))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	if ing.callCount() != 1 {
		t.Fatalf("exactly one ingester call required, got %d", ing.callCount())
	}
	if got := ing.lastRequest(); got.RootID != p9RootID || got.ScopeKey != "/a/b" || got.Reason != "" {
		t.Fatalf("mapped request = %+v", got)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if out["status"] != "accepted" || out["root_id"] != p9RootID || out["scope_key"] != "/a/b" ||
		out["work_state"] != "PENDING" || out["signal_seq"].(float64) != 12 {
		t.Fatalf("response = %s", body)
	}
	if strings.Contains(string(body), p9Token) || strings.Contains(string(body), "pending_source_set") {
		t.Fatalf("response must not leak token/internals: %s", body)
	}
}

func TestP9StrictRequestParsing(t *testing.T) {
	ing := &fakeIngester{}
	base := p9Start(t, ing)
	big := append([]byte(`{"root_id":"`+p9RootID+`","scope_key":"/a","pad":"`), bytes.Repeat([]byte("x"), 5000)...)
	big = append(big, []byte(`"}`)...)

	cases := []struct {
		name        string
		contentType string
		body        []byte
		want        int
		wantCode    string
	}{
		{"non json content type", "text/plain", p9Body(p9RootID, "/a", ""), 415, "unsupported_media_type"},
		{"oversize body", "application/json", big, 413, "request_too_large"},
		{"malformed json", "application/json", []byte(`{"root_id":`), 400, "invalid_request"},
		{"unknown field", "application/json", []byte(`{"root_id":"` + p9RootID + `","scope_key":"/a","extra":1}`), 400, "invalid_request"},
		{"trailing value", "application/json", []byte(`{"root_id":"` + p9RootID + `","scope_key":"/a"} {}`), 400, "invalid_request"},
		{"empty root", "application/json", p9Body("", "/a", ""), 400, "invalid_request"},
		{"trailing slash scope", "application/json", p9Body(p9RootID, "/a/", ""), 400, "invalid_request"},
		{"dotdot scope", "application/json", p9Body(p9RootID, "/a/../b", ""), 400, "invalid_request"},
		{"relative scope", "application/json", p9Body(p9RootID, "a", ""), 400, "invalid_request"},
		{"bad reason", "application/json", p9Body(p9RootID, "/a", "MANUAL_VERIFY"), 400, "invalid_request"},
		{"charset parameter allowed", "application/json; charset=utf-8", p9Body(p9RootID, "/a", "DELETE_HINT"), 202, ""},
	}
	for _, tc := range cases {
		ing.reset()
		resp, body := p9Post(t, base, p9Token, tc.contentType, tc.body)
		if resp.StatusCode != tc.want {
			t.Fatalf("%s: status = %d want %d (%s)", tc.name, resp.StatusCode, tc.want, body)
		}
		if tc.want == http.StatusAccepted {
			if ing.callCount() != 1 {
				t.Fatalf("%s: ingester calls = %d, want 1", tc.name, ing.callCount())
			}
			continue
		}
		if ing.callCount() != 0 {
			t.Fatalf("%s: ingester must not be called, got %d", tc.name, ing.callCount())
		}
		var out map[string]string
		_ = json.Unmarshal(body, &out)
		if out["error"] != tc.wantCode {
			t.Fatalf("%s: body=%s want code %s", tc.name, body, tc.wantCode)
		}
	}
}

func TestP9IngesterErrorIsPrivate(t *testing.T) {
	ing := &fakeIngester{err: errors.New("postgres://user:supersecret@db/x")}
	base := p9Start(t, ing)
	resp, body := p9Post(t, base, p9Token, "application/json", p9Body(p9RootID, "/a", ""))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	var out map[string]string
	_ = json.Unmarshal(body, &out)
	if out["error"] != "ingest_unavailable" {
		t.Fatalf("body = %s", body)
	}
	for _, leaked := range []string{"supersecret", "postgres://", p9Token} {
		if strings.Contains(string(body), leaked) {
			t.Fatalf("error text must be opaque, leaked %q in %s", leaked, body)
		}
	}
}

func TestP9ConcurrencyBackpressure(t *testing.T) {
	ing := &fakeIngester{block: make(chan struct{})}
	base := p9Start(t, ing)

	var wg sync.WaitGroup
	for i := 0; i < hintapi.MaxInFlight; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _, err := p9PostErr(base, p9Token, "application/json", p9Body(p9RootID, "/a", ""))
			if err != nil {
				return
			}
			_ = resp.Body.Close()
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for ing.callCount() < hintapi.MaxInFlight && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := ing.callCount(); got != hintapi.MaxInFlight {
		t.Fatalf("in-flight = %d, want %d", got, hintapi.MaxInFlight)
	}

	resp, body := p9Post(t, base, p9Token, "application/json", p9Body(p9RootID, "/a", ""))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("fifth request status = %d (%s)", resp.StatusCode, body)
	}
	if resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q", resp.Header.Get("Retry-After"))
	}
	if got := ing.callCount(); got != hintapi.MaxInFlight {
		t.Fatalf("busy must not call the ingester, calls = %d", got)
	}

	close(ing.block)
	wg.Wait()

	resp2, body2 := p9Post(t, base, p9Token, "application/json", p9Body(p9RootID, "/a", ""))
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("after release status = %d (%s)", resp2.StatusCode, body2)
	}
}

func TestP9HintListenerHasNoQueryRoutes(t *testing.T) {
	ing := &fakeIngester{}
	base := p9Start(t, ing)
	resp, err := http.Get(base + "/v1/roots")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /v1/roots on hint listener = %d, want 404", resp.StatusCode)
	}
}

// TestP9ShutdownWaitsForRequestStillReadingBody proves the lifecycle gate admits
// a request BEFORE its body is read: even a request that is still uploading its
// body is tracked, so the writer lock can never be released while such a handler
// might still reach P8.
func TestP9ShutdownWaitsForRequestStillReadingBody(t *testing.T) {
	ing := &fakeIngester{}
	s, err := hintapi.New(hintapi.Deps{Ingester: ing, Token: p9Token})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(ln) }()

	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+hintapi.HintPath, pr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p9Token)
	go func() { _, _ = http.DefaultClient.Do(req) }()

	// Send only a partial body and keep the stream open so the handler is blocked
	// reading it.
	if _, err := pw.Write([]byte(`{"root_id":"`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	// Close admission and shut the listener down. The slow request is still being
	// read, so Shutdown may time out, but the lifecycle gate must stay closed.
	shCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_ = s.Shutdown(shCtx)
	cancel()

	drained := make(chan struct{})
	go func() { s.WaitHandlers(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("WaitHandlers must not return while a request is still being read")
	case <-time.After(300 * time.Millisecond):
	}

	// Completing the body lets the handler finish; only then may the gate drain.
	if _, err := pw.Write([]byte(`"scope_key":"/a","reason":"POSSIBLE_CHANGE"}`)); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("WaitHandlers must return after the handler completes")
	}
}
