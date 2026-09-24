package scan_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func noopPoll(_ context.Context, _ string) (scan.Result, error) {
	return scan.Result{Outcome: postgres.ReconcileOutcome{Status: domain.AdmissionNoop}}, nil
}

func TestPollHarnessSelectsOnlyDueScopes(t *testing.T) {
	clk := &testClock{t: time.Unix(0, 0)}
	h := newPollHarness(defaultPollLimits(), clk.now, noopPoll)
	for _, p := range []string{"/a", "/b", "/c"} {
		h.addScope(p, cadenceHot)
	}

	r1 := h.runCycle(context.Background())
	if r1.due != 3 || len(r1.results) != 3 {
		t.Fatalf("first cycle must poll all 3 due scopes, got due=%d results=%v", r1.due, r1.polledPaths())
	}

	// Immediately again: the minimum interval has not elapsed, so nothing is due.
	r2 := h.runCycle(context.Background())
	if r2.due != 0 || len(r2.results) != 0 {
		t.Fatalf("no scope is due before the minimum interval, got due=%d results=%v", r2.due, r2.polledPaths())
	}

	// After the interval elapses, all HOT scopes are due again.
	clk.advance(121 * time.Second)
	r3 := h.runCycle(context.Background())
	if r3.due != 3 {
		t.Fatalf("after the interval all HOT scopes are due, got %d", r3.due)
	}
}

func TestPollHarnessHonorsMaxScopesPerCycle(t *testing.T) {
	clk := &testClock{}
	lim := defaultPollLimits()
	lim.maxScopesPerCycle = 2
	h := newPollHarness(lim, clk.now, noopPoll)
	for _, p := range []string{"/a", "/b", "/c", "/d"} {
		h.addScope(p, cadenceHot)
	}
	r := h.runCycle(context.Background())
	if r.due != 4 {
		t.Fatalf("all 4 scopes are due, got %d", r.due)
	}
	if len(r.results) != 2 {
		t.Fatalf("max_scopes_per_cycle=2 must be honored, got %v", r.polledPaths())
	}
}

func TestPollHarnessStopsOnWallTimeBudget(t *testing.T) {
	clk := &testClock{}
	lim := defaultPollLimits()
	lim.maxCycleWallTime = 60 * time.Second
	lim.maxScopesPerCycle = 5
	h := newPollHarness(lim, clk.now, func(_ context.Context, _ string) (scan.Result, error) {
		clk.advance(30 * time.Second) // each poll consumes 30s
		return noopPoll(context.Background(), "")
	})
	for _, p := range []string{"/a", "/b", "/c", "/d", "/e"} {
		h.addScope(p, cadenceHot)
	}
	r := h.runCycle(context.Background())
	if len(r.results) != 2 {
		t.Fatalf("wall-time budget (60s @ 30s/poll) must stop after 2 polls, got %v", r.polledPaths())
	}
	if !r.budgetExhausted {
		t.Fatal("budget exhaustion must be recorded")
	}
}

func TestPollHarnessErrorScopeDoesNotLoopUnbounded(t *testing.T) {
	clk := &testClock{}
	h := newPollHarness(defaultPollLimits(), clk.now, func(_ context.Context, _ string) (scan.Result, error) {
		return scan.Result{}, errors.New("provider boom")
	})
	h.addScope("/a", cadenceHot)
	h.addScope("/b", cadenceHot)
	r := h.runCycle(context.Background())
	if r.attempted != 2 || len(r.results) != 2 {
		t.Fatalf("a failing scope must be attempted at most once per cycle, attempted=%d results=%d", r.attempted, len(r.results))
	}
	for _, res := range r.results {
		if res.err == nil {
			t.Fatalf("scope %s must record its error", res.path)
		}
	}
}

func TestPollHarnessDoesNotCollapseParentChild(t *testing.T) {
	clk := &testClock{}
	h := newPollHarness(defaultPollLimits(), clk.now, noopPoll)
	for _, p := range []string{"/", "/a", "/a/b"} {
		h.addScope(p, cadenceHot)
	}
	r := h.runCycle(context.Background())
	if got := r.polledPaths(); len(got) != 3 {
		t.Fatalf("parent/child scopes must not be collapsed, got %v", got)
	}
}

func TestPollHarnessHonorsMaxHotScopes(t *testing.T) {
	lim := defaultPollLimits()
	lim.maxHotScopes = 2
	h := newPollHarness(lim, nil, noopPoll)
	if !h.addScope("/a", cadenceHot) || !h.addScope("/b", cadenceHot) {
		t.Fatal("first two hot scopes must be accepted")
	}
	if h.addScope("/c", cadenceHot) {
		t.Fatal("third hot scope must be rejected by max_hot_scopes")
	}
	if len(h.scopes) != 2 {
		t.Fatalf("hot set must be capped at 2, got %d", len(h.scopes))
	}
}

func TestPollHarnessPollsOnlyHotScopes(t *testing.T) {
	clk := &testClock{}
	h := newPollHarness(defaultPollLimits(), clk.now, noopPoll)
	h.addScope("/hot", cadenceHot)
	h.addScope("/warm", cadenceWarm)
	h.addScope("/cold", cadenceCold)
	r := h.runCycle(context.Background())
	if got := r.polledPaths(); len(got) != 1 || got[0] != "/hot" {
		t.Fatalf("only HOT scopes are polled by P1, got %v", got)
	}
}
