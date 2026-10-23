package incrementalexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

type planningFake struct {
	WorkStore
	freshCalls, agedCalls, fifoCalls int
	emptyFresh                       bool
	agedError, freshError            error
}

func (f *planningFake) NextAgedBackgroundWork(context.Context, time.Time, time.Time) (state.DirtyScopeWork, bool, error) {
	f.agedCalls++
	if f.agedError != nil {
		return state.DirtyScopeWork{}, false, f.agedError
	}
	return state.DirtyScopeWork{RootID: "old", ScopeKey: "/old"}, true, nil
}
func (f *planningFake) NextFirstDiscoveryWork(_ context.Context, _ time.Time, excluded []string) (state.DirtyScopeWork, bool, error) {
	f.freshCalls++
	if f.freshError != nil {
		return state.DirtyScopeWork{}, false, f.freshError
	}
	if f.emptyFresh {
		return state.DirtyScopeWork{}, false, nil
	}
	for _, root := range []string{"a", "b", "c"} {
		found := false
		for _, x := range excluded {
			found = found || x == root
		}
		if !found {
			return state.DirtyScopeWork{RootID: root, ScopeKey: "/new"}, true, nil
		}
	}
	return state.DirtyScopeWork{}, false, nil
}
func (f *planningFake) NextEligiblePendingWork(context.Context, time.Time) (state.DirtyScopeWork, bool, error) {
	f.fifoCalls++
	return state.DirtyScopeWork{RootID: "high", ScopeKey: "/hot"}, true, nil
}
func TestIssue134PlanningQuotasAndHighFloodFairness(t *testing.T) {
	f := &planningFake{}
	e := &Executor{store: f, cfg: Config{FirstDiscoveryEnabled: true}}
	for cycle := 0; cycle < 100; cycle++ {
		ctx, err := e.PrepareCycle(context.Background(), CycleConfig{MaxItems: 5, MaxWallTime: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		classes := map[string]int{}
		roots := map[string]bool{}
		for i := 0; i < 5; i++ {
			work, found, class, err := e.selectWork(ctx, time.Now())
			if err != nil || !found {
				t.Fatal(err)
			}
			classes[class]++
			if class == SelectionFirstDiscovery {
				if roots[work.RootID] {
					t.Fatal("per-root quota exceeded")
				}
				roots[work.RootID] = true
			}
			e.recordSelection(ctx, work.RootID, class)
		}
		if classes[SelectionAgedBackground] != 1 || classes[SelectionFirstDiscovery] != 2 || classes[SelectionPriorityFIFO] != 2 {
			t.Fatal(classes)
		}
	}
	if f.agedCalls != 100 || f.freshCalls != 200 || f.fifoCalls != 200 {
		t.Fatal(f)
	}
}
func TestIssue134PlanningDisabledDirectAndSmallCycle(t *testing.T) {
	f := &planningFake{}
	e := &Executor{store: f, cfg: Config{FirstDiscoveryEnabled: true}}
	if _, err := e.PrepareCycle(context.Background(), CycleConfig{MaxItems: 2}); err == nil {
		t.Fatal("small window consumes all foreground budget")
	}
	if _, _, class, err := e.selectWork(context.Background(), time.Now()); err != nil || class != SelectionPriorityFIFO || f.freshCalls != 0 || f.agedCalls != 0 {
		t.Fatal("direct one-shot semantics changed")
	}
	e.cfg.FirstDiscoveryEnabled = false
	ctx, err := e.PrepareCycle(context.Background(), CycleConfig{MaxItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, class, err := e.selectWork(ctx, time.Now()); err != nil || class != SelectionPriorityFIFO {
		t.Fatal(class, err)
	}
}
func TestIssue134NoRepeatedEmptyPlanningProbe(t *testing.T) {
	f := &planningFake{emptyFresh: true}
	e := &Executor{store: f, cfg: Config{FirstDiscoveryEnabled: true}}
	ctx, _ := e.PrepareCycle(context.Background(), CycleConfig{MaxItems: 5})
	for i := 0; i < 5; i++ {
		work, _, class, _ := e.selectWork(ctx, time.Now())
		e.recordSelection(ctx, work.RootID, class)
	}
	if f.agedCalls != 1 || f.freshCalls != 1 || f.fifoCalls != 4 {
		t.Fatal(f)
	}
}

func TestIssue134PlanningErrorStopsWithoutFallbackOrClaim(t *testing.T) {
	for _, phase := range []string{"aged", "fresh"} {
		t.Run(phase, func(t *testing.T) {
			failure := errors.New("planning unavailable")
			f := &planningFake{}
			e := &Executor{store: f, cfg: Config{FirstDiscoveryEnabled: true}}
			ctx, _ := e.PrepareCycle(context.Background(), CycleConfig{MaxItems: 5})
			if phase == "aged" {
				f.agedError = failure
			} else {
				e.recordSelection(ctx, "old", SelectionAgedBackground)
				f.freshError = failure
			}
			if _, found, _, err := e.selectWork(ctx, time.Now()); !errors.Is(err, failure) || found || f.fifoCalls != 0 {
				t.Fatal("planning uncertainty fell back", found, err, f.fifoCalls)
			}
		})
	}
}
