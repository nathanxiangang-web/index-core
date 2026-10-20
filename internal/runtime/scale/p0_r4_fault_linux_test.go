package scale_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/internal/diagnostic"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

type r4FenceTracer struct {
	stage   string
	armed   bool
	reached bool
	marker  string
}

func (f *r4FenceTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (f *r4FenceTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	// Query end has no SQL, so the start callback below carries it via a context.
}

type r4TraceKey struct{}
type r4SQLFence struct{ *r4FenceTracer }

func (f *r4SQLFence) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, r4TraceKey{}, strings.ToLower(strings.TrimSpace(data.SQL)))
}
func (f *r4SQLFence) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if !f.armed || f.reached || data.Err != nil {
		return
	}
	sql, _ := ctx.Value(r4TraceKey{}).(string)
	if (f.stage == "during-reconcile" && strings.HasPrefix(sql, "insert into index_journal_event")) || (f.stage == "after-commit" && sql == "commit") {
		f.reached = true
		r4Park(f.marker)
	}
}
func r4Park(marker string) { fmt.Println(marker); _, _ = bufio.NewReader(os.Stdin).ReadString('\n') }
func r4FaultSeed(t *testing.T) *r4Lab {
	h := r4New(t)
	h.add("/keep.bin", false)
	h.add("/gone.bin", false)
	h.add("/other.bin", false)
	h.apply(h.entries(""), "", false, "fault-seed")
	h.verify("fault-seed")
	delete(h.source, "/gone.bin")
	h.apply(h.entries(""), "", false, "fault-first-missing")
	h.apply(h.entries(""), "", false, "fault-independent-confirmation")
	h.verify("retained-tombstone")
	return h
}
func r4FaultInput() []domain.SnapshotEntry {
	h := &r4Lab{source: map[string]domain.SnapshotEntry{}}
	h.add("/keep.bin", false)
	h.add("/other.bin", false)
	h.add("/new.bin", false)
	out := []domain.SnapshotEntry{}
	for _, p := range []string{"/keep.bin", "/new.bin", "/other.bin"} {
		out = append(out, h.source[p])
	}
	return out
}

// This helper never resets schema and only runs inside the parent's verified
// disposable instance. Its process is the exact owned subprocess later killed.
func TestP0R4CrashChild(t *testing.T) {
	if os.Getenv("INDEXCORE_R4_CHILD") != "1" {
		t.Log("HELPER_INACTIVE: not a crash result")
		return
	}
	stage := os.Getenv("INDEXCORE_R4_STAGE")
	ctx := context.Background()
	proof := r4Pool(t)
	cfg := proof.Config().Copy()
	f := &r4SQLFence{&r4FenceTracer{stage: stage, marker: "R4_PROCESS_FENCE"}}
	cfg.ConnConfig.Tracer = f
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("child pool")
	}
	defer pool.Close()
	st := postgres.New(pool)
	lock, err := st.AcquireWriterLock(ctx)
	if err != nil {
		t.Fatal("child writer lock")
	}
	defer lock.Release(ctx)
	if stage == "inflight" {
		sig := state.DirtySignal{RootID: r4Root, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify, Priority: state.PriorityNormal, SeenAt: time.Now().UTC()}
		w, err := st.MergeSignal(ctx, sig)
		if err != nil {
			t.Fatal("child signal")
		}
		if _, err = st.ClaimWork(ctx, r4Root, "/", w.Version, time.Now().UTC()); err != nil {
			t.Fatal("child claim")
		}
		r4Park("R4_PROCESS_FENCE")
		t.Fatal("parent failed to kill inflight child")
	}
	h := &r4Lab{t: t, ctx: ctx, st: st}
	entries := r4FaultInput()
	s := h.draft(entries, "", false)
	s.SnapshotID = os.Getenv("INDEXCORE_R4_SNAPSHOT")
	if stage == "before-draft" {
		r4Park("R4_PROCESS_FENCE")
	}
	if err = st.CreateDraftSnapshot(ctx, s, entries); err != nil {
		t.Fatal("child draft")
	}
	if stage == "after-draft" {
		r4Park("R4_PROCESS_FENCE")
	}
	if stage == "legacy-submitted" {
		if err = st.MarkSnapshotSubmitted(ctx, pool, s.SnapshotID); err != nil {
			t.Fatal("legacy submit")
		}
		r4Park("R4_PROCESS_FENCE")
	}
	if _, err = st.SubmitAndAdmitSnapshot(ctx, r4Root, s.SnapshotID); err != nil {
		t.Fatal("child admission")
	}
	if stage == "admitted" {
		r4Park("R4_PROCESS_FENCE")
	}
	f.armed = true
	if _, _, err = postgres.NewCoordinator(st, (postgres.RootPolicy{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}).ReconcileConfig()).ProcessHead(ctx, r4Root); err != nil {
		t.Fatal("child reconcile")
	}
	t.Fatal("requested crash fence was never reached")
}

func TestP0R4ProcessCrashes(t *testing.T) {
	if os.Getenv("INDEXCORE_R4_PROFILE") != "fault" {
		t.Skip("NOT_RUN: real SIGKILL probes require owned isolated fault runner")
	}
	for _, stage := range []string{"before-draft", "after-draft", "legacy-submitted", "admitted", "during-reconcile", "after-commit", "inflight"} {
		t.Run(stage, func(t *testing.T) {
			h := r4FaultSeed(t)
			before := h.verify("before-crash")
			tail, _ := h.st.MaxEventSeq(h.ctx, h.st.Pool(), r4Root)
			snap := reconcile.NewUUID()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestP0R4CrashChild$", "-test.v")
			cmd.Env = append(os.Environ(), "INDEXCORE_R4_CHILD=1", "INDEXCORE_R4_STAGE="+stage, "INDEXCORE_R4_SNAPSHOT="+snap)
			// Keep stdin open while the child is parked. A nil exec.Cmd.Stdin
			// is /dev/null, which would make the fence read EOF and race ahead.
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = cmd.Stdout
			if err = cmd.Start(); err != nil {
				t.Fatal("start owned crash child")
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			fenced := make(chan bool, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if scanner.Text() == "R4_PROCESS_FENCE" {
						fenced <- true
						return
					}
				}
				fenced <- false
			}()
			select {
			case ok := <-fenced:
				if !ok {
					t.Fatal("child exited before deterministic fence")
				}
			case <-time.After(20 * time.Second):
				t.Fatal("child fence timed out")
			}
			// Observe stable truth while the child is still alive, including the
			// open transaction case: uncommitted Canonical/Journal remain invisible.
			if stage == "after-commit" {
				h.add("/new.bin", false)
			}
			h.verify("at-fence")
			started := time.Now()
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal("owned SIGKILL failed")
			}
			err = cmd.Wait()
			if err == nil {
				t.Fatal("crash child exited normally")
			}
			ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
				t.Fatal("not an actual SIGKILL")
			}
			lock, err := h.st.AcquireWriterLock(h.ctx)
			deadline := time.Now().Add(5 * time.Second)
			for err == postgres.ErrWriterLockHeld && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
				lock, err = h.st.AcquireWriterLock(h.ctx)
			}
			if err != nil {
				t.Fatal("dead child writer lock not released")
			}
			defer lock.Release(h.ctx)
			if stage == "inflight" {
				rt, err := incrementalruntime.New(h.st, r4NeverCycle{}, incrementalruntime.Config{WakeInterval: time.Second, MaxRetryPromotions: 1, Cycle: incrementalruntime.DefaultCycleConfig()}, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				n, err := rt.RecoverStartupInflight(h.ctx)
				if err != nil || n != 1 {
					t.Fatal("inflight startup recovery failed")
				}
				w, err := h.st.GetWork(h.ctx, r4Root, "/")
				if err != nil || w.WorkState != state.WorkPending || w.AttemptCount != 1 || w.ConsecutiveFailures != 0 || w.LastErrorClass != nil || w.ClaimedSignalSeq != nil {
					t.Fatal("crash must preserve history without inventing a provider failure")
				}
				if n, err = rt.RecoverStartupInflight(h.ctx); err != nil || n != 0 {
					t.Fatal("recovery duplicated")
				}
			} else {
				if stage == "before-draft" {
					s := h.draft(r4FaultInput(), "", false)
					s.SnapshotID = snap
					if err = h.st.CreateDraftSnapshot(h.ctx, s, r4FaultInput()); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "before-draft" || stage == "after-draft" {
					n, amb, err := h.st.ResolveUnadmittedSubmittedForRoot(h.ctx, r4Root)
					if err != nil || n != 0 || amb {
						t.Fatal("DRAFT auto-admitted")
					}
					if _, err = h.st.SubmitAndAdmitSnapshot(h.ctx, r4Root, snap); err != nil {
						t.Fatal(err)
					} // Explicit manual submission, not automatic DRAFT recovery.
				}
				if stage == "legacy-submitted" {
					n, amb, err := h.st.ResolveUnadmittedSubmittedForRoot(h.ctx, r4Root)
					if err != nil || n != 1 || amb {
						t.Fatal("single legacy candidate not recovered")
					}
				}
				_, done, err := h.coord.ProcessHead(h.ctx, r4Root)
				if err != nil || (stage != "after-commit" && !done) || (stage == "after-commit" && done) {
					t.Fatal("pending/committed recovery mismatch")
				}
				h.add("/new.bin", false)
				for i := 0; i < 3; i++ {
					if _, done, err = h.coord.ProcessHead(h.ctx, r4Root); err != nil || done {
						t.Fatal("duplicate post-ack replay")
					}
				}
			}
			after := h.verify("recovered")
			h.assertSurvivors(before, after)
			end, _ := h.st.MaxEventSeq(h.ctx, h.st.Pool(), r4Root)
			want := tail + 1
			if stage == "inflight" {
				want = tail
			}
			if end != want {
				t.Fatal("unexpected/duplicate terminal journal event")
			}
			t.Logf("R4_CRASH stage=%s signal=SIGKILL owned_pid=%d recovery_ms=%.3f old_Q8=%d new_Q8=%d mismatches=0", stage, cmd.Process.Pid, float64(time.Since(started).Microseconds())/1000, tail, end)
		})
	}
}

type r4NeverCycle struct{}

func (r4NeverCycle) RunCycle(context.Context, incrementalorch.Config) (incrementalorch.Result, error) {
	panic("R4 startup recovery must not run an orchestration cycle")
}

func TestP0R4DatabaseRestart(t *testing.T) {
	if os.Getenv("INDEXCORE_R4_PROFILE") != "fault" {
		t.Skip("NOT_RUN: DB stop/restart requires owned isolated orchestrator")
	}
	h := r4FaultSeed(t)
	before := h.verify("before-db-stop")
	tail, _ := h.st.MaxEventSeq(h.ctx, h.st.Pool(), r4Root)
	entries := r4FaultInput()
	s := h.draft(entries, "", false)
	if err := h.st.CreateDraftSnapshot(h.ctx, s, entries); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SubmitAndAdmitSnapshot(h.ctx, r4Root, s.SnapshotID); err != nil {
		t.Fatal(err)
	}
	cfg := h.st.Pool().Config().Copy()
	f := &r4SQLFence{&r4FenceTracer{stage: "during-reconcile", armed: true, marker: "R4_DB_STOP_READY"}}
	cfg.ConnConfig.Tracer = f
	pool, err := pgxpool.NewWithConfig(h.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := postgres.New(pool)
	coord := postgres.NewCoordinator(st, (postgres.RootPolicy{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}).ReconcileConfig())
	_, _, err = coord.ProcessHead(h.ctx, r4Root) // Python kills ONLY the attested test DB at the traced INSERT fence, then releases stdin.
	if !f.reached || err == nil {
		t.Fatal("DB interruption did not abort open reconcile")
	}
	o := diagnostic.Defaults()
	o.Timeout = 250 * time.Millisecond
	r, readErr := postgres.NewDiagnosticReader(pool).Read(h.ctx, r4Root, o)
	if readErr == nil || r.Complete || r.Overall != "DEGRADED" {
		t.Fatal("database-down diagnostic false green")
	}
	pool.Close()
	r4Park("R4_DB_RESTART_READY") // Orchestrator restarts the SAME container/data and waits for pg_isready.
	started := time.Now()
	fresh := r4Pool(t)
	h.st = postgres.New(fresh)
	h.qr = postgres.NewQueryReader(fresh)
	h.coord = postgres.NewCoordinator(h.st, (postgres.RootPolicy{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}).ReconcileConfig())
	h.verify("after-db-restart-before-replay")
	lock, err := h.st.AcquireWriterLock(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release(h.ctx)
	if _, done, err := h.coord.ProcessHead(h.ctx, r4Root); err != nil || !done {
		t.Fatal("durable pending input not recovered")
	}
	h.add("/new.bin", false)
	after := h.verify("after-db-recovery")
	h.assertSurvivors(before, after)
	for i := 0; i < 3; i++ {
		if _, done, err := h.coord.ProcessHead(h.ctx, r4Root); err != nil || done {
			t.Fatal("DB replay duplicated")
		}
	}
	end, _ := h.st.MaxEventSeq(h.ctx, fresh, r4Root)
	if end != tail+1 {
		t.Fatal("DB recovery journal mismatch")
	}
	t.Logf("R4_DB_RESTART recovery_after_ready_ms=%.3f old_Q8=%d new_Q8=%d mismatches=0", float64(time.Since(started).Microseconds())/1000, tail, end)
}
