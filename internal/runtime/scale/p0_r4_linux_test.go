package scale_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/internal/diagnostic"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
	"github.com/nathanxiangang-web/index-core/testutil/r4guard"
)

const r4Root = "a4000000-0000-0000-0000-000000000125"

type r4Lab struct {
	t         *testing.T
	ctx       context.Context
	st        *postgres.Store
	coord     *postgres.Coordinator
	qr        query.Reader
	source    map[string]domain.SnapshotEntry
	lat       map[string][]float64
	traversed int
	gen       int64
}

func r4Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := r4guard.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
func r4New(t *testing.T) *r4Lab {
	t.Helper()
	pool := r4Pool(t) // Mandatory instance proof BEFORE DROP public.
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal("isolated migration failed")
	}
	st := postgres.New(pool)
	if err := st.CreateRoot(ctx, pool, r4Root, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	policy := postgres.RootPolicy{RemovalGracePeriod: 0, MoveRecognitionHorizon: 0, MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	if err := st.UpsertRootPolicy(ctx, r4Root, policy); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAdapterConfig(ctx, r4Root, postgres.AdapterConfig{CollectorKind: "openlist", Config: []byte(`{"path":"/","expected_base_path":"/r4-synthetic","mirror_presence":true,"topology_token_env":"R4_SYNTHETIC_ONLY"}`)}); err != nil {
		t.Fatal(err)
	}
	return &r4Lab{t: t, ctx: ctx, st: st, coord: postgres.NewCoordinator(st, policy.ReconcileConfig()), qr: postgres.NewQueryReader(pool), source: map[string]domain.SnapshotEntry{}, lat: map[string][]float64{}}
}
func (h *r4Lab) metric(op string, started time.Time) {
	h.lat[op] = append(h.lat[op], float64(time.Since(started).Nanoseconds())/1e6)
}
func (h *r4Lab) add(p string, dir bool) {
	id := "object-" + p
	hash := "hash-" + p
	alg, scope := "sha256", "r4-fixture"
	assurance := domain.IdentityStableWithinScope
	size := int64(10)
	mt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h.source[p] = domain.SnapshotEntry{EntryLocalID: p, Name: path.Base(p), ParentRef: path.Dir(p), IsDir: dir, ProviderObjectID: &id, ProviderObjectIDScope: &scope, ProviderIdentityAssurance: &assurance, ContentHash: &hash, HashAlgorithm: &alg, Size: &size, Mtime: &mt}
}
func (h *r4Lab) tree(n int) {
	for _, p := range []string{"/a", "/ab", "/other"} {
		h.add(p, true)
	}
	for g := 0; len(h.source)+20 <= n; g++ {
		parent := []string{"/a", "/ab", "/other"}[g%3]
		depth := 1 + g%5
		for d := 0; d < depth; d++ {
			parent = path.Join(parent, fmt.Sprintf("g%06d-d%d", g, d))
			h.add(parent, true)
		}
		for f := 0; f < 20-depth; f++ {
			h.add(path.Join(parent, fmt.Sprintf("f%03d.bin", f)), false)
		}
	}
	for len(h.source) < n {
		h.add(fmt.Sprintf("/tail%06d.bin", len(h.source)), false)
	}
}
func (h *r4Lab) entries(scope string) []domain.SnapshotEntry {
	keys := []string{}
	for p, e := range h.source {
		h.traversed++
		if scope == "" || e.ParentRef == scope {
			keys = append(keys, p)
		}
	}
	sort.Strings(keys)
	out := make([]domain.SnapshotEntry, 0, len(keys))
	for _, p := range keys {
		out = append(out, h.source[p])
	}
	return out
}
func (h *r4Lab) draft(entries []domain.SnapshotEntry, scope string, trusted bool) domain.Snapshot {
	root, err := h.st.GetRoot(h.ctx, h.st.Pool(), r4Root)
	if err != nil {
		h.t.Fatal(err)
	}
	fresh, assurance := domain.FreshDirect, domain.StrongFailureVisibility
	count := int64(len(entries))
	s := domain.Snapshot{SnapshotID: reconcile.NewUUID(), RootID: r4Root, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(), TraversalStatus: domain.TraversalSuccess, CompletenessFlag: domain.CompletenessFlagComplete, SkippedScopesKnownEmpty: true, FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &assurance, LifecycleState: domain.SnapshotDraft, EntryCount: &count}
	if scope != "" {
		s.TraversalStatus = domain.TraversalPartial
		s.CompletenessFlag = domain.CompletenessFlagPartial
		fresh = domain.FreshRefreshed
		s.SkippedScopesKnownEmpty = false
		p := map[string]any{"scope": scope, "mode": "r4-mock-direct-children"}
		if trusted {
			ac, err := h.st.GetAdapterConfig(h.ctx, r4Root)
			if err != nil {
				h.t.Fatal(err)
			}
			digest := sha256.Sum256([]byte("independent-fixed-R4-topology"))
			p["directory_coverage"] = domain.DirectoryCoverage{Version: 2, Path: scope, Generation: root.CurrentGeneration, AdapterDigest: postgres.AdapterConfigDigest(ac), EntryCount: len(entries), TopologyDigest: hex.EncodeToString(digest[:])}
		}
		s.Provenance, _ = json.Marshal(p)
	}
	return s
}
func (h *r4Lab) apply(entries []domain.SnapshotEntry, scope string, trusted bool, label string) postgres.ReconcileOutcome {
	h.t.Helper()
	started := time.Now()
	s := h.draft(entries, scope, trusted)
	if err := h.st.CreateDraftSnapshot(h.ctx, s, entries); err != nil {
		h.t.Fatal("persist mock input failed")
	}
	if _, err := h.st.SubmitAndAdmitSnapshot(h.ctx, r4Root, s.SnapshotID); err != nil {
		h.t.Fatal("admission failed")
	}
	out, done, err := h.coord.ProcessHead(h.ctx, r4Root)
	if err != nil || !done {
		h.t.Fatalf("coordinator failed: %v", err)
	}
	h.metric(label, started)
	return out
}

// Verify every Q4/Q5/Q6 result, all retained Q3 tombstones and FULL Q8 transition
// replay. Source exists independently, never reconstructed from Canonical rows.
func (h *r4Lab) verify(label string) map[string]string {
	h.t.Helper()
	ids := map[string]string{}
	rowsByID := map[string]query.ResourceView{}
	var cursor *query.Cursor
	for {
		started := time.Now()
		page, err := h.qr.ListActivePage(h.ctx, r4Root, cursor, 257)
		h.metric("Q6-page", started)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, r := range page.Items {
			if r.CanonicalPath == nil || ids[*r.CanonicalPath] != "" {
				h.t.Fatal("missing/ambiguous canonical path")
			}
			p := *r.CanonicalPath
			want, ok := h.source[p]
			if !ok || r.IsDir == nil || *r.IsDir != want.IsDir {
				h.t.Fatal("inventory not equal to independent source")
			}
			if r.Size == nil || want.Size == nil || *r.Size != *want.Size {
				h.t.Fatal("metadata update not reflected")
			}
			ids[p] = r.ResourceID
			rowsByID[r.ResourceID] = r
		}
		if page.Next == nil {
			break
		}
		cursor = page.Next
	}
	if len(ids) != len(h.source) {
		h.t.Fatal("Q6 omitted source entries")
	}
	parents := map[string]map[string]bool{"/": {}}
	for p, e := range h.source {
		if parents[e.ParentRef] == nil {
			parents[e.ParentRef] = map[string]bool{}
		}
		parents[e.ParentRef][p] = true
	}
	for p, id := range ids {
		started := time.Now()
		resolved, err := h.qr.ResolvePath(h.ctx, r4Root, p, query.ReadOptions{})
		h.metric("Q5", started)
		if err != nil || resolved.Ambiguous || len(resolved.Matches) != 1 || resolved.Matches[0].ResourceID != id {
			h.t.Fatal("Q5 identity mismatch")
		}
	}
	for p, want := range parents {
		var parent *string
		if p != "/" {
			id := ids[p]
			parent = &id
		}
		got := map[string]bool{}
		cursor = nil
		for {
			started := time.Now()
			page, err := h.qr.ListResources(h.ctx, r4Root, parent, query.ReadOptions{}, cursor, 127)
			h.metric("Q4-page", started)
			if err != nil {
				h.t.Fatal(err)
			}
			for _, r := range page.Items {
				if r.CanonicalPath == nil {
					h.t.Fatal("missing Q4 path")
				}
				got[*r.CanonicalPath] = true
			}
			if page.Next == nil {
				break
			}
			cursor = page.Next
		}
		if !reflect.DeepEqual(got, want) {
			h.t.Fatal("Q4 hierarchy differs from source")
		}
	}
	states := map[string]domain.ResourcePresence{}
	var seq, gen int64
	var intra int32
	for {
		started := time.Now()
		events, err := h.qr.ReadJournal(h.ctx, r4Root, seq, 503)
		h.metric("Q8-page", started)
		if err != nil {
			h.t.Fatal(err)
		}
		if len(events) == 0 {
			break
		}
		for _, e := range events {
			if e.EventSeq != seq+1 || e.GenerationNumber < gen || (e.GenerationNumber == gen && e.IntraGenerationSeq <= intra) {
				h.t.Fatal("Q8 ordering violation")
			}
			seq = e.EventSeq
			gen = e.GenerationNumber
			intra = e.IntraGenerationSeq
			if e.ResourceID == nil {
				continue
			}
			id := *e.ResourceID
			prior, seen := states[id]
			switch e.EventType {
			case domain.EventResourceAdded:
				if seen {
					h.t.Fatal("duplicate ADD/tombstone resurrection")
				}
				states[id] = domain.ResourcePresent
			case domain.EventResourceRemoved:
				if !seen || prior != domain.ResourcePresent {
					h.t.Fatal("duplicate REMOVE/unknown ID")
				}
				states[id] = domain.ResourceRemoved
			case domain.EventResourceUpdated, domain.EventResourceMoved, domain.EventResourceRenamed:
				if !seen || prior != domain.ResourcePresent {
					h.t.Fatal("invalid Q8 live transition")
				}
			default:
				h.t.Fatal("unexpected Q8 kind")
			}
		}
	}
	for id, presence := range states {
		r, err := h.qr.GetResource(h.ctx, id, query.ReadOptions{IncludeRemoved: true})
		if err != nil || r.ResourcePresence != presence {
			h.t.Fatal("Q3 differs from Q8 terminal replay")
		}
		if presence == domain.ResourcePresent {
			if _, ok := rowsByID[id]; !ok {
				h.t.Fatal("Q8 PRESENT missing from Q6")
			}
		}
	}
	var total int
	if err := h.st.Pool().QueryRow(h.ctx, `SELECT count(*) FROM index_canonical_resource WHERE root_id=$1`, r4Root).Scan(&total); err != nil || total != len(states) {
		h.t.Fatal("Canonical/Journal ID coverage mismatch")
	}
	root, err := h.qr.GetRoot(h.ctx, r4Root, query.ReadOptions{})
	if err != nil || root.CurrentGeneration < h.gen || root.CurrentGeneration != gen {
		h.t.Fatal("generation mismatch")
	}
	h.gen = root.CurrentGeneration
	h.t.Logf("R4_CHECK %s present=%d all_ids=%d events=%d generation=%d mismatch=0", label, len(ids), total, seq, gen)
	return ids
}
func (h *r4Lab) assertSurvivors(before, after map[string]string) {
	for p, id := range before {
		if h.source[p].EntryLocalID != "" && after[p] != id {
			h.t.Fatal("unaffected survivor identity changed")
		}
	}
}
func (h *r4Lab) noopCycles(scope string) {
	h.t.Helper()
	ids := h.verify("before-repeat")
	seq, _ := h.st.MaxEventSeq(h.ctx, h.st.Pool(), r4Root)
	gen := h.gen
	for i := 0; i < 3; i++ {
		out := h.apply(h.entries(scope), scope, scope != "", "noop")
		if out.Mutated {
			h.t.Fatal("identical observation mutated")
		}
		after := h.verify("repeat")
		tail, _ := h.st.MaxEventSeq(h.ctx, h.st.Pool(), r4Root)
		if !reflect.DeepEqual(ids, after) || seq != tail || gen != h.gen {
			h.t.Fatal("replay not idempotent")
		}
	}
}
func TestP0R4Capacity(t *testing.T) {
	if os.Getenv("INDEXCORE_R4_PROFILE") != "capacity" {
		t.Skip("NOT_RUN: R4 capacity requires guarded isolated runner; CI smoke is separate")
	}
	n, err := strconv.Atoi(os.Getenv("INDEXCORE_R4_N"))
	if err != nil || n < 100 || n > 50000 {
		t.Fatal("invalid R4 N")
	}
	h := r4New(t)
	h.tree(n)
	dirs := 0
	depth := map[int]int{}
	for p, e := range h.source {
		if e.IsDir {
			dirs++
		}
		depth[strings.Count(p, "/")]++
	}
	h.apply(h.entries(""), "", false, "cold-ingestion")
	baseline := h.verify("cold")
	h.noopCycles("")
	for _, pct := range []int{1, 10} { // per-thousand = 0.1% / 1% of total N.
		keys := []string{}
		for p, e := range h.source {
			if !e.IsDir {
				keys = append(keys, p)
			}
		}
		sort.Strings(keys)
		k := n * pct / 1000
		if k < 1 {
			k = 1
		}
		scopes := map[string]bool{}
		for _, p := range keys[:k] {
			e := h.source[p]
			size := *e.Size + int64(pct)
			e.Size = &size
			mt := e.Mtime.Add(time.Duration(pct) * time.Second)
			e.Mtime = &mt
			h.source[p] = e
			scopes[e.ParentRef] = true
		}
		started := time.Now()
		visits := h.traversed
		for _, scope := range sortedKeys(scopes) {
			h.apply(h.entries(scope), scope, true, "scoped-delta")
		}
		after := h.verify("scoped-delta")
		h.assertSurvivors(baseline, after)
		t.Logf("R4_DELTA per_thousand=%d changed_files=%d scopes=%d source_map_visits=%d elapsed_including_verification_ms=%.3f", pct, k, len(scopes), h.traversed-visits, float64(time.Since(started).Microseconds())/1000)
		h.noopCycles(sortedKeys(scopes)[0])
	}
	// Trusted direct-parent deletion, fresh-ID reappearance, equal-content copy.
	files := []string{}
	for p, e := range h.source {
		if !e.IsDir {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	victim := files[0]
	old := h.source[victim]
	oldID := baseline[victim]
	delete(h.source, victim)
	h.apply(h.entries(path.Dir(victim)), path.Dir(victim), true, "delete")
	h.verify("deleted")
	row, err := h.qr.GetResource(h.ctx, oldID, query.ReadOptions{IncludeRemoved: true})
	if err != nil || row.ResourcePresence != domain.ResourceRemoved {
		t.Fatal("trusted deletion did not retain tombstone")
	}
	h.source[victim] = old
	h.apply(h.entries(old.ParentRef), old.ParentRef, true, "reappear")
	current := h.verify("reappeared")
	if current[victim] == oldID {
		t.Fatal("tombstone ID reused")
	}
	copyPath := path.Join(old.ParentRef, "same-content-copy.bin")
	h.add(copyPath, false)
	copyEntry := h.source[copyPath]
	copyEntry.ContentHash = old.ContentHash
	copyEntry.Size = old.Size
	copyEntry.Mtime = old.Mtime
	h.source[copyPath] = copyEntry
	h.apply(h.entries(old.ParentRef), old.ParentRef, true, "copy")
	current = h.verify("distinct-copy")
	if current[copyPath] == current[victim] {
		t.Fatal("equal content merged identities")
	}
	h.noopCycles(old.ParentRef)
	// Non-authoritative omissions never remove descendants or siblings.
	before := current
	h.apply(nil, "/", false, "partial-empty")
	current = h.verify("partial-absence")
	if !reflect.DeepEqual(before, current) {
		t.Fatal("PARTIAL removed survivors")
	}
	// Root direct-child proof cannot infer deep file absence.
	h.apply(h.entries("/"), "/", true, "root-direct")
	current = h.verify("root-boundary")
	if !reflect.DeepEqual(before, current) {
		t.Fatal("root coverage removed descendants")
	}
	h.apply(h.entries(""), "", false, "full-refresh-after-delta")
	h.verify("final-full")
	h.noopCycles(old.ParentRef)
	o := diagnostic.Defaults()
	o.RowLimit = 100
	r, err := postgres.NewDiagnosticReader(h.st.Pool()).Read(h.ctx, r4Root, o)
	if err != nil || !r.Complete || r.Overall == "OK" || (n > 100 && r.Inventory.CanonicalExact) {
		t.Fatal("capped diagnostic false green")
	}
	var bytes int64
	if err = h.st.Pool().QueryRow(h.ctx, `SELECT pg_database_size(current_database())`).Scan(&bytes); err != nil {
		t.Fatal(err)
	}
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		t.Fatal("OS rusage unavailable")
	}
	stats := map[string]any{}
	for op, samples := range h.lat {
		sort.Float64s(samples)
		quant := func(p float64) float64 { i := int(float64(len(samples)-1) * p); return samples[i] }
		stats[op] = map[string]any{"samples": len(samples), "p50_ms": quant(.5), "p95_ms": quant(.95), "p99_ms": quant(.99)}
	}
	data, _ := json.Marshal(map[string]any{"N_initial_resources": n, "initial_dirs": dirs, "initial_files": n - dirs, "initial_depth_histogram": depth, "db_bytes": bytes, "os_peak_rss_kib": usage.Maxrss, "user_cpu_seconds": float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6, "system_cpu_seconds": float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6, "source_map_visits": h.traversed, "latencies": stats, "quantile_method": "floor((n-1)*p), individual API/pipeline calls, monotonic wall clock", "mismatches": 0})
	t.Log("R4_CAPACITY " + string(data))
}
func sortedKeys(m map[string]bool) []string {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
