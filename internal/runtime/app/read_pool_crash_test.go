package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// Separate OS actor; purpose guard runs again in the child. Never resets schema.
func TestIssue138PoolCrashChild(t *testing.T) {
	if os.Getenv("INDEXCORE_ISSUE138_POOL_CHILD") != "stage2" {
		t.Skip("OS-crash child only")
	}
	pool := testutil.Issue138Pool(t)
	st := postgres.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	lock, e := st.AcquireWriterLock(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Release(context.Background())
	cfg := poolConfig(4)
	cycle, e := buildIncrementalCycle(st, cfg, poolLogger())
	if e != nil {
		t.Fatal(e)
	}
	rt, e := incrementalruntime.New(st, cycle, runtimeConfig(cfg), poolLogger(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = rt.RecoverStartupInflight(ctx); e != nil {
		t.Fatal(e)
	}
	if e = rt.Run(ctx); e != nil {
		t.Fatal(e)
	}
}

func TestIssue138PoolOSCrashPreservesOrderedRecovery(t *testing.T) {
	st, h, _, roots := poolFixture(t, 1, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	guard, e := st.Pool().Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer guard.Release()
	if _, e = guard.Exec(ctx, `SELECT pg_advisory_lock(1380142)`); e != nil {
		t.Fatal(e)
	}
	release := sync.OnceFunc(func() { guard.Exec(context.Background(), `SELECT pg_advisory_unlock(1380142)`) })
	defer release()
	_, e = st.Pool().Exec(ctx, `CREATE FUNCTION issue138_pool_stage2_barrier() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(1380142); RETURN NEW; END$$; CREATE TRIGGER issue138_pool_stage2_barrier BEFORE INSERT ON index_generation FOR EACH ROW EXECUTE FUNCTION issue138_pool_stage2_barrier()`)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestIssue138PoolCrashChild$", "-test.v=true", "-test.timeout=50s")
	cmd.Env = append(os.Environ(), "INDEXCORE_ISSUE138_POOL_CHILD=stage2")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	var childErr error
	go func() { childErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("OS child not joined")
		}
	})
	for {
		var waiting bool
		if e = st.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%INSERT INTO index_generation%')`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		select {
		case <-done:
			t.Fatalf("child exited before Stage2 barrier: %v %s", childErr, output.String())
		case <-ctx.Done():
			t.Fatal("no real Stage2 wait", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
		if childErr == nil {
			t.Fatal("OS crash was not an abnormal exit")
		}
	case <-ctx.Done():
		t.Fatal("OS kill did not join")
	}
	release()
	if _, e = st.Pool().Exec(ctx, `DROP TRIGGER issue138_pool_stage2_barrier ON index_generation; DROP FUNCTION issue138_pool_stage2_barrier()`); e != nil {
		t.Fatal(e)
	}
	// New process ownership is available only after the crashed connection drained.
	lock, e := st.AcquireWriterLock(ctx)
	if e != nil {
		t.Fatal("writer lock leaked across OS crash", e)
	}
	defer lock.Release(context.Background())
	cfg := poolConfig(4)
	cycle, e := buildIncrementalCycle(st, cfg, poolLogger())
	if e != nil {
		t.Fatal(e)
	}
	rt, e := incrementalruntime.New(st, cycle, runtimeConfig(cfg), poolLogger(), nil)
	if e != nil {
		t.Fatal(e)
	}
	recovered, e := rt.RecoverStartupInflight(ctx)
	if e != nil || recovered != 8 {
		t.Fatal("crash claims not recovered", recovered, e)
	}
	var snapshot string
	if e = st.Pool().QueryRow(ctx, `SELECT snapshot_id::text FROM index_admission WHERE root_id=$1 AND status='PENDING'`, roots[0]).Scan(&snapshot); e != nil {
		t.Fatal(e)
	}
	policy, e := st.RootReconcileConfig(ctx, roots[0])
	if e != nil {
		t.Fatal(e)
	}
	out, e := postgres.NewCoordinator(st, policy).ProcessSnapshot(ctx, roots[0], snapshot)
	if e != nil || out.Status != domain.AdmissionStaleInput {
		t.Fatal("old epoch was accepted or blocked head", out, e)
	}
	var events int
	if e = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_journal_event`).Scan(&events); e != nil || events != 0 || h.calls.Load() != 8 {
		t.Fatal("crash partially committed or reread during recovery", events, h.calls.Load(), e)
	}
	// Existing Kernel stale-input semantics drained the old admitted slot. Fresh
	// claim epochs can now read again; inert old DRAFT facts are retained for audit.
	if _, e = cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle); e != nil {
		t.Fatal(e)
	}
	if e = st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_journal_event WHERE event_type=$1`, domain.EventResourceAdded).Scan(&events); e != nil || events != 8 || h.calls.Load() != 16 {
		t.Fatal("fresh recovery did not converge exactly once", events, h.calls.Load(), e)
	}
}
