package scan_test

import (
	"context"
	"sort"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
)

// P1 hot-scope polling feasibility harness (Issue #66).
//
// TEST-ONLY: this file defines an in-memory cadence/budget model that selects
// due HOT scopes and drives the already-accepted scan.Service.ScanScope path. It
// is not a production scheduler and adds no production runtime code.

type pollCadence int

const (
	cadenceCold pollCadence = iota
	cadenceWarm
	cadenceHot
)

func (c pollCadence) String() string {
	switch c {
	case cadenceHot:
		return "HOT"
	case cadenceWarm:
		return "WARM"
	default:
		return "COLD"
	}
}

// hotScope is one operator-provided directory known to belong to the root.
type hotScope struct {
	path           string
	cadence        pollCadence
	lastPolled     time.Time
	noChangeStreak int
}

// pollLimits are the explicit prototype request/cycle budgets (Issue #66 §6).
type pollLimits struct {
	maxHotScopes         int
	maxScopesPerCycle    int
	maxCycleWallTime     time.Duration
	maxEntriesPerScope   int
	minimumScopeInterval time.Duration
}

func defaultPollLimits() pollLimits {
	return pollLimits{
		maxHotScopes:         5,
		maxScopesPerCycle:    5,
		maxCycleWallTime:     60 * time.Second,
		maxEntriesPerScope:   1000,
		minimumScopeInterval: 120 * time.Second,
	}
}

type scopePollResult struct {
	path    string
	cadence pollCadence
	outcome string
	mutated bool
	elapsed time.Duration
	err     error
}

type pollCycleResult struct {
	due             int
	attempted       int
	wallTime        time.Duration
	budgetExhausted bool
	results         []scopePollResult
}

func (r pollCycleResult) polledPaths() []string {
	out := make([]string, 0, len(r.results))
	for _, res := range r.results {
		out = append(out, res.path)
	}
	return out
}

// mutatedByPath reports, per polled scope, whether the observation canonically
// mutated state (used for P1 change attribution).
func (r pollCycleResult) mutatedByPath() map[string]bool {
	out := map[string]bool{}
	for _, res := range r.results {
		out[res.path] = res.mutated
	}
	return out
}

// pollHarness is the in-memory prototype poller.
type pollHarness struct {
	limits pollLimits
	scopes []*hotScope
	now    func() time.Time
	pollFn func(ctx context.Context, scope string) (scan.Result, error)
}

func newPollHarness(limits pollLimits, now func() time.Time, pollFn func(context.Context, string) (scan.Result, error)) *pollHarness {
	if now == nil {
		now = time.Now
	}
	return &pollHarness{limits: limits, now: now, pollFn: pollFn}
}

// addScope registers a scope. maxHotScopes caps only the HOT set: WARM/COLD
// scopes do not consume HOT budget, so a future HOT->WARM demotion frees it.
func (h *pollHarness) addScope(path string, c pollCadence) bool {
	if c == cadenceHot && h.hotCount() >= h.limits.maxHotScopes {
		return false
	}
	h.scopes = append(h.scopes, &hotScope{path: path, cadence: c})
	return true
}

func (h *pollHarness) hotCount() int {
	n := 0
	for _, s := range h.scopes {
		if s.cadence == cadenceHot {
			n++
		}
	}
	return n
}

// dueScopes returns HOT scopes whose minimum interval has elapsed. Parent/child
// scopes are intentionally NOT collapsed (Issue #66 §4): polling /a does not
// cover /a/b.
func (h *pollHarness) dueScopes(now time.Time) []*hotScope {
	var due []*hotScope
	for _, s := range h.scopes {
		if s.cadence != cadenceHot {
			continue
		}
		if !s.lastPolled.IsZero() && now.Sub(s.lastPolled) < h.limits.minimumScopeInterval {
			continue
		}
		due = append(due, s)
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].path < due[j].path })
	return due
}

// runCycle polls at most max_scopes_per_cycle due HOT scopes, stopping when the
// cycle wall-time budget is exhausted. A slow or failing scope is recorded and is
// never retried inside the same cycle (no tight loop).
func (h *pollHarness) runCycle(ctx context.Context) pollCycleResult {
	start := h.now()
	deadline := start.Add(h.limits.maxCycleWallTime)
	res := pollCycleResult{}

	due := h.dueScopes(start)
	res.due = len(due)

	limit := h.limits.maxScopesPerCycle
	if limit <= 0 || limit > len(due) {
		limit = len(due)
	}
	for i := 0; i < limit; i++ {
		if !h.now().Before(deadline) {
			res.budgetExhausted = true
			break
		}
		s := due[i]
		res.attempted++
		before := h.now()
		out, err := h.pollFn(ctx, s.path)
		elapsed := h.now().Sub(before)
		s.lastPolled = h.now()
		res.results = append(res.results, scopePollResult{
			path: s.path, cadence: s.cadence,
			outcome: string(out.Outcome.Status), mutated: out.Outcome.Mutated,
			elapsed: elapsed, err: err,
		})
		switch {
		case err != nil:
			s.noChangeStreak = 0
		case out.Outcome.Status == domain.AdmissionNoop:
			s.noChangeStreak++
		default:
			s.noChangeStreak = 0
		}
	}
	res.wallTime = h.now().Sub(start)
	return res
}
