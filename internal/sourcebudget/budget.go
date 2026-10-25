// Package sourcebudget owns process-local source HTTP policy, never source facts.
package sourcebudget

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrDeferred = errors.New("source request deferred by budget")
var ErrCapacity = errors.New("source budget state capacity exhausted")
var ErrEndpoint = errors.New("invalid source budget endpoint")

type Config struct {
	Global, Root, Provider time.Duration
	Backoff, MaxBackoff    time.Duration
	MaxKeys                int
}

func Defaults() Config {
	return Config{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond, 45 * time.Second, 15 * time.Minute, 4096}
}
func (c Config) Validate() error {
	for _, d := range []time.Duration{c.Global, c.Root, c.Provider} {
		if d < time.Millisecond || d > time.Minute {
			return errors.New("source spacing must be1ms..1m")
		}
	}
	if c.Backoff < time.Millisecond || c.MaxBackoff < c.Backoff || c.MaxBackoff > time.Hour || c.MaxKeys < 1 || c.MaxKeys > 4096 {
		return errors.New("invalid bounded source backoff/state policy")
	}
	return nil
}

type slot struct {
	next, until time.Time
	strikes     int
}
type Manager struct {
	mu            sync.Mutex
	cfg           Config
	global        time.Time
	roots, groups map[string]*slot
	now           func() time.Time
	wait          func(context.Context, time.Duration) error
}

func New(c Config) (*Manager, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	return &Manager{cfg: c, roots: map[string]*slot{}, groups: map[string]*slot{}, now: time.Now, wait: sleep}, nil
}
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func max(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}
func (m *Manager) register(root, group string) error {
	if root == "" || group == "" || len(root) > 128 || len(group) > 512 {
		return ErrEndpoint
	}
	if (m.roots[root] == nil && len(m.roots) >= m.cfg.MaxKeys) || (m.groups[group] == nil && len(m.groups) >= m.cfg.MaxKeys) {
		return ErrCapacity
	}
	if m.roots[root] == nil {
		m.roots[root] = &slot{}
	}
	if m.groups[group] == nil {
		m.groups[group] = &slot{}
	}
	return nil
}
func (m *Manager) acquire(ctx context.Context, root, group string) error {
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		m.mu.Lock()
		if e := m.register(root, group); e != nil {
			m.mu.Unlock()
			return e
		}
		r, p := m.roots[root], m.groups[group]
		now := m.now()
		if max(r.until, p.until).After(now) {
			r.until = max(r.until, p.until)
			m.mu.Unlock()
			return ErrDeferred
		}
		due := max(m.global, max(r.next, p.next))
		if !due.After(now) {
			m.global = now.Add(m.cfg.Global)
			r.next = now.Add(m.cfg.Root)
			p.next = now.Add(m.cfg.Provider)
			m.mu.Unlock()
			return nil
		}
		m.mu.Unlock()
		if e := m.wait(ctx, due.Sub(now)); e != nil {
			return e
		}
	}
}

// Penalize consumes no request and stores no provider payload. Local exponential
// delay is capped; a longer valid Retry-After is NEVER shortened to that cap.
func (m *Manager) Penalize(root, group string, retry time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.register(root, group) != nil {
		return
	}
	p := m.groups[group]
	if p.strikes < 20 {
		p.strikes++
	}
	d := m.cfg.Backoff
	for i := 1; i < p.strikes && d < m.cfg.MaxBackoff; i++ {
		d *= 2
	}
	if d > m.cfg.MaxBackoff {
		d = m.cfg.MaxBackoff
	}
	if retry > d {
		d = retry
	}
	until := m.now().Add(d)
	p.until = max(p.until, until)
	m.roots[root].until = max(m.roots[root].until, p.until)
}
func (m *Manager) RootUntil(root string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.roots[root]; r != nil {
		return r.until
	}
	return time.Time{}
}

// ReadyAt is read-only admission policy, not a request reservation. Shared-group
// cooldowns can exclude a Root BEFORE ClaimWork, without spending failure budget.
func (m *Manager) ReadyAt(root, endpoint, explicit string) (time.Time, error) {
	g, e := Group(endpoint, explicit)
	if e != nil {
		return time.Time{}, e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.register(root, g); e != nil {
		return time.Time{}, e
	}
	until := max(m.roots[root].until, m.groups[g].until)
	m.roots[root].until = until
	return until, nil
}
func (m *Manager) Succeeded(endpoint, explicit string) {
	g, e := Group(endpoint, explicit)
	if e != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.groups[g]; p != nil && !p.until.After(m.now()) {
		p.strikes = 0
	}
}
func Group(endpoint, explicit string) (string, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", ErrEndpoint
	}
	if explicit != "" {
		if len(explicit) > 64 {
			return "", ErrEndpoint
		}
		for _, r := range explicit {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
				return "", ErrEndpoint
			}
		}
		return "group:" + explicit, nil
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}

type transport struct {
	manager             *Manager
	root, group, origin string
	base                http.RoundTripper
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	origin, e := Group(req.URL.String(), "")
	if e != nil || origin != t.origin {
		return nil, ErrEndpoint
	}
	if e = t.manager.acquire(req.Context(), t.root, t.group); e != nil {
		return nil, e
	}
	resp, e := t.base.RoundTrip(req)
	if e == nil && resp != nil && resp.StatusCode == 429 {
		t.manager.Penalize(t.root, t.group, retryAfter(resp.Header.Get("Retry-After"), t.manager.now()))
	}
	return resp, e
}
func retryAfter(raw string, now time.Time) time.Duration {
	if n, e := strconv.ParseInt(raw, 10, 64); e == nil && n > 0 {
		if n > int64((1<<63-1)/time.Second) {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(n) * time.Second
	}
	if at, e := http.ParseTime(raw); e == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

// Clients share the manager and normal connection pool. Policy wraps every
// request, including identity, login, topology and redirects (which are denied).
func (m *Manager) Client(root, endpoint, explicit string) (*http.Client, error) {
	group, e := Group(endpoint, explicit)
	if e != nil {
		return nil, e
	}
	origin, e := Group(endpoint, "")
	if e != nil {
		return nil, e
	}
	m.mu.Lock()
	e = m.register(root, group)
	m.mu.Unlock()
	if e != nil {
		return nil, e
	}
	return &http.Client{Transport: transport{m, root, group, origin, http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
