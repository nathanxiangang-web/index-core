package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
)

// ScopedReadLineage is experimental immutable DRAFT provenance, not a new queue.
// The complete ordered roster is frozen atomically before the first admission.
// Captured Generation/observed_at are never rewritten after sibling commits.
type ScopedReadLineage struct {
	Snapshots        []string `json:"snapshots"`
	Rank             int      `json:"rank"`
	DependencyDigest string   `json:"dependency_digest"`
	PolicyRevision   string   `json:"policy_revision"`
}

type FrozenScopeInput struct {
	Snapshot domain.Snapshot
	Entries  []domain.SnapshotEntry
}

// All Stage1 entry points enforce the optional contract, including recovery of
// SUBMITTED input. Existing pending admissions are checked by Stage2 instead.
func (s *Store) validateOptionalScopedAdmission(ctx context.Context, tx pgx.Tx, id string) error {
	var present bool
	if err := tx.QueryRow(ctx, `SELECT provenance ? 'scoped_fetch_fence' FROM index_snapshot WHERE snapshot_id=$1::uuid`, id).Scan(&present); err != nil {
		return err
	}
	if !present {
		return nil
	}
	snap, err := s.GetSnapshot(ctx, tx, id)
	if err != nil {
		return err
	}
	return s.validateScopedFetch(ctx, tx, snap, nil)
}

func readFence(snap domain.Snapshot) (map[string]json.RawMessage, ScopedFetchFence, error) {
	var meta map[string]json.RawMessage
	var f ScopedFetchFence
	if json.Unmarshal(snap.Provenance, &meta) != nil || json.Unmarshal(meta["scoped_fetch_fence"], &f) != nil {
		return nil, f, ErrScopedFetchStale
	}
	return meta, f, nil
}

func claimOrderedBefore(a, b ScopedFetchFence) bool {
	return a.AttemptStartedAt.Before(b.AttemptStartedAt) || (a.AttemptStartedAt.Equal(b.AttemptStartedAt) && a.ScopeKey < b.ScopeKey)
}

// FreezeScopedReadBatch only creates inert DRAFTs. A consistent DB snapshot
// captures dependencies against the common pre-read frontier; there is no
// provider call or Root write lock here. Actual admission revalidates everything.
func (s *Store) FreezeScopedReadBatch(ctx context.Context, inputs []FrozenScopeInput) error {
	if len(inputs) < 1 || len(inputs) > 8 {
		return ErrScopedFetchStale
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	root, err := s.GetRoot(ctx, tx, inputs[0].Snapshot.RootID)
	if err != nil {
		return err
	}
	var revision string
	if err := tx.QueryRow(ctx, `SELECT xmin::text FROM index_root_config WHERE root_id=$1::uuid`, root.RootID).Scan(&revision); err != nil {
		return err
	}
	roster := make([]string, len(inputs))
	seen := map[string]bool{}
	for i, in := range inputs {
		roster[i] = in.Snapshot.SnapshotID
		if seen[roster[i]] {
			return ErrScopedFetchStale
		}
		seen[roster[i]] = true
	}
	var first ScopedFetchFence
	for i, in := range inputs {
		meta, f, err := readFence(in.Snapshot)
		if err != nil {
			return err
		}
		if i == 0 {
			first = f
		}
		if root.LifecycleState != domain.RootActive || in.Snapshot.RootID != root.RootID || in.Snapshot.LifecycleState != domain.SnapshotDraft || in.Snapshot.EntryCount == nil || *in.Snapshot.EntryCount != int64(len(in.Entries)) || f.Version != 2 || f.Lineage == nil || f.ScopeKey == "/" || state.ValidateScopeKey(f.ScopeKey) != nil || f.Generation != root.CurrentGeneration || f.AdmissionFrontier != root.LatestAdmissionSeq || f.Generation != first.Generation || f.AdmissionFrontier != first.AdmissionFrontier || f.AdapterDigest != first.AdapterDigest || f.AdapterRevision != first.AdapterRevision || path.Dir(f.TargetPath) != path.Dir(first.TargetPath) || path.Clean(f.TargetPath) != f.TargetPath || f.Lineage.Rank != i || !reflect.DeepEqual(f.Lineage.Snapshots, roster) || len(in.Entries) > 1000 {
			return ErrScopedFetchStale
		}
		for j := 0; j < i; j++ {
			_, p, e := readFence(inputs[j].Snapshot)
			if e != nil || p.ScopeKey == f.ScopeKey || p.ParentID == f.ParentID {
				return ErrScopedFetchStale
			}
			if j == i-1 && !claimOrderedBefore(p, f) {
				return ErrScopedFetchStale
			}
		}
		// A claim must still be real at freeze, not merely supplied by the caller.
		w, e := scanWork(tx.QueryRow(ctx, `SELECT `+workColumns+` FROM index_dirty_scope_work WHERE root_id=$1 AND scope_key=$2`, root.RootID, f.ScopeKey).Scan)
		if e != nil {
			return e
		}
		if w.WorkState != state.WorkInFlight || w.ClaimedSignalSeq == nil || *w.ClaimedSignalSeq != f.ClaimedSequence || w.AttemptCount != f.Attempt || w.LastAttemptStartedAt == nil || !w.LastAttemptStartedAt.Equal(f.AttemptStartedAt) {
			return ErrScopedFetchStale
		}
		digest, e := s.scopedDependencyDigest(ctx, tx, root.RootID, f.TargetPath, in.Entries)
		if e != nil {
			return e
		}
		f.Lineage.DependencyDigest = digest
		f.Lineage.PolicyRevision = revision
		raw, _ := json.Marshal(f)
		meta["scoped_fetch_fence"] = raw
		in.Snapshot.Provenance, err = json.Marshal(meta)
		if err != nil {
			return err
		}
		if err := s.InsertSnapshotStub(ctx, tx, in.Snapshot); err != nil {
			return err
		}
		for _, entry := range in.Entries {
			entry.SnapshotID = in.Snapshot.SnapshotID
			if err := s.InsertSnapshotEntry(ctx, tx, entry); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// Conservative superset of Kernel identity/absence dependencies: local subtree
// and ancestors, matching hash/size/provider IDs anywhere, all missing evidence.
// Read authoritative latest observations via LoadPriorResources, not its cache.
// This intentionally rejects some independent work; it never weakens matching.
func (s *Store) scopedDependencyDigest(ctx context.Context, q Querier, root, target string, entries []domain.SnapshotEntry) (string, error) {
	where, args := dependencyQuery(root, target, entries)
	prior, err := s.loadPriorWhere(ctx, q, where, args...)
	if err != nil {
		return "", err
	}
	return dependencyDigest(prior, target, entries)
}

// Exactly the reference Go predicate below, pushed down before row transfer.
// Separate no-ID SQL prevents a generic plan from retaining an unnecessary
// global latest-provider-ID branch for the usual OpenList input.
func dependencyQuery(root, target string, entries []domain.SnapshotEntry) (string, []any) {
	hashes, ids, sizes := []string{}, []string{}, []int64{}
	for _, e := range entries {
		if e.ContentHash != nil {
			hashes = append(hashes, *e.ContentHash)
		}
		if e.ProviderObjectID != nil {
			ids = append(ids, *e.ProviderObjectID)
		}
		if e.Size != nil {
			sizes = append(sizes, *e.Size)
		}
	}
	canonical := `(c.canonical_path=$2::text OR pg_catalog.starts_with(c.canonical_path,$2::text||'/') OR pg_catalog.starts_with($2::text,c.canonical_path||'/') OR c.content_hash=ANY($3::text[]) OR c.size=ANY($4::bigint[]) OR c.removal_evidence_state<>'NONE')`
	where := "c.root_id=$1::uuid AND " + canonical
	args := []any{root, target, hashes, sizes}
	if len(ids) > 0 {
		where = "c.root_id=$1::uuid AND (" + canonical + " OR o.provider_object_id=ANY($5::text[]))"
		args = append(args, ids)
	}
	return where, args
}

func dependencyDigest(prior []reconcile.PriorResource, target string, entries []domain.SnapshotEntry) (string, error) {
	hashes, ids, sizes := map[string]bool{}, map[string]bool{}, map[int64]bool{}
	for _, e := range entries {
		if e.ContentHash != nil {
			hashes[*e.ContentHash] = true
		}
		if e.ProviderObjectID != nil {
			ids[*e.ProviderObjectID] = true
		}
		if e.Size != nil {
			sizes[*e.Size] = true
		}
	}
	deps := []reconcile.PriorResource{}
	for _, p := range prior {
		local := p.CanonicalPath != nil && (*p.CanonicalPath == target || strings.HasPrefix(*p.CanonicalPath, target+"/") || strings.HasPrefix(target, *p.CanonicalPath+"/"))
		candidate := p.ContentHash != nil && hashes[*p.ContentHash] || p.Size != nil && sizes[*p.Size] || p.ProviderObjectID != nil && ids[*p.ProviderObjectID]
		if local || candidate || p.RemovalEvidenceState != domain.RemovalEvidenceNone {
			deps = append(deps, p)
		}
	}
	raw, err := json.Marshal(deps)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) validateReadLineage(ctx context.Context, tx pgx.Tx, snap domain.Snapshot, f ScopedFetchFence, seq *int64) (int64, int64, error) {
	fail := func() (int64, int64, error) { return 0, 0, ErrScopedFetchStale }
	l := f.Lineage
	if l == nil || len(l.Snapshots) < 1 || len(l.Snapshots) > 8 || l.Rank < 0 || l.Rank >= len(l.Snapshots) || l.Snapshots[l.Rank] != snap.SnapshotID || f.ScopeKey == "/" || l.PolicyRevision == "" {
		return fail()
	}
	d, e := hex.DecodeString(l.DependencyDigest)
	if e != nil || len(d) != 32 {
		return fail()
	}
	seenIDs, seenScopes, seenParents := map[string]bool{}, map[string]bool{}, map[string]bool{}
	gen, frontier := f.Generation, f.AdmissionFrontier
	var previous ScopedFetchFence
	for i, id := range l.Snapshots {
		if seenIDs[id] {
			return fail()
		}
		seenIDs[id] = true
		sibling, e := s.GetSnapshot(ctx, tx, id)
		if e != nil {
			return 0, 0, e
		}
		_, p, e := readFence(sibling)
		if e != nil {
			return fail()
		}
		if p.AttemptStartedAt.IsZero() || p.Attempt < 1 || p.ClaimedSequence < 1 || (i > 0 && !claimOrderedBefore(previous, p)) {
			return fail()
		}
		previous = p
		if sibling.RootID != snap.RootID || p.Version != 2 || p.Lineage == nil || p.Lineage.Rank != i || !reflect.DeepEqual(p.Lineage.Snapshots, l.Snapshots) || p.Lineage.PolicyRevision != l.PolicyRevision || p.Generation != f.Generation || p.AdmissionFrontier != f.AdmissionFrontier || p.AdapterDigest != f.AdapterDigest || p.AdapterRevision != f.AdapterRevision || p.ScopeKey == "/" || p.ScopeKey == "" || path.Dir(p.TargetPath) != path.Dir(f.TargetPath) || seenScopes[p.ScopeKey] || p.ParentID == "" || seenParents[p.ParentID] {
			return fail()
		}
		seenScopes[p.ScopeKey] = true
		seenParents[p.ParentID] = true
		if i < l.Rank {
			_, out, e := s.terminalSnapshotReceipt(ctx, tx, snap.RootID, id, f.AdmissionFrontier+int64(i)+1, gen+1)
			if e != nil {
				if errors.Is(e, pgx.ErrNoRows) || errors.Is(e, ErrTerminalSnapshot) {
					return fail()
				}
				return 0, 0, e
			}
			if out.Generation != gen || out.AppliedGeneration < gen || out.AppliedGeneration > gen+1 {
				return fail()
			}
			gen = out.AppliedGeneration
			frontier++
		}
	}
	if seq != nil {
		frontier++
		if *seq != frontier {
			return fail()
		}
	}
	// No other persisted Snapshot may consume this exact durable claim epoch.
	var duplicate bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM index_admission a JOIN index_snapshot s ON s.snapshot_id=a.snapshot_id WHERE a.root_id=$1::uuid AND s.snapshot_id<>$2::uuid AND s.provenance->'scoped_fetch_fence'->>'scope_key'=$3 AND s.provenance->'scoped_fetch_fence'->>'claimed_sequence'=$4 AND s.provenance->'scoped_fetch_fence'->>'attempt'=$5 AND (s.provenance->'scoped_fetch_fence'->>'attempt_started_at')::timestamptz=$6)`, snap.RootID, snap.SnapshotID, f.ScopeKey, strconv.FormatInt(f.ClaimedSequence, 10), strconv.FormatInt(f.Attempt, 10), f.AttemptStartedAt).Scan(&duplicate); err != nil {
		return 0, 0, err
	}
	if duplicate {
		return fail()
	}
	var policyRevision string
	if err := tx.QueryRow(ctx, `SELECT xmin::text FROM index_root_config WHERE root_id=$1::uuid`, snap.RootID).Scan(&policyRevision); err != nil {
		return 0, 0, err
	}
	if policyRevision != l.PolicyRevision {
		return fail()
	}
	entries, err := s.ListSnapshotEntries(ctx, tx, snap.SnapshotID)
	if err != nil {
		return 0, 0, err
	}
	digest, err := s.scopedDependencyDigest(ctx, tx, snap.RootID, f.TargetPath, entries)
	if err != nil {
		return 0, 0, err
	}
	if digest != l.DependencyDigest {
		return fail()
	}
	return gen, frontier, nil
}
