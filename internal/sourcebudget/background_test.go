package sourcebudget

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIssue134RollingActualHTTPShareAndForeground(t *testing.T) {
	for _, policy := range []Config{{15 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, time.Second, time.Minute, 4}, {5 * time.Millisecond, 15 * time.Millisecond, 10 * time.Millisecond, time.Second, time.Minute, 4}, {5 * time.Millisecond, 10 * time.Millisecond, 15 * time.Millisecond, time.Second, time.Minute, 4}} {
		var events []HTTPEvent
		m, err := New(policy, WithBackgroundShare(25), WithObserver(func(e HTTPEvent) { events = append(events, e) }))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(1, 0)
		m.now = func() time.Time { return now }
		m.wait = func(ctx context.Context, d time.Duration) error { now = now.Add(d); return ctx.Err() }
		tr := transport{manager: m, root: "root", group: "shared", origin: "https://example.test", base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}
		for i := 0; i < 4; i++ {
			for _, bg := range []bool{true, false, false} {
				ctx := context.Background()
				if bg {
					ctx = Background(ctx)
				}
				r, _ := http.NewRequestWithContext(ctx, "GET", "https://example.test/api/me", nil)
				if _, err = tr.RoundTrip(r); err != nil {
					t.Fatal(err)
				}
			}
		}
		var lastBG time.Time
		fastForeground := false
		for i, e := range events {
			if e.Status != 503 || e.Failed {
				t.Fatal(e)
			}
			if e.Background {
				if !lastBG.IsZero() && e.StartedAt.Sub(lastBG) < 60*time.Millisecond {
					t.Fatal("background quota bypass", events)
				}
				lastBG = e.StartedAt
			} else if i > 0 && e.StartedAt.Sub(events[i-1].StartedAt) < 60*time.Millisecond {
				fastForeground = true
			}
		}
		if len(events) != 12 || !fastForeground {
			t.Fatal("foreground incorrectly shares background delay", events)
		}
	}
}

func TestIssue134RollingObserverCancellationAndReentrancy(t *testing.T) {
	var events []HTTPEvent
	var m *Manager
	m, _ = New(fastPolicy(), WithBackgroundShare(50), WithObserver(func(e HTTPEvent) { m.RootUntil("root"); events = append(events, e) }))
	tr := transport{manager: m, root: "root", group: "shared", origin: "https://example.test", base: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
	r, _ := http.NewRequestWithContext(Background(context.Background()), "GET", "https://example.test/api/me", nil)
	if _, err := tr.RoundTrip(r); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = r.WithContext(ctx)
	if _, err := tr.RoundTrip(r); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(events) != 1 || !events[0].Timeout || !events[0].Failed {
		t.Fatal("planned wait counted as HTTP", events)
	}
}
