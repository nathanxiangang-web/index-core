package scan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

type s138Source struct {
	calls, active, peak atomic.Int64
	delay               atomic.Int64
	code                atomic.Int64
	virtual             atomic.Bool
	mu                  sync.Mutex
	replies             []string
}

func s138Setup(t *testing.T, n, workers int, mirror bool) (*postgres.Store, *alistScopeMock, string, *scan.SameRootPrototype, []state.DirtyScopeWork, *s138Source) {
	t.Helper()
	testutil.Issue138Pool(t)
	st, m, root := setupScopedRoot(t)
	dirs := []map[string]any{}
	for i := 0; i < n; i++ {
		dirs = append(dirs, dirEntry(fmt.Sprintf("d%03d", i)))
	}
	m.set("/", dirs...)
	if _, err := p4ScanService(st).ScanScope(context.Background(), root, "/", 1000); err != nil {
		t.Fatal(err)
	}
	src := &s138Source{}
	src.code.Store(200)
	t.Setenv("ISSUE138_LINEAGE_TOPOLOGY", "fixture-only")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			io.WriteString(w, `{"code":200,"data":{"base_path":"/drive/library"}}`)
			return
		case "/api/admin/storage/list":
			stores := []map[string]any{{"id": 1, "mount_path": "/drive", "driver": "115 Open", "status": "work", "disabled": false}}
			if src.virtual.Load() {
				stores = append(stores, map[string]any{"id": 2, "mount_path": "/drive/library/d001", "driver": "Alias", "status": "work", "disabled": false})
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": len(stores), "content": stores}})
			return
		case "/api/fs/list":
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var req struct {
				Path    string
				Refresh bool
			}
			json.Unmarshal(raw, &req)
			if !req.Refresh {
				http.Error(w, "fixture requires refresh", 400)
				return
			}
			src.calls.Add(1)
			cur := src.active.Add(1)
			defer src.active.Add(-1)
			for old := src.peak.Load(); cur > old; old = src.peak.Load() {
				if src.peak.CompareAndSwap(old, cur) {
					break
				}
			}
			if d := src.delay.Load(); d > 0 {
				duration := time.Duration(d)
				if req.Path == "/d000" {
					duration *= 3
				}
				timer := time.NewTimer(duration)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-r.Context().Done():
					return
				}
			}
			if code := src.code.Load(); code != 200 {
				json.NewEncoder(w).Encode(map[string]any{"code": code, "data": nil})
				return
			}
			m.handler()(w, r)
			src.mu.Lock()
			src.replies = append(src.replies, req.Path)
			src.mu.Unlock()
			return
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cfg, _ := json.Marshal(map[string]any{"base_url": srv.URL, "path": "/", "mirror_presence": mirror, "expected_base_path": "/drive/library", "topology_token_env": "ISSUE138_LINEAGE_TOPOLOGY"})
	if err := st.UpsertAdapterConfig(context.Background(), root, postgres.AdapterConfig{CollectorKind: "openlist", Config: cfg}); err != nil {
		t.Fatal(err)
	}
	p, err := scan.NewSameRootPrototype(p4ScanService(st), workers)
	if err != nil {
		t.Fatal(err)
	}
	claims := make([]state.DirtyScopeWork, n)
	for i := range claims {
		scope := fmt.Sprintf("/d%03d", i)
		m.set(scope, fileEntry("late.bin", int64(100+i), fmt.Sprintf("hash-%d", i)))
		claims[i] = f138Claim(t, st, root, scope)
	}
	return st, m, root, p, claims, src
}

func s138Complete(t *testing.T, st *postgres.Store, c state.DirtyScopeWork) {
	t.Helper()
	if _, err := st.CompleteSuccess(context.Background(), c.RootID, c.ScopeKey, *c.ClaimedSignalSeq, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestIssue138SameRootSiblingOrderAndSourceTime(t *testing.T) {
	st, _, root, p, claims, src := s138Setup(t, 2, 2, true)
	src.delay.Store(int64(30 * time.Millisecond))
	before, _ := st.GetRoot(context.Background(), st.Pool(), root)
	b, err := p.FetchBatch(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if src.calls.Load() != 2 || src.peak.Load() != 2 {
		t.Fatal("not actual same-root overlap", src.calls.Load(), src.peak.Load())
	}
	src.mu.Lock()
	first := src.replies[0]
	src.mu.Unlock()
	if first != "/d001" {
		t.Fatal("fixture did not reverse HTTP replies", first)
	}
	if _, err := p.Commit(context.Background(), b, 1); !errors.Is(err, scan.ErrReadOrder) {
		t.Fatal("arrival order admitted", err)
	}
	copyHandle := *b
	for i, c := range claims {
		res, err := p.Commit(context.Background(), b, i)
		if err != nil {
			t.Fatal(i, err)
		}
		snap, err := st.GetSnapshot(context.Background(), st.Pool(), res.SnapshotID)
		if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			Fence    postgres.ScopedFetchFence `json:"scoped_fetch_fence"`
			Coverage domain.DirectoryCoverage  `json:"directory_coverage"`
		}
		json.Unmarshal(snap.Provenance, &meta)
		if meta.Fence.Generation != before.CurrentGeneration || meta.Coverage.Generation != before.CurrentGeneration || meta.Coverage.Version != 3 || snap.ObservedAt.IsZero() {
			t.Fatal("source generation/time was rebased", meta, snap.ObservedAt)
		}
		if res.Outcome.Generation != before.CurrentGeneration+int64(i) {
			t.Fatal("wrong committed prefix", res)
		}
		s138Complete(t, st, c)
	}
	if _, err := p.Commit(context.Background(), &copyHandle, 0); !errors.Is(err, scan.ErrFetchConsumed) {
		t.Fatal("copied handle replayed", err)
	}
	events, err := postgres.NewQueryReader(st.Pool()).ReadJournal(context.Background(), root, 2, 20)
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	for i, e := range events {
		r, err := postgres.NewQueryReader(st.Pool()).ResolvePath(context.Background(), root, fmt.Sprintf("/d%03d/late.bin", i), query.ReadOptions{})
		if err != nil || len(r.Matches) != 1 || e.ResourceID == nil || r.Matches[0].ResourceID != *e.ResourceID {
			t.Fatal("wrong Q5/Q8", i, r, e, err)
		}
	}
}

func TestIssue138SameRootConflictingIdentityRefusesOldSibling(t *testing.T) {
	st, m, root, p, claims, _ := s138Setup(t, 2, 2, true)
	m.set("/d000", fileEntry("late.bin", 100, "shared"))
	m.set("/d001", fileEntry("late.bin", 100, "shared"))
	b, err := p.FetchBatch(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := p.Commit(context.Background(), b, 0); err != nil {
		t.Fatal(err)
	}
	s138Complete(t, st, claims[0])
	journal := f138Count(t, st, "index_journal_event")
	if _, err := p.Commit(context.Background(), b, 1); !errors.Is(err, postgres.ErrScopedFetchStale) {
		t.Fatal("shared identity dependency ignored", err)
	}
	if f138Count(t, st, "index_journal_event") != journal {
		t.Fatal("stale sibling wrote Q8")
	}
	// Existing recovery/reclaim and serial source read, never mutate old evidence.
	if _, err := st.RecoverStaleInflight(context.Background(), root, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	next := f138Claim(t, st, root, claims[1].ScopeKey)
	if next.AttemptCount <= claims[1].AttemptCount {
		t.Fatal("no new claim epoch")
	}
	if _, err := p4ScanService(st).ScanScope(context.Background(), root, next.ScopeKey, 1000); err != nil {
		t.Fatal(err)
	}
	s138Complete(t, st, next)
	if len(activePaths(t, postgres.NewQueryReader(st.Pool()), root)) != 4 {
		t.Fatal("serial fallback lost equal-content copy")
	}
}

func TestIssue138SameRootChangedStateFailsClosed(t *testing.T) {
	for _, change := range []string{"adapter-ABA", "policy-ABA", "claim-recovery", "parent-replaced", "unexpected-admission", "expired", "stage2-dependency"} {
		t.Run(change, func(t *testing.T) {
			st, _, root, p, claims, _ := s138Setup(t, 2, 2, true)
			b, err := p.FetchBatch(context.Background(), claims)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			switch change {
			case "adapter-ABA":
				a, _ := st.GetAdapterConfig(context.Background(), root)
				if err := st.UpsertAdapterConfig(context.Background(), root, a); err != nil {
					t.Fatal(err)
				}
			case "policy-ABA":
				policy, _ := st.GetRootPolicy(context.Background(), root)
				if err := st.UpsertRootPolicy(context.Background(), root, policy); err != nil {
					t.Fatal(err)
				}
			case "claim-recovery":
				if _, err := st.RecoverStaleInflight(context.Background(), root, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			case "parent-replaced":
				if _, err := st.Pool().Exec(context.Background(), `UPDATE index_canonical_resource SET canonical_path='/moved' WHERE canonical_path='/d000'`); err != nil {
					t.Fatal(err)
				}
			case "unexpected-admission":
				if _, err := p4ScanService(st).ScanScope(context.Background(), root, "/d001", 1000); err != nil {
					t.Fatal(err)
				}
			case "expired":
				if _, err := st.Pool().Exec(context.Background(), `UPDATE index_snapshot SET observed_at=clock_timestamp()-interval '121 seconds' WHERE snapshot_id=$1::uuid`, b.Checkpoint()); err != nil {
					t.Fatal(err)
				}
			case "stage2-dependency":
				if _, err := st.Pool().Exec(context.Background(), `CREATE FUNCTION issue138_lineage_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE index_canonical_resource SET name='changed' WHERE canonical_path='/d000'; RETURN NEW; END $$; CREATE TRIGGER issue138_lineage_fault AFTER INSERT ON index_admission FOR EACH ROW EXECUTE FUNCTION issue138_lineage_fault()`); err != nil {
					t.Fatal(err)
				}
			}
			before := f138Count(t, st, "index_journal_event")
			if _, err := p.Commit(context.Background(), b, 0); !errors.Is(err, postgres.ErrScopedFetchStale) {
				t.Fatal(change, err)
			}
			if f138Count(t, st, "index_journal_event") != before {
				t.Fatal("stale state wrote Q8")
			}
		})
	}
}

func TestIssue138SameRootResumeFrozenEvidenceAndLateHint(t *testing.T) {
	st, _, root, p, claims, src := s138Setup(t, 2, 2, true)
	b, err := p.FetchBatch(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := b.Checkpoint()
	if _, err := p.Commit(context.Background(), b, 0); err != nil {
		t.Fatal(err)
	}
	s138Complete(t, st, claims[0])
	b.Close()
	if _, err := st.MergeSignal(context.Background(), state.DirtySignal{RootID: root, ScopeKey: claims[1].ScopeKey, Source: state.SourceMutationHint, Reason: state.ReasonPossibleChange, Priority: state.PriorityHigh, SeenAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	restarted, err := scan.NewSameRootPrototype(p4ScanService(postgres.New(testutil.Pool(t))), 2)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := restarted.ResumeBatch(context.Background(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if _, err := restarted.Commit(context.Background(), resumed, 1); err != nil {
		t.Fatal(err)
	}
	s138Complete(t, st, claims[1])
	if src.calls.Load() != 2 {
		t.Fatal("resume refetched", src.calls.Load())
	}
	w, err := st.GetWork(context.Background(), root, claims[1].ScopeKey)
	if err != nil || w.WorkState != state.WorkPending || w.LastVerifiedSignalSeq == nil || *w.LastVerifiedSignalSeq != *claims[1].ClaimedSignalSeq {
		t.Fatal("late Hint lost", w, err)
	}
}

func TestIssue138SameRootRejectsAncestorCancelAndFailedSource(t *testing.T) {
	st, _, root, p, claims, src := s138Setup(t, 2, 2, true)
	bad := append([]state.DirtyScopeWork(nil), claims...)
	bad[1].ScopeKey = "/d000/child"
	if _, err := p.FetchBatch(context.Background(), bad); !errors.Is(err, scan.ErrReadOrder) || src.calls.Load() != 0 {
		t.Fatal("ancestor eligible", err, src.calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.FetchBatch(ctx, claims); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	before := f138Count(t, st, "index_snapshot")
	src.code.Store(429)
	if _, err := p.FetchBatch(context.Background(), claims); err == nil {
		t.Fatal("429 frozen")
	}
	if f138Count(t, st, "index_snapshot") != before {
		t.Fatal("failed source persisted partial batch")
	}
	src.code.Store(200)
	b, err := p.FetchBatch(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := p.Commit(ctx, b, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	w, err := st.GetWork(context.Background(), root, claims[0].ScopeKey)
	if err != nil || w.WorkState != state.WorkInFlight {
		t.Fatal("cancel verified work", w, err)
	}
}
