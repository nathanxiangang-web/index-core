package scan

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

var ErrReadOrder = errors.New("same-root frozen slot is not the next ordered input")

// SameRootPrototype is explicitly unwired. Its bounded barrier experiment is
// NOT the application's Worker Pool, a scheduler, or a provider request limiter.
type SameRootPrototype struct {
	s       *Service
	workers int
	mu      sync.Mutex
	busy    bool
}

func NewSameRootPrototype(s *Service, workers int) (*SameRootPrototype, error) {
	if s == nil || s.store == nil || s.timeout <= 0 || (workers != 1 && workers != 2 && workers != 4 && workers != 8) {
		return nil, errors.New("same-root prototype requires finite scanner and1/2/4/8 readers")
	}
	return &SameRootPrototype{s: s, workers: workers}, nil
}

type SameRootBatch struct{ value *sameRootBatch }
type sameRootBatch struct {
	owner  *SameRootPrototype
	root   string
	ids    []string
	next   int
	mu     sync.Mutex
	closed bool
}

func (p *SameRootPrototype) reserve() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		return false
	}
	p.busy = true
	return true
}
func (p *SameRootPrototype) release() { p.mu.Lock(); p.busy = false; p.mu.Unlock() }
func (b *SameRootBatch) Checkpoint() string {
	if b == nil || b.value == nil || len(b.value.ids) == 0 {
		return ""
	}
	return b.value.ids[0]
}
func (b *SameRootBatch) Len() int {
	if b == nil || b.value == nil {
		return 0
	}
	return len(b.value.ids)
}
func (b *SameRootBatch) Close() {
	if b == nil || b.value == nil {
		return
	}
	v := b.value
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.closed {
		v.closed = true
		v.owner.release()
	}
}

// FetchBatch fixes order from durable claims before I/O; arrival order cannot
// choose admission order. All real source observations are frozen atomically
// before any input becomes executable. Failed/canceled reads create NO drafts.
func (p *SameRootPrototype) FetchBatch(ctx context.Context, claims []state.DirtyScopeWork) (*SameRootBatch, error) {
	if len(claims) < 1 || len(claims) > 8 {
		return nil, ErrReadOrder
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !p.reserve() {
		return nil, ErrFetchBusy
	}
	success := false
	defer func() {
		if !success {
			p.release()
		}
	}()
	claims = append([]state.DirtyScopeWork(nil), claims...)
	seenScopes := map[string]bool{}
	for _, c := range claims {
		if seenScopes[c.ScopeKey] {
			return nil, ErrReadOrder
		}
		seenScopes[c.ScopeKey] = true
		if c.ScopeKey == "/" || state.ValidateScopeKey(c.ScopeKey) != nil || len(strings.Split(strings.Trim(c.ScopeKey, "/"), "/")) > 8 || c.LastAttemptStartedAt == nil || c.ClaimedSignalSeq == nil || c.RootID != claims[0].RootID || path.Dir(c.ScopeKey) != path.Dir(claims[0].ScopeKey) {
			return nil, ErrReadOrder
		}
	}
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].LastAttemptStartedAt.Equal(*claims[j].LastAttemptStartedAt) {
			return claims[i].ScopeKey < claims[j].ScopeKey
		}
		return claims[i].LastAttemptStartedAt.Before(*claims[j].LastAttemptStartedAt)
	})
	for i, c := range claims {
		if i > 0 && c.ScopeKey == claims[i-1].ScopeKey {
			return nil, ErrReadOrder
		}
		current, err := p.s.store.GetWork(ctx, c.RootID, c.ScopeKey)
		if err != nil {
			return nil, err
		}
		if !claimMatches(c, current) {
			return nil, postgres.ErrScopedFetchStale
		}
	}
	ctx, cancel := context.WithTimeout(ctx, p.s.timeout)
	defer cancel()
	obs := make([]*scopeObservation, len(claims))
	errs := make([]error, len(claims))
	jobs := make(chan int, len(claims))
	for i := range claims {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for w := 0; w < p.workers && w < len(claims); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					errs[i] = ctx.Err()
					continue
				}
				obs[i], errs[i] = p.s.readScope(ctx, claims[i].RootID, claims[i].ScopeKey, 1000)
				if errs[i] != nil {
					cancel()
				}
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, len(obs))
	for i := range ids {
		ids[i] = reconcile.NewUUID()
	}
	inputs := make([]postgres.FrozenScopeInput, len(obs))
	for i, o := range obs {
		c := claims[i]
		f := postgres.ScopedFetchFence{Version: 2, Generation: o.root.CurrentGeneration, AdmissionFrontier: o.root.LatestAdmissionSeq, AdapterDigest: postgres.AdapterConfigDigest(o.binding), AdapterRevision: o.binding.Revision, ScopeKey: c.ScopeKey, TargetPath: o.target, ParentID: o.parentID, ClaimedSequence: *c.ClaimedSignalSeq, Attempt: c.AttemptCount, AttemptStartedAt: *c.LastAttemptStartedAt, Lineage: &postgres.ScopedReadLineage{Snapshots: ids, Rank: i}}
		meta := map[string]any{"collector_kind": o.binding.CollectorKind, "path": o.config.Path, "scope": o.target, "mode": "scoped_refresh", "scoped_fetch_fence": f}
		if o.config.MirrorPresence && o.raw.DirectoryTopologyDigest != "" {
			meta["directory_coverage"] = domain.DirectoryCoverage{Version: 3, Path: o.target, Generation: o.root.CurrentGeneration, AdapterDigest: f.AdapterDigest, EntryCount: len(o.raw.Entries), TopologyDigest: o.raw.DirectoryTopologyDigest}
		}
		provenance, _ := json.Marshal(meta)
		fresh, assurance, count := o.raw.Freshness, o.raw.Assurance, int64(len(o.raw.Entries))
		inputs[i] = postgres.FrozenScopeInput{Snapshot: domain.Snapshot{SnapshotID: ids[i], RootID: c.RootID, Provenance: provenance, ObservedAt: o.observedAt, TraversalStatus: o.raw.TraversalStatus, ErrorSummary: o.raw.ErrorSummary, SkippedScopes: o.raw.SkippedScopes, SkippedScopesKnownEmpty: o.raw.SkippedKnownEmpty, FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &assurance, CompletenessFlag: domain.CompletenessFlagPartial, LifecycleState: domain.SnapshotDraft, EntryCount: &count}, Entries: toEntries(o.raw)}
	}
	if err := p.s.store.FreezeScopedReadBatch(ctx, inputs); err != nil {
		return nil, err
	}
	success = true
	return &SameRootBatch{value: &sameRootBatch{owner: p, root: claims[0].RootID, ids: ids}}, nil
}

// ResumeBatch uses persisted facts, NEVER a fresh source call. Stale epochs
// will still fail final DB fences. Terminal history is not re-evaluated.
func (p *SameRootPrototype) ResumeBatch(ctx context.Context, checkpoint string) (*SameRootBatch, error) {
	if !p.reserve() {
		return nil, ErrFetchBusy
	}
	success := false
	defer func() {
		if !success {
			p.release()
		}
	}()
	snap, err := p.s.store.GetSnapshot(ctx, p.s.store.Pool(), checkpoint)
	if err != nil {
		return nil, err
	}
	var meta struct {
		Fence postgres.ScopedFetchFence `json:"scoped_fetch_fence"`
	}
	if json.Unmarshal(snap.Provenance, &meta) != nil || meta.Fence.Version != 2 || meta.Fence.Lineage == nil {
		return nil, ErrReadOrder
	}
	l := meta.Fence.Lineage
	if l.Rank != 0 || len(l.Snapshots) < 1 || len(l.Snapshots) > 8 || l.Snapshots[0] != checkpoint {
		return nil, ErrReadOrder
	}
	v := &sameRootBatch{owner: p, root: snap.RootID, ids: append([]string(nil), l.Snapshots...)}
	for _, id := range v.ids {
		row, err := p.s.store.GetSnapshot(ctx, p.s.store.Pool(), id)
		if err != nil {
			return nil, err
		}
		if row.RootID != v.root {
			return nil, ErrReadOrder
		}
		if row.LifecycleState != domain.SnapshotReconciled {
			break
		}
		var committed struct {
			Fence postgres.ScopedFetchFence `json:"scoped_fetch_fence"`
		}
		if json.Unmarshal(row.Provenance, &committed) != nil {
			return nil, ErrReadOrder
		}
		f := committed.Fence
		w, err := p.s.store.GetWork(ctx, v.root, f.ScopeKey)
		if err != nil {
			return nil, err
		}
		if w.LastVerifiedSignalSeq == nil || *w.LastVerifiedSignalSeq < f.ClaimedSequence {
			// Crash after canonical commit but before CompleteSuccess: replay the
			// exact receipt, letting the caller acknowledge its still-owned claim.
			if w.WorkState != state.WorkInFlight || w.ClaimedSignalSeq == nil || *w.ClaimedSignalSeq != f.ClaimedSequence || w.AttemptCount != f.Attempt || w.LastAttemptStartedAt == nil || !w.LastAttemptStartedAt.Equal(f.AttemptStartedAt) {
				return nil, postgres.ErrScopedFetchStale
			}
			break
		}
		v.next++
	}
	success = true
	return &SameRootBatch{value: v}, nil
}

// Commit is one serial ordered slot, not arrival-driven completion. Work
// completion remains caller-owned. All DB truth uses the unchanged Coordinator.
func (p *SameRootPrototype) Commit(ctx context.Context, b *SameRootBatch, rank int) (Result, error) {
	if b == nil || b.value == nil || b.value.owner != p {
		return Result{}, ErrFetchConsumed
	}
	v := b.value
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed || rank < v.next {
		return Result{}, ErrFetchConsumed
	}
	if rank != v.next || rank >= len(v.ids) {
		return Result{}, ErrReadOrder
	}
	ctx, cancel := context.WithTimeout(ctx, p.s.timeout)
	defer cancel()
	fail := func(err error) (Result, error) { v.closed = true; p.release(); return Result{}, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	id := v.ids[rank]
	snap, err := p.s.store.GetSnapshot(ctx, p.s.store.Pool(), id)
	if err != nil {
		return fail(err)
	}
	policy, err := p.s.store.RootReconcileConfig(ctx, v.root)
	if err != nil {
		return fail(err)
	}
	coord := postgres.NewCoordinator(p.s.store, policy)
	if snap.LifecycleState == domain.SnapshotDraft {
		if _, err = p.s.store.SubmitAndAdmitSnapshot(ctx, v.root, id); err != nil {
			return fail(err)
		}
	}
	out, err := coord.ProcessSnapshot(ctx, v.root, id)
	if err != nil {
		return fail(err)
	}
	if out.Status != domain.AdmissionApplied && out.Status != domain.AdmissionNoop {
		return fail(postgres.ErrScopedFetchStale)
	}
	var meta struct {
		Fence    postgres.ScopedFetchFence `json:"scoped_fetch_fence"`
		Coverage *domain.DirectoryCoverage `json:"directory_coverage"`
	}
	if err := json.Unmarshal(snap.Provenance, &meta); err != nil {
		return fail(err)
	}
	entries, err := p.s.store.ListSnapshotEntries(ctx, p.s.store.Pool(), id)
	if err != nil {
		return fail(err)
	}
	result := Result{SnapshotID: id, Outcome: out, MirrorPresence: meta.Coverage != nil}
	for _, e := range entries {
		if e.IsDir {
			result.ChildScopes = append(result.ChildScopes, strings.TrimSuffix(meta.Fence.ScopeKey, "/")+"/"+e.Name)
		}
	}
	v.next++
	if v.next == len(v.ids) {
		v.closed = true
		p.release()
	}
	return result, nil
}
