package scan_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func f138Claim(t *testing.T, st *postgres.Store, root, scope string) state.DirtyScopeWork {
	t.Helper()
	w, err := st.MergeSignal(context.Background(), state.DirtySignal{RootID: root, ScopeKey: scope, Source: state.SourceMutationHint, Reason: state.ReasonPossibleChange, Priority: state.PriorityHigh, SeenAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	w, err = st.ClaimWork(context.Background(), root, scope, w.Version, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func f138Setup(t *testing.T) (*postgres.Store, *alistScopeMock, string, *scan.FetchPrototype, state.DirtyScopeWork) {
	t.Helper()
	testutil.Issue138Pool(t) // before inherited helper resets the schema
	st, mock, root := setupScopedRootPath(t, "/library")
	mock.set("/library", fileEntry("one.bin", 10, "aa"))
	p, err := scan.NewFetchPrototype(p4ScanService(st), 4)
	if err != nil {
		t.Fatal(err)
	}
	return st, mock, root, p, f138Claim(t, st, root, "/")
}

func f138Count(t *testing.T, st *postgres.Store, table string) int {
	t.Helper()
	n := 0
	if err := st.Pool().QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIssue138FetchIsReadOnlyAndHandleIsSingleUse(t *testing.T) {
	st, _, root, p, claim := f138Setup(t)
	before, err := st.GetWork(context.Background(), root, "/")
	if err != nil {
		t.Fatal(err)
	}
	f, err := p.FetchScope(context.Background(), claim, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if f.Stats().Entries != 1 || f.Stats().ObservedAt.IsZero() {
		t.Fatal(f.Stats())
	}
	for _, table := range []string{"index_snapshot", "index_admission", "index_canonical_resource", "index_journal_event"} {
		if n := f138Count(t, st, table); n != 0 {
			t.Fatal("fetch wrote", table, n)
		}
	}
	after, _ := st.GetWork(context.Background(), root, "/")
	if before.Version != after.Version {
		t.Fatal("fetch changed work")
	}
	copyHandle := *f // exported handle copies must share the same consumption token
	res, err := p.CommitFetchedScope(context.Background(), f)
	if err != nil || res.Outcome.Status != domain.AdmissionApplied {
		t.Fatal(res, err)
	}
	if _, err := p.CommitFetchedScope(context.Background(), &copyHandle); !errors.Is(err, scan.ErrFetchConsumed) {
		t.Fatal(err)
	}
	if p.DiscardFetchedScope(&copyHandle) {
		t.Fatal("consumed handle discarded twice")
	}
	if n := f138Count(t, st, "index_journal_event"); n != 1 {
		t.Fatal(n)
	}
	paths := activePaths(t, postgres.NewQueryReader(st.Pool()), root)
	if !paths["/library/one.bin"] {
		t.Fatal(paths)
	}
	var observed time.Time
	if err := st.Pool().QueryRow(context.Background(), `SELECT observed_at FROM index_snapshot WHERE snapshot_id=$1`, res.SnapshotID).Scan(&observed); err != nil || !observed.Equal(f.Stats().ObservedAt) {
		t.Fatal("source time replaced", observed, err)
	}
}

func TestIssue138FetchGenerationConfigAndClaimFences(t *testing.T) {
	for _, change := range []string{"adapter-ABA", "adapter-removed", "generation", "root-deprecated", "claim-recovery"} {
		t.Run(change, func(t *testing.T) {
			st, _, root, p, claim := f138Setup(t)
			f, err := p.FetchScope(context.Background(), claim, 1000)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "adapter-ABA":
				a, err := st.GetAdapterConfig(context.Background(), root)
				if err != nil {
					t.Fatal(err)
				}
				other := a
				other.Config = []byte(`{"base_url":"http://127.0.0.1:1","path":"/changed"}`)
				if err := st.UpsertAdapterConfig(context.Background(), root, other); err != nil {
					t.Fatal(err)
				}
				if err := st.UpsertAdapterConfig(context.Background(), root, a); err != nil {
					t.Fatal(err)
				}
			case "adapter-removed":
				if _, err := st.Pool().Exec(context.Background(), `DELETE FROM index_root_adapter_config WHERE root_id=$1`, root); err != nil {
					t.Fatal(err)
				}
			case "generation":
				if _, err := p4ScanService(st).ScanScope(context.Background(), root, "/", 1000); err != nil {
					t.Fatal(err)
				}
			case "root-deprecated":
				if _, err := st.TransitionRootLifecycle(context.Background(), root, domain.RootDeprecated); err != nil {
					t.Fatal(err)
				}
			case "claim-recovery":
				if _, err := st.RecoverStaleInflight(context.Background(), root, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			before := f138Count(t, st, "index_journal_event")
			if _, err := p.CommitFetchedScope(context.Background(), f); !errors.Is(err, postgres.ErrScopedFetchStale) {
				t.Fatalf("stale result accepted: %v", err)
			}
			if got := f138Count(t, st, "index_journal_event"); got != before {
				t.Fatal("stale result wrote Journal", before, got)
			}
		})
	}
}

func TestIssue138LateHintPreservesPendingEpoch(t *testing.T) {
	st, _, root, p, claim := f138Setup(t)
	f, err := p.FetchScope(context.Background(), claim, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MergeSignal(context.Background(), state.DirtySignal{RootID: root, ScopeKey: "/", Source: state.SourceMutationHint, Reason: state.ReasonPossibleChange, Priority: state.PriorityHigh, SeenAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CommitFetchedScope(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	w, err := st.CompleteSuccess(context.Background(), root, "/", *claim.ClaimedSignalSeq, time.Now().UTC())
	if err != nil || w.WorkState != state.WorkPending || w.SignalSeq != 2 || w.LastVerifiedSignalSeq == nil || *w.LastVerifiedSignalSeq != 1 {
		t.Fatal(w, err)
	}
}

func TestIssue138NoPostCancelAdmissionOrReusedSlot(t *testing.T) {
	st, _, root, p, claim := f138Setup(t)
	f, err := p.FetchScope(context.Background(), claim, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.FetchScope(context.Background(), claim, 1000); !errors.Is(err, scan.ErrFetchBusy) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.CommitFetchedScope(ctx, f); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f138Count(t, st, "index_snapshot") != 0 {
		t.Fatal("canceled result created Snapshot")
	}
	if _, err := st.RecoverStaleInflight(context.Background(), root, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	nextClaim := f138Claim(t, st, root, "/")
	next, err := p.FetchScope(context.Background(), nextClaim, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if p.DiscardFetchedScope(f) {
		t.Fatal("old token released a later reservation")
	}
	if _, err := p.FetchScope(context.Background(), nextClaim, 1000); !errors.Is(err, scan.ErrFetchBusy) {
		t.Fatal(err)
	}
	if !p.DiscardFetchedScope(next) {
		t.Fatal("discard failed")
	}
}

func TestIssue138ProviderFailuresDoNotAdmit(t *testing.T) {
	for _, kind := range []string{"429", "500", "decode", "timeout", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			testutil.Issue138Pool(t)
			var calls atomic.Int64
			entered := make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				calls.Add(1)
				entered <- struct{}{}
				switch kind {
				case "timeout", "cancel":
					<-r.Context().Done()
				case "429":
					w.WriteHeader(429)
				case "500":
					w.WriteHeader(500)
				default:
					io.WriteString(w, "not json")
				}
			}))
			defer srv.Close()
			st, ctx := p4ScanSetup(t, "openlist", srv.URL, "/", true, domain.RootActive)
			p, err := scan.NewFetchPrototype(scan.New(st, "", "", 100*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil))), 1)
			if err != nil {
				t.Fatal(err)
			}
			claim := f138Claim(t, st, p4ScopeRootID, "/")
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			if kind == "cancel" {
				go func() { <-entered; cancel() }()
			}
			if _, err := p.FetchScope(ctx, claim, 1000); err == nil {
				t.Fatal("failed source accepted")
			}
			if calls.Load() != 1 || f138Count(t, st, "index_snapshot") != 0 || f138Count(t, st, "index_journal_event") != 0 {
				t.Fatal("unexpected retry/admission", calls.Load())
			}
		})
	}
}

// Raw metadata is opaque to callers; explicit JSON cannot leak config or facts.
func TestIssue138HandleCannotSerializeMutableEvidence(t *testing.T) {
	_, _, _, p, claim := f138Setup(t)
	f, err := p.FetchScope(context.Background(), claim, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer p.DiscardFetchedScope(f)
	b, err := json.Marshal(f)
	if err != nil || string(b) != "{}" {
		t.Fatal(string(b), err)
	}
}

func TestIssue138ConcurrentHandleCommitOnlyOnce(t *testing.T) {
	st, _, _, p, claim := f138Setup(t)
	f, err := p.FetchScope(context.Background(), claim, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var accepted atomic.Int64
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.CommitFetchedScope(context.Background(), f)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, scan.ErrFetchConsumed) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if accepted.Load() != 1 || f138Count(t, st, "index_journal_event") != 1 {
		t.Fatal("duplicate commit", accepted.Load())
	}
}

func TestIssue138Stage2RechecksFencesAfterAdmission(t *testing.T) {
	st, _, root, p, claim := f138Setup(t)
	f, err := p.FetchScope(context.Background(), claim, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Owned PG18 ONLY: deterministic fault between short admission and Stage2.
	// A config rewrite with the same content must invalidate the tuple revision.
	_, err = st.Pool().Exec(context.Background(), `CREATE FUNCTION issue138_after_admission() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE index_root_adapter_config SET config=config WHERE root_id=NEW.root_id; RETURN NEW; END $$;
CREATE TRIGGER issue138_after_admission AFTER INSERT ON index_admission FOR EACH ROW EXECUTE FUNCTION issue138_after_admission()`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.CommitFetchedScope(context.Background(), f)
	if err != nil || res.Outcome.Status != domain.AdmissionStaleInput {
		t.Fatal("Stage2 bypassed changed config", res, err)
	}
	if f138Count(t, st, "index_canonical_resource") != 0 || f138Count(t, st, "index_journal_event") != 0 {
		t.Fatal("stale observation applied")
	}
	w, err := st.GetWork(context.Background(), root, "/")
	if err != nil || w.WorkState != state.WorkInFlight {
		t.Fatal("commit acknowledged work by itself", w, err)
	}
}

func TestIssue138DatabaseTimeRejectsExpiredObservation(t *testing.T) {
	st, _, _, p, claim := f138Setup(t)
	f, err := p.FetchScope(context.Background(), claim, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Fault injection changes only this disposable Snapshot input. The Store's
	// clock_timestamp(), not an application's refreshed timestamp, decides TTL.
	_, err = st.Pool().Exec(context.Background(), `CREATE FUNCTION issue138_expired() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.observed_at=clock_timestamp()-interval '121 seconds'; RETURN NEW; END $$;
CREATE TRIGGER issue138_expired BEFORE INSERT ON index_snapshot FOR EACH ROW EXECUTE FUNCTION issue138_expired()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.CommitFetchedScope(context.Background(), f); !errors.Is(err, postgres.ErrScopedFetchStale) {
		t.Fatal(err)
	}
	if f138Count(t, st, "index_admission") != 0 || f138Count(t, st, "index_journal_event") != 0 {
		t.Fatal("expired observation admitted")
	}
}

func TestIssue138RestartRejectsOldAdmissionThenAllowsFreshClaim(t *testing.T) {
	st, _, root, p, claim := f138Setup(t)
	r, _ := st.GetRoot(context.Background(), st.Pool(), root)
	a, _ := st.GetAdapterConfig(context.Background(), root)
	meta, _ := json.Marshal(map[string]any{"scoped_fetch_fence": postgres.ScopedFetchFence{
		Version: 1, Generation: r.CurrentGeneration, AdmissionFrontier: r.LatestAdmissionSeq,
		AdapterDigest: postgres.AdapterConfigDigest(a), AdapterRevision: a.Revision, ScopeKey: "/", TargetPath: "/library",
		ClaimedSequence: *claim.ClaimedSignalSeq, Attempt: claim.AttemptCount, AttemptStartedAt: *claim.LastAttemptStartedAt,
	}})
	fresh, assurance, count := domain.FreshRefreshed, domain.WeakFailureVisibility, int64(0)
	snap := domain.Snapshot{SnapshotID: reconcile.NewUUID(), RootID: root, Provenance: meta, ObservedAt: time.Now().UTC(), TraversalStatus: domain.TraversalPartial, CompletenessFlag: domain.CompletenessFlagPartial, LifecycleState: domain.SnapshotDraft, FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &assurance, EntryCount: &count}
	if err := st.CreateDraftSnapshot(context.Background(), snap, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SubmitAndAdmitSnapshot(context.Background(), root, snap.SnapshotID); err != nil {
		t.Fatal(err)
	}
	// The old process is absent at this durable boundary. Reopen the store and
	// use unmodified startup recovery, not a direct SQL repair of the claim.
	restarted := postgres.New(testutil.Pool(t))
	if n, err := restarted.RecoverStaleInflight(context.Background(), root, time.Now().UTC()); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	policy, err := restarted.RootReconcileConfig(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	out, done, err := postgres.NewCoordinator(restarted, policy).ProcessHead(context.Background(), root)
	if err != nil || !done || out.Status != domain.AdmissionStaleInput {
		t.Fatal(out, done, err)
	}
	if f138Count(t, st, "index_journal_event") != 0 {
		t.Fatal("old receipt changed truth")
	}
	next := f138Claim(t, st, root, "/")
	if next.AttemptCount != claim.AttemptCount+1 {
		t.Fatal("attempt lineage reset", next)
	}
	f, err := p.FetchScope(context.Background(), next, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := p.CommitFetchedScope(context.Background(), f); err != nil || res.Outcome.Status != domain.AdmissionApplied {
		t.Fatal(res, err)
	}
	if f138Count(t, st, "index_journal_event") != 1 {
		t.Fatal("fresh retry did not converge once")
	}
}

func TestIssue138ParentIdentityCannotBeReusedAtSamePath(t *testing.T) {
	st, mock, root, p, _ := f138Setup(t)
	mock.set("/library", dirEntry("sub"))
	if _, err := p4ScanService(st).ScanScope(context.Background(), root, "/", 1000); err != nil {
		t.Fatal(err)
	}
	mock.set("/library/sub", fileEntry("late.bin", 10, ""))
	claim := f138Claim(t, st, root, "/sub")
	f, err := p.FetchScope(context.Background(), claim, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately corrupt only owned fixture lineage without advancing root
	// generation, proving parent identity is independently checked, not path-only.
	_, err = st.Pool().Exec(context.Background(), `UPDATE index_canonical_resource SET canonical_path='/library/moved' WHERE root_id=$1 AND canonical_path='/library/sub'`, root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Pool().Exec(context.Background(), `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path,name,is_dir)
VALUES ($2,$1,1,1,'PRESENT','NONE','/library/sub','sub',true)`, root, reconcile.NewUUID())
	if err != nil {
		t.Fatal(err)
	}
	before := f138Count(t, st, "index_journal_event")
	if _, err := p.CommitFetchedScope(context.Background(), f); !errors.Is(err, postgres.ErrScopedFetchStale) {
		t.Fatal(err)
	}
	if before != f138Count(t, st, "index_journal_event") {
		t.Fatal("replacement parent accepted old child observation")
	}
}

func TestIssue138PrototypeBoundsAndForeignOwnership(t *testing.T) {
	st, _, _, p, claim := f138Setup(t)
	if _, err := scan.NewFetchPrototype(p4ScanService(st), 9); err == nil {
		t.Fatal("unbounded reader count accepted")
	}
	if _, err := p.FetchScope(context.Background(), claim, 1001); err == nil {
		t.Fatal("entry cap widened")
	} else {
		assertScanScopeKind(t, err, scan.ScopeFailureConfigInvalid)
	}
	bad := claim
	bad.ScopeKey = "/a/b/c/d/e/f/g/h/i"
	if _, err := p.FetchScope(context.Background(), bad, 1000); err == nil {
		t.Fatal("depth cap widened")
	} else {
		assertScanScopeKind(t, err, scan.ScopeFailureInvalidScope)
	}
	if f138Count(t, st, "index_snapshot") != 0 {
		t.Fatal("invalid input wrote Snapshot")
	}
	f, err := p.FetchScope(context.Background(), claim, 1000)
	if err != nil {
		t.Fatal(err)
	}
	other, err := scan.NewFetchPrototype(p4ScanService(st), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.CommitFetchedScope(context.Background(), f); !errors.Is(err, scan.ErrFetchConsumed) {
		t.Fatal(err)
	}
	if other.DiscardFetchedScope(f) {
		t.Fatal("foreign discard consumed the real owner's token")
	}
	if _, err := p.CommitFetchedScope(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

func TestIssue138MirrorVirtualOmissionAndQualifiedRemoval(t *testing.T) {
	testutil.Issue138Pool(t)
	st, mock, root := setupScopedRoot(t)
	t.Setenv("ISSUE138_TOPOLOGY", "fixture-only")
	var virtual atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			io.WriteString(w, `{"code":200,"data":{"base_path":"/drive/library"}}`)
		case "/api/admin/storage/list":
			stores := []map[string]any{{"id": 1, "mount_path": "/drive", "driver": "115 Open", "status": "work", "disabled": false}}
			if virtual.Load() {
				stores = append(stores, map[string]any{"id": 2, "mount_path": "/drive/library/alias", "driver": "Alias", "status": "work", "disabled": false})
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": len(stores), "content": stores}})
		default:
			mock.handler()(w, r)
		}
	}))
	defer srv.Close()
	cfg, _ := json.Marshal(map[string]any{"base_url": srv.URL, "path": "/", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_token_env": "ISSUE138_TOPOLOGY"})
	if err := st.UpsertAdapterConfig(context.Background(), root, postgres.AdapterConfig{CollectorKind: "openlist", Config: cfg}); err != nil {
		t.Fatal(err)
	}
	svc := p4ScanService(st)
	mock.set("/", dirEntry("gallery"))
	if _, err := svc.ScanScope(context.Background(), root, "/", 1000); err != nil {
		t.Fatal(err)
	}
	mock.set("/gallery", fileEntry("keep.txt", 10, ""))
	if _, err := svc.ScanScope(context.Background(), root, "/gallery", 1000); err != nil {
		t.Fatal(err)
	}
	p, err := scan.NewFetchPrototype(svc, 2)
	if err != nil {
		t.Fatal(err)
	}
	observe := func() {
		t.Helper()
		claim := f138Claim(t, st, root, "/")
		f, err := p.FetchScope(context.Background(), claim, 1000)
		if err != nil {
			t.Fatal(err)
		}
		res, err := p.CommitFetchedScope(context.Background(), f)
		if err != nil || (res.Outcome.Status != domain.AdmissionApplied && res.Outcome.Status != domain.AdmissionNoop) {
			t.Fatal(res, err)
		}
		if _, err := st.CompleteSuccess(context.Background(), root, "/", *claim.ClaimedSignalSeq, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	virtual.Store(true)
	mock.set("/", dirEntry("alias"))
	observe()
	paths := activePaths(t, postgres.NewQueryReader(st.Pool()), root)
	if !paths["/gallery/keep.txt"] || !paths["/gallery"] {
		t.Fatal("virtual omission removed real inventory", paths)
	}
	virtual.Store(false)
	mock.set("/")
	observe()
	paths = activePaths(t, postgres.NewQueryReader(st.Pool()), root)
	if paths["/gallery"] || paths["/gallery/keep.txt"] {
		t.Fatal("qualified removal failed", paths)
	}
	var removed int
	if err := st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM index_journal_event WHERE event_type='resource-removed'`).Scan(&removed); err != nil || removed != 3 {
		t.Fatal("wrong removal journal", removed, err)
	}
}
