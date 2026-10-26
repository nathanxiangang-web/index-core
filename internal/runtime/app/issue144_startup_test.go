package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Driver-only barrier after the real pending-head SELECT, before Stage2 takes
// its root lock. No production failpoint, mocked Store or timed race winner.
type issue144HeadGate struct {
	paused, release chan struct{}
	once            sync.Once
}
type issue144HeadKey struct{}

func (g *issue144HeadGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, issue144HeadKey{}, strings.Contains(d.SQL, "SELECT admission_seq, snapshot_id::text FROM index_admission"))
}
func (g *issue144HeadGate) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	if d.Err == nil && ctx.Value(issue144HeadKey{}) == true {
		g.once.Do(func() {
			close(g.paused)
			select {
			case <-g.release:
			case <-ctx.Done():
			}
		})
	}
}

type issue144CycleResult struct {
	result incrementalorch.Result
	err    error
}
type issue144Tap struct {
	inner  incrementalruntime.CycleRunner
	result chan issue144CycleResult
}

func (p issue144Tap) RunCycle(ctx context.Context, cfg incrementalorch.Config) (incrementalorch.Result, error) {
	r, e := p.inner.RunCycle(ctx, cfg)
	select {
	case p.result <- issue144CycleResult{r, e}:
	default:
	}
	return r, e
}

// Stage1 is seeded through the real draft/admission APIs with its ORIGINAL old
// collector timestamp. This models a legacy writer stopped after admission;
// no UPDATE/rebase of observed_at or fake terminal status is used.
func issue144OldSubmitted(t *testing.T) (*postgres.Store, *poolHTTP, string, string, string) {
	t.Helper()
	st, h, _, roots := poolFixture(t, 1, 1)
	ctx := context.Background()
	root := roots[0]
	scope := "/d000000"
	lock, e := st.AcquireWriterLock(ctx)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { lock.Release(context.Background()) })
	svc := scan.New(st, "", "", 5*time.Second, poolLogger())
	if _, e = svc.ScanScope(ctx, root, scope, 100); e != nil {
		t.Fatal(e)
	}
	w, e := st.GetWork(ctx, root, scope)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.ClaimWork(ctx, root, scope, w.Version, time.Now().Add(-6*time.Minute)); e != nil {
		t.Fatal(e)
	}
	a, e := st.GetAdapterConfig(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	r, e := st.GetRoot(ctx, st.Pool(), root)
	if e != nil {
		t.Fatal(e)
	}
	provenance, _ := json.Marshal(map[string]any{"mode": "scoped_refresh", "scope": scope, "directory_coverage": domain.DirectoryCoverage{Version: 2, Path: scope, Generation: r.CurrentGeneration, AdapterDigest: postgres.AdapterConfigDigest(a), EntryCount: 0, TopologyDigest: strings.Repeat("a", 64)}})
	fresh := domain.FreshRefreshed
	count := int64(0)
	id := reconcile.NewUUID()
	snap := domain.Snapshot{SnapshotID: id, RootID: root, Provenance: provenance, ObservedAt: time.Now().Add(-6 * time.Minute), TraversalStatus: domain.TraversalPartial, CompletenessFlag: domain.CompletenessFlagPartial, LifecycleState: domain.SnapshotDraft, FreshnessEvidence: &fresh, EntryCount: &count}
	if e = st.CreateDraftSnapshot(ctx, snap, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = st.SubmitAndAdmitSnapshot(ctx, root, id); e != nil {
		t.Fatal(e)
	}
	return st, h, root, scope, id
}

func TestIssue144N1FirstWakeAfterStaleHeadHandoff(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(fmt.Sprintf("worker_wins=%v", race), func(t *testing.T) {
			st, h, root, scope, old := issue144OldSubmitted(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var digest string
			if e := st.Pool().QueryRow(ctx, `SELECT md5(string_agg(row_to_json(j)::text,'|'ORDER BY root_id,event_seq)) FROM index_journal_event j`).Scan(&digest); e != nil {
				t.Fatal(e)
			}
			gate := &issue144HeadGate{paused: make(chan struct{}), release: make(chan struct{})}
			release := sync.OnceFunc(func() { close(gate.release) })
			defer release()
			cfgPool := st.Pool().Config()
			if race {
				cfgPool.ConnConfig.Tracer = gate
			}
			pool, e := pgxpool.NewWithConfig(ctx, cfgPool)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(pool.Close)
			traced := postgres.New(pool)
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			cfg := poolConfig(1)
			cfg.IncrementalWakeInterval = time.Second
			cycle, e := buildIncrementalCycle(traced, cfg, logger)
			if e != nil {
				t.Fatal(e)
			}
			tap := issue144Tap{inner: cycle, result: make(chan issue144CycleResult, 2)}
			rt, e := incrementalruntime.New(traced, tap, runtimeConfig(cfg), logger, nil)
			if e != nil {
				t.Fatal(e)
			}
			if n, e := rt.RecoverStartupInflight(ctx); e != nil || n != 1 {
				t.Fatal("actual startup recovery", n, e)
			}
			done := make(chan struct{})
			var runErr error
			go func() { runErr = rt.Run(ctx); close(done) }()
			t.Cleanup(func() {
				cancel()
				release()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("runtime not joined")
				}
			})
			if race {
				select {
				case <-gate.paused:
				case <-done:
					t.Fatalf("before head barrier: %v", runErr)
				case <-ctx.Done():
					t.Fatal("head barrier not reached")
				}
				policy, e := st.RootReconcileConfig(ctx, root)
				if e != nil {
					t.Fatal(e)
				}
				out, worked, e := postgres.NewCoordinator(st, policy).ProcessHead(ctx, root)
				if e != nil || !worked || out.Status != domain.AdmissionStaleInput || out.Mutated {
					t.Fatal("competing actor must reject old input without mutation", out, worked, e)
				}
				w, e := st.GetWork(ctx, root, scope)
				if e != nil || w.WorkState != state.WorkInFlight {
					t.Fatal("stale receipt falsely verified live claim", w, e)
				}
				release()
			}
			select {
			case result := <-tap.result:
				if result.err != nil {
					t.Fatalf("first-wake P6 stop=%s class=%v typed_cause=%v", result.result.Executor.StopReason, result.result.Executor.Last.FailureClass, result.err)
				}
			case <-ctx.Done():
				t.Fatal("first cycle did not complete")
			}
			cancel()
			<-done
			if runErr != nil {
				t.Fatal("first wake killed runtime", runErr)
			}
			w, e := st.GetWork(context.Background(), root, scope)
			if e != nil || w.WorkState != state.WorkVerified || w.AttemptCount != 2 || h.calls.Load() != 2 {
				t.Fatal("fresh observation not verified exactly once", w, h.calls.Load(), e)
			}
			var after, admission, lifecycle string
			if e = st.Pool().QueryRow(context.Background(), `SELECT md5(string_agg(row_to_json(j)::text,'|'ORDER BY root_id,event_seq)) FROM index_journal_event j`).Scan(&after); e != nil {
				t.Fatal(e)
			}
			if e = st.Pool().QueryRow(context.Background(), `SELECT a.status,s.lifecycle_state FROM index_admission a JOIN index_snapshot s ON s.snapshot_id=a.snapshot_id WHERE s.snapshot_id=$1`, old).Scan(&admission, &lifecycle); e != nil {
				t.Fatal(e)
			}
			if after != digest || admission != "STALE_INPUT" || lifecycle != "REJECTED" || strings.Contains(logs.String(), "incremental_runtime_fatal") {
				t.Fatal("old input mutated/replayed or runtime failed", after, digest, admission, lifecycle, logs.String())
			}
		})
	}
}
