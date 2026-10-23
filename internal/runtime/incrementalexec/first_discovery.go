package incrementalexec

import (
	"context"
	"errors"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// FirstDiscoveryStore is an additive, read-only planning port. Every returned
// version still goes through the unchanged ClaimWork CAS before any scan.
type FirstDiscoveryStore interface {
	NextFirstDiscoveryWork(context.Context, time.Time, []string) (state.DirtyScopeWork, bool, error)
	NextAgedBackgroundWork(context.Context, time.Time, time.Time) (state.DirtyScopeWork, bool, error)
}

type selectionKey struct{}
type selectionWindow struct {
	owner         *Executor
	selected      int
	fastLimit     int
	fastRoots     []string
	fastExhausted bool
}

const (
	SelectionPriorityFIFO   = "PRIORITY_FIFO"
	SelectionFirstDiscovery = "FIRST_DISCOVERY"
	SelectionAgedBackground = "AGED_BACKGROUND"
	BackgroundAge           = 30 * time.Second
)

// PrepareCycle creates a finite serial planning window, never process-global
// priority state. At least three slots reserve background, fresh and FIFO work.
// Direct one-shot callers retain the accepted legacy priority/FIFO semantics.
func (e *Executor) PrepareCycle(ctx context.Context, cfg CycleConfig) (context.Context, error) {
	if !e.cfg.FirstDiscoveryEnabled {
		return ctx, nil
	}
	if cfg.MaxItems < 3 {
		return ctx, errors.New("first discovery requires cycle max_items >= 3")
	}
	limit := cfg.MaxItems - 2
	if limit > 2 {
		limit = 2
	}
	return context.WithValue(ctx, selectionKey{}, &selectionWindow{owner: e, fastLimit: limit}), nil
}

func (e *Executor) selectWork(ctx context.Context, now time.Time) (state.DirtyScopeWork, bool, string, error) {
	w, _ := ctx.Value(selectionKey{}).(*selectionWindow)
	if e.cfg.FirstDiscoveryEnabled && w != nil && w.owner == e {
		store := e.store.(FirstDiscoveryStore) // constructor validates composition
		if w.selected == 0 {
			item, found, err := store.NextAgedBackgroundWork(ctx, now, now.Add(-BackgroundAge))
			if err != nil || found {
				return item, found, SelectionAgedBackground, err
			}
		}
		if !w.fastExhausted && len(w.fastRoots) < w.fastLimit {
			item, found, err := store.NextFirstDiscoveryWork(ctx, now, append([]string(nil), w.fastRoots...))
			if err != nil || found {
				return item, found, SelectionFirstDiscovery, err
			}
			w.fastExhausted = true
		}
	}
	item, found, err := e.store.NextEligiblePendingWork(ctx, now)
	return item, found, SelectionPriorityFIFO, err
}

func (e *Executor) recordSelection(ctx context.Context, root, class string) {
	w, _ := ctx.Value(selectionKey{}).(*selectionWindow)
	if w == nil || w.owner != e {
		return
	}
	w.selected++
	if class == SelectionFirstDiscovery {
		w.fastRoots = append(w.fastRoots, root)
	}
}
