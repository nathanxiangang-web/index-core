package sourcebudget

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func fastPolicy() Config {
	return Config{5 * time.Millisecond, 10 * time.Millisecond, 15 * time.Millisecond, 5 * time.Millisecond, 40 * time.Millisecond, 4}
}
func TestIssue138BudgetActualHTTPSharedGroupAndRedirect(t *testing.T) {
	var mu sync.Mutex
	times := []time.Time{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/unexpected", 302)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	m, _ := New(fastPolicy())
	a, _ := m.Client("rootA", srv.URL, "shared")
	b, _ := m.Client("rootB", srv.URL, "shared")
	started := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client := a
			if i%2 == 1 {
				client = b
			}
			res, e := client.Get(srv.URL + "/ok")
			if e != nil {
				t.Error(e)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}(i)
	}
	wg.Wait()
	if len(times) != 8 || time.Since(started) < 7*fastPolicy().Provider {
		t.Fatal("shared group aggregate spacing bypassed", times)
	}
	res, e := a.Get(srv.URL + "/redirect")
	if e != nil || res.StatusCode != 302 {
		t.Fatal(res, e)
	}
	res.Body.Close()
	if len(times) != 9 {
		t.Fatal("redirect followed", len(times))
	}
}
func TestIssue138BudgetDeterministicThreeLevels(t *testing.T) {
	for _, cfg := range []Config{{15 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, time.Second, time.Minute, 4}, {5 * time.Millisecond, 15 * time.Millisecond, 10 * time.Millisecond, time.Second, time.Minute, 4}, {5 * time.Millisecond, 10 * time.Millisecond, 15 * time.Millisecond, time.Second, time.Minute, 4}} {
		m, _ := New(cfg)
		at := time.Unix(1, 0)
		start := at
		m.now = func() time.Time { return at }
		m.wait = func(ctx context.Context, d time.Duration) error { at = at.Add(d); return ctx.Err() }
		for i := 0; i < 8; i++ {
			if e := m.acquire(context.Background(), "root", "group"); e != nil {
				t.Fatal(e)
			}
		}
		if at.Sub(start) != 7*15*time.Millisecond {
			t.Fatal("global/root/group bottleneck bypassed", at.Sub(start))
		}
	}
}
func TestIssue138BudgetCooldownRetryAfterAndCanceledWait(t *testing.T) {
	cfg := fastPolicy()
	m, _ := New(cfg)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.wait = func(ctx context.Context, d time.Duration) error { now = now.Add(d); return ctx.Err() }
	if e := m.acquire(context.Background(), "a", "group"); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := m.acquire(ctx, "b", "group"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if len(m.roots) != 1 {
		t.Fatal("canceled waiter allocated state")
	}
	m.Penalize("a", "group", 2*time.Hour)
	until := m.RootUntil("a")
	if until.Sub(now) != 2*time.Hour {
		t.Fatal("Retry-After shortened")
	}
	if e := m.acquire(context.Background(), "b", "group"); !errors.Is(e, ErrDeferred) {
		t.Fatal(e)
	}
	if m.RootUntil("b") != until {
		t.Fatal("shared cooldown missing from root retry floor")
	}
	now = until
	if e := m.acquire(context.Background(), "a", "group"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10; i++ {
		m.Penalize("a", "group", 0)
	}
	if m.RootUntil("a").Sub(now) != cfg.MaxBackoff {
		t.Fatal("exponential cap")
	}
	if retryAfter("172800", now) != 48*time.Hour {
		t.Fatal("long Retry-After truncated")
	}
}
func TestIssue138BudgetHTTP429DoesNotRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	m, _ := New(fastPolicy())
	a, _ := m.Client("a", srv.URL, "")
	b, _ := m.Client("b", srv.URL, "")
	start := time.Now()
	res, e := a.Get(srv.URL)
	if e != nil || res.StatusCode != 429 {
		t.Fatal(e)
	}
	res.Body.Close()
	if m.RootUntil("a").Before(start.Add(120 * time.Second)) {
		t.Fatal("Retry-After lost")
	}
	_, e = b.Get(srv.URL)
	if !errors.Is(e, ErrDeferred) || calls != 1 {
		t.Fatal("request/retry escaped cooldown", calls, e)
	}
}
func TestIssue138BudgetBoundsOriginsAndCanceledPacing(t *testing.T) {
	m, _ := New(fastPolicy())
	if a, _ := Group("https://EXAMPLE.com:443/a", ""); a != "https://example.com" {
		t.Fatal(a)
	}
	for _, s := range []string{"file:///a", "https://user:secret@host/a", "https://"} {
		if _, e := m.Client("a", s, ""); e == nil {
			t.Fatal("bad endpoint accepted")
		}
	}
	for i := 0; i < 4; i++ {
		if _, e := m.Client(fmt.Sprint(i), "http://example.com", ""); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := m.Client("extra", "http://example.com", ""); !errors.Is(e, ErrCapacity) {
		t.Fatal(e)
	}
	if len(m.roots) != 4 {
		t.Fatal("unbounded root state")
	}
	live, _ := New(fastPolicy())
	live.cfg.Root = time.Second
	live.acquire(context.Background(), "a", "g")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if e := live.acquire(ctx, "a", "g"); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	tr := transport{manager: live, root: "a", group: "g", origin: "http://allowed", base: http.DefaultTransport}
	req, _ := http.NewRequest("GET", "http://elsewhere/a", strings.NewReader(""))
	if _, e := tr.RoundTrip(req); !errors.Is(e, ErrEndpoint) {
		t.Fatal("cross-origin request allowed", e)
	}
}
