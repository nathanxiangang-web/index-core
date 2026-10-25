package incrementalexec

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"sort"
	"sync/atomic"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

type PoolFairStore interface {
	NextPoolFairWork(context.Context, time.Time, string, ...string) (state.DirtyScopeWork, bool, error)
}
type poolItem struct {
	claim  state.DirtyScopeWork
	result Result
	done   bool
}

// ReadPoolRunner is one finite cycle, not another daemon or queue. Only reads
// overlap; claims, ordered commits and common completions stay serialized.
type ReadPoolRunner struct {
	one           *Executor
	scanner       *scan.Service
	workers       int
	logger        *slog.Logger
	busy          atomic.Bool
	cursor        string
	fallbackUntil time.Time
	now           func() time.Time
}

func NewReadPoolRunner(one *Executor, s *scan.Service, n int, logger *slog.Logger) (*ReadPoolRunner, error) {
	if one == nil || s == nil || (n != 2 && n != 4 && n != 8) {
		return nil, errors.New("read pool requires scanner,executor and2/4/8 readers")
	}
	if one.cfg.MaxEntriesPerScope != 1000 {
		return nil, errors.New("read pool requires the accepted1000-entry cohort cap")
	}
	if _, ok := one.scanner.(ClaimedScopedScanner); !ok {
		return nil, errors.New("read pool requires claim-fenced serial fallback")
	}
	if _, ok := one.store.(PoolFairStore); !ok {
		return nil, errors.New("read pool requires fair planning store")
	}
	return &ReadPoolRunner{one: one, scanner: s, workers: n, logger: logger, now: time.Now}, nil
}
func (p *ReadPoolRunner) Run(parent context.Context, cfg CycleConfig) (out CycleResult, retErr error) {
	if err := cfg.Validate(); err != nil {
		return out, err
	}
	if cfg.ReadWorkers != p.workers {
		return out, errors.New("pool reader config mismatch")
	}
	if !p.busy.CompareAndSwap(false, true) {
		return out, errors.New("read pool cycle already active")
	}
	defer p.busy.Store(false)
	effective := p.workers
	if p.now().Before(p.fallbackUntil) {
		effective = 1
	}
	out.StartedAt = p.now()
	ctx, cancel := context.WithTimeout(parent, cfg.MaxWallTime)
	defer cancel()
	ctx, retErr = p.one.PrepareCycle(ctx, cfg)
	if retErr != nil {
		return out.stop(StopSystemicError), retErr
	}
	items := []poolItem{}
	defer func() {
		// Readers have joined before returning from FetchBatch. Cancellation leaves
		// claims untouched; existing runtime recovery owns ALL interrupted roots.
		seen := map[string]bool{}
		for _, it := range items {
			if !it.done && !seen[it.claim.RootID] {
				seen[it.claim.RootID] = true
				out.InterruptedRoots = append(out.InterruptedRoots, it.claim.RootID)
			}
		}
		out.InterruptedInFlight = len(out.InterruptedRoots) > 0
		if p.logger != nil {
			p.logger.Info("incremental_read_pool_cycle", "readers", p.workers, "effective_readers", effective, "selected", out.SelectedItems, "succeeded", out.Succeeded, "failed", out.Failed, "interrupted_roots", len(out.InterruptedRoots), "stop_reason", out.StopReason)
		}
	}()
	canceled := func() (CycleResult, error) {
		if parent.Err() != nil {
			return out.stop(StopContextCancelled), parent.Err()
		}
		return out.stop(StopMaxWallTime), nil
	}
	// Finish deferred peers through normal retry transitions, never raw SQL or
	// a second source call. Permanent errors apply ONLY to proven failed slots.
	abort := func(from, to int, root string, cause error) (bool, error) {
		actual := map[string]error{}
		var be *scan.BatchReadError
		if errors.As(cause, &be) {
			for _, f := range be.Failures {
				actual[f.Scope] = f.Cause
			}
		}
		internal := false
		for i := from; i < to; i++ {
			if items[i].done {
				continue
			}
			if ctx.Err() != nil {
				return internal, ctx.Err()
			}
			class := state.ErrorTransientProvider
			err := errors.New("source cohort aborted without verification")
			if e := actual[items[i].claim.ScopeKey]; e != nil && items[i].claim.RootID == root {
				class = MapScopeFailure(e)
				err = e
			}
			res, e := p.one.completeFailure(ctx, items[i].result, items[i].claim, class, err)
			out.Last = res
			if e != nil && !errors.Is(e, ErrScannerFailed) {
				return internal, e
			}
			items[i].done = true
			out.Failed++
			if class == state.ErrorInternal {
				internal = true
			}
		}
		return internal, nil
	}
	for i := 0; i < cfg.MaxItems; i++ {
		if ctx.Err() != nil {
			return canceled()
		}
		out.Invocations++
		now := p.one.now()
		var w state.DirtyScopeWork
		var found bool
		var class string
		var err error
		if i == 1 {
			after := p.cursor
			if after == "" && len(items) > 0 {
				after = items[0].claim.RootID
			}
			w, found, err = p.one.store.(PoolFairStore).NextPoolFairWork(ctx, now, after)
			class = "POOL_ROOT_FAIR"
			if found {
				p.cursor = w.RootID
			}
		} else {
			w, found, class, err = p.one.selectWork(ctx, now)
		}
		if err == nil {
			w, found, class, err = p.one.readySelection(ctx, now, w, found, class)
		}
		if err != nil {
			return out.stop(StopSystemicError), err
		}
		if !found {
			break
		}
		res, claimed, err := p.one.claimSelected(ctx, w, class, now)
		out.SelectedItems++
		out.Last = res
		if err != nil {
			if errors.Is(err, ErrStaleSelection) {
				if _, e := abort(0, len(items), "", nil); e != nil {
					return out.stop(StopSystemicError), e
				}
				out.Last = res
				return out.stop(StopStaleSelection), err
			}
			return out.stop(StopSystemicError), err
		}
		items = append(items, poolItem{claim: claimed, result: res})
	}
	for first := 0; first < len(items); {
		if ctx.Err() != nil {
			return canceled()
		}
		end := first + 1
		c := items[first].claim
		if effective > 1 && c.ScopeKey != "/" {
			for end < len(items) && items[end].claim.RootID == c.RootID && path.Dir(items[end].claim.ScopeKey) == path.Dir(c.ScopeKey) && items[end].claim.ScopeKey != "/" {
				end++
			}
		}
		var batch *scan.SameRootBatch
		var sibling *scan.SameRootPrototype
		var err error
		if end-first > 1 {
			sort.SliceStable(items[first:end], func(i, j int) bool {
				a, b := items[first+i].claim, items[first+j].claim
				if a.LastAttemptStartedAt.Equal(*b.LastAttemptStartedAt) {
					return a.ScopeKey < b.ScopeKey
				}
				return a.LastAttemptStartedAt.Before(*b.LastAttemptStartedAt)
			})
			sibling, err = scan.NewSameRootPrototype(p.scanner, effective)
			if err != nil {
				return out.stop(StopSystemicError), err
			}
			claims := []state.DirtyScopeWork{}
			for i := first; i < end; i++ {
				claims = append(claims, items[i].claim)
			}
			batch, err = sibling.FetchBatch(ctx, claims)
		}
		for i := first; i < end && err == nil; i++ {
			var res scan.Result
			if batch != nil {
				res, err = sibling.Commit(ctx, batch, i-first)
			} else {
				res, err = p.one.scanClaimed(ctx, items[i].claim)
			}
			if err != nil {
				break
			}
			if ctx.Err() != nil {
				break
			}
			var completed Result
			completed, err = p.one.completeScan(ctx, items[i].result, items[i].claim, res, nil)
			out.Last = completed
			if err == nil {
				items[i].done = true
				out.Succeeded++
			}
		}
		batch.Close()
		if ctx.Err() != nil {
			return canceled()
		}
		if err != nil {
			// A commit/completion/unknown DB error is never disguised as a source retry.
			var scope *scan.ScopeError
			var be *scan.BatchReadError
			if !errors.As(err, &scope) && !errors.As(err, &be) && !errors.Is(err, postgres.ErrScopedFetchStale) {
				return out.stop(StopSystemicError), err
			}
			if errors.Is(err, ErrCompletionFailed) {
				return out.stop(StopSystemicError), err
			}
			if !errors.As(err, &be) && errors.As(err, &scope) {
				err = &scan.BatchReadError{Failures: []scan.BatchReadFailure{{Scope: items[first].claim.ScopeKey, Cause: err}}, Cause: err}
			}
			p.fallbackUntil = p.now().Add(30 * time.Second)
			internal, e := abort(first, end, c.RootID, err)
			if e != nil {
				return out.stop(StopSystemicError), e
			}
			if internal {
				if _, e := abort(end, len(items), "", nil); e != nil {
					return out.stop(StopSystemicError), e
				}
				return out.stop(StopInternalItemFailure), err
			}
			effective = 1
			first = end
			continue
		}
		first = end
	}
	if out.SelectedItems == cfg.MaxItems {
		return out.stop(StopMaxItems), nil
	}
	return out.stop(StopNoEligibleWork), nil
}
