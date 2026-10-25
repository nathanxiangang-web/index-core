package scan

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

var ErrFetchBusy = errors.New("fetch prototype capacity or root already reserved")
var ErrFetchConsumed = errors.New("fetch observation is invalid, foreign or consumed")

// FetchPrototype owns the accepted bounded immutable-observation seam, NOT a scheduler.
// One observation per root prevents same-root speculative generation rebasing.
// Claims/completions remain the caller's existing durable P3 responsibility.
type FetchPrototype struct {
	s          *Service
	limit      int
	mu         sync.Mutex
	roots      map[string]bool
	commitLane chan struct{}
}

func NewFetchPrototype(s *Service, limit int) (*FetchPrototype, error) {
	if s == nil || s.store == nil || s.timeout <= 0 || limit < 1 || limit > 8 {
		return nil, errors.New("fetch prototype requires store, finite timeout and capacity1..8")
	}
	p := &FetchPrototype{s: s, limit: limit, roots: map[string]bool{}, commitLane: make(chan struct{}, 1)}
	p.commitLane <- struct{}{}
	return p, nil
}

// FetchStats returns only scalar immutable instrumentation, never entries,
// config, credentials, adapter URLs, provider bodies or mutable evidence.
type FetchStats struct {
	Entries      int
	ObservedAt   time.Time
	ReadDuration time.Duration
}

type FetchedScope struct {
	value *fetchedScope
}

type fetchedScope struct {
	owner    *FetchPrototype
	obs      *scopeObservation
	fence    postgres.ScopedFetchFence
	stats    FetchStats
	consumed atomic.Bool
}

func (f *FetchedScope) Stats() FetchStats {
	if f == nil || f.value == nil {
		return FetchStats{}
	}
	return f.value.stats
}

func (p *FetchPrototype) release(root string) {
	p.mu.Lock()
	delete(p.roots, root)
	p.mu.Unlock()
}

func claimMatches(a, b state.DirtyScopeWork) bool {
	return a.WorkState == state.WorkInFlight && b.WorkState == state.WorkInFlight &&
		a.RootID == b.RootID && a.ScopeKey == b.ScopeKey && a.ClaimedSignalSeq != nil && b.ClaimedSignalSeq != nil &&
		*a.ClaimedSignalSeq == *b.ClaimedSignalSeq && a.AttemptCount == b.AttemptCount &&
		a.LastAttemptStartedAt != nil && b.LastAttemptStartedAt != nil && a.LastAttemptStartedAt.Equal(*b.LastAttemptStartedAt)
}

// FetchScope only reads the store and provider. It cannot create a Snapshot,
// admission, Canonical resource, Journal event or modify Work/Watch. A failed
// fetch releases memory; the caller must classify/complete the durable claim.
func (p *FetchPrototype) FetchScope(ctx context.Context, claimed state.DirtyScopeWork, maxEntries int) (*FetchedScope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxEntries < 0 || maxEntries > 1000 {
		return nil, scopeErrorf(ScopeFailureConfigInvalid, "prototype entry bound")
	}
	if state.ValidateScopeKey(claimed.ScopeKey) != nil || (claimed.ScopeKey != "/" && len(strings.Split(strings.Trim(claimed.ScopeKey, "/"), "/")) > 8) {
		return nil, scopeErrorf(ScopeFailureInvalidScope, "prototype scope/depth bound")
	}
	p.mu.Lock()
	if p.roots[claimed.RootID] || len(p.roots) >= p.limit {
		p.mu.Unlock()
		return nil, ErrFetchBusy
	}
	p.roots[claimed.RootID] = true
	p.mu.Unlock()
	success := false
	defer func() {
		if !success {
			p.release(claimed.RootID)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, p.s.timeout)
	defer cancel()
	current, err := p.s.store.GetWork(ctx, claimed.RootID, claimed.ScopeKey)
	if err != nil {
		return nil, err
	}
	if !claimMatches(claimed, current) {
		return nil, postgres.ErrScopedFetchStale
	}
	obs, err := p.s.readScope(ctx, claimed.RootID, claimed.ScopeKey, maxEntries)
	if err != nil {
		return nil, err
	}
	f := &fetchedScope{owner: p, obs: obs, stats: FetchStats{Entries: len(obs.raw.Entries), ObservedAt: obs.observedAt, ReadDuration: time.Since(obs.started)}}
	f.fence = postgres.ScopedFetchFence{
		Version: 1, Generation: obs.root.CurrentGeneration, AdmissionFrontier: obs.root.LatestAdmissionSeq,
		AdapterDigest: postgres.AdapterConfigDigest(obs.binding), AdapterRevision: obs.binding.Revision,
		ScopeKey: claimed.ScopeKey, TargetPath: obs.target, ParentID: obs.parentID,
		ClaimedSequence: *current.ClaimedSignalSeq, Attempt: current.AttemptCount, AttemptStartedAt: *current.LastAttemptStartedAt,
	}
	success = true
	return &FetchedScope{value: f}, nil
}

// DiscardFetchedScope releases memory only. It cannot refund a source request,
// undo a claim or acknowledge verification. Old callbacks cannot reuse a slot.
func (p *FetchPrototype) DiscardFetchedScope(handle *FetchedScope) bool {
	if handle == nil || handle.value == nil {
		return false
	}
	f := handle.value
	if f.owner != p || !f.consumed.CompareAndSwap(false, true) {
		return false
	}
	rootID := f.obs.root.RootID
	f.obs = nil
	p.release(rootID)
	return true
}

// CommitFetchedScope owns ONE serial commit lane. The immutable source time
// is not replaced with commit time. DB fences are authoritative both at
// admission and at reconcile, so a background worker cannot bypass them.
func (p *FetchPrototype) CommitFetchedScope(ctx context.Context, handle *FetchedScope) (Result, error) {
	if handle == nil || handle.value == nil {
		return Result{}, ErrFetchConsumed
	}
	f := handle.value
	if f.owner != p || !f.consumed.CompareAndSwap(false, true) {
		return Result{}, ErrFetchConsumed
	}
	rootID := f.obs.root.RootID
	defer func() { f.obs = nil; p.release(rootID) }()
	ctx, cancel := context.WithTimeout(ctx, p.s.timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-p.commitLane:
	}
	defer func() { p.commitLane <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return p.s.commitScope(ctx, f.obs, &f.fence)
}
