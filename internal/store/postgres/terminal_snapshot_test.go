package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalorch"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalruntime"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

type handoffFixture struct {
	st         *postgres.Store
	ctx        context.Context
	root, snap string
	seq        int64
	cfg        reconcile.Config
	out        postgres.ReconcileOutcome
}

func handoffSetup(t *testing.T, noop bool) handoffFixture {
	t.Helper()
	p := testutil.Issue130Pool(t)
	testutil.ResetSchema(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	if err := postgres.Migrate(ctx, p); err != nil {
		t.Fatal(err)
	}
	st := postgres.New(p)
	root := reconcile.NewUUID()
	if err := st.CreateRoot(ctx, p, root, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	cfg := reconcile.Config{RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour, MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}
	f := handoffFixture{st: st, ctx: ctx, root: root, cfg: cfg}
	f.snap, f.seq = handoffSubmit(t, f, "alive.txt")
	var err error
	f.out, err = postgres.NewCoordinator(st, cfg).ProcessSnapshot(ctx, root, f.snap)
	if err != nil {
		t.Fatal(err)
	}
	if noop {
		f.snap, f.seq = handoffSubmit(t, f, "alive.txt")
		f.out, err = postgres.NewCoordinator(st, cfg).ProcessSnapshot(ctx, root, f.snap)
		if err != nil {
			t.Fatal(err)
		}
		if f.out.Status != domain.AdmissionNoop {
			t.Fatal("expected NOOP")
		}
	}
	return f
}
func handoffSubmit(t *testing.T, f handoffFixture, name string) (string, int64) {
	t.Helper()
	id := reconcile.NewUUID()
	n := int64(1)
	size := int64(10)
	mt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hash, algo := "hash-"+name, "sha1"
	fresh, strong := domain.FreshDirect, domain.StrongFailureVisibility
	snap := domain.Snapshot{SnapshotID: id, RootID: f.root, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(), TraversalStatus: domain.TraversalPartial, SkippedScopesKnownEmpty: true, FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong, CompletenessFlag: domain.CompletenessFlagPartial, LifecycleState: domain.SnapshotDraft, EntryCount: &n}
	entries := []domain.SnapshotEntry{{EntryLocalID: "/" + name, Name: name, ParentRef: "/", Size: &size, Mtime: &mt, ContentHash: &hash, HashAlgorithm: &algo}}
	if err := f.st.CreateDraftSnapshot(f.ctx, snap, entries); err != nil {
		t.Fatal(err)
	}
	seq, err := f.st.SubmitAndAdmitSnapshot(f.ctx, f.root, id)
	if err != nil {
		t.Fatal(err)
	}
	return id, seq
}
func handoffCounts(t *testing.T, f handoffFixture) string {
	t.Helper()
	var out string
	err := f.st.Pool().QueryRow(f.ctx, `SELECT json_build_array((SELECT latest_admission_seq FROM index_root WHERE root_id=$1),(SELECT current_generation FROM index_root WHERE root_id=$1),(SELECT count(*) FROM index_admission),(SELECT count(*) FROM index_journal_event),(SELECT count(*) FROM index_identity_evidence_observation),(SELECT count(*) FROM index_applied_snapshot),(SELECT count(*) FROM index_reconcile_result),(SELECT count(*) FROM index_canonical_resource WHERE resource_presence='PRESENT'),(SELECT md5(string_agg(row_to_json(j)::text,'|' ORDER BY root_id,event_seq)) FROM index_journal_event j),(SELECT md5(string_agg(row_to_json(r)::text,'|' ORDER BY root_id,resource_id)) FROM index_canonical_resource r))::text`, f.root).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestIssue130TerminalReplay(t *testing.T) {
	for _, noop := range []bool{false, true} {
		t.Run(fmt.Sprint("noop=", noop), func(t *testing.T) {
			f := handoffSetup(t, noop)
			before := handoffCounts(t, f)
			// Fresh coordinator instances model a restarted caller, not another writer.
			for i := 0; i < 3; i++ {
				got, err := postgres.NewCoordinator(f.st, f.cfg).ProcessSnapshot(f.ctx, f.root, f.snap)
				if err != nil || !reflect.DeepEqual(got, f.out) {
					t.Fatalf("receipt mismatch: %+v want %+v err %v", got, f.out, err)
				}
			}
			if after := handoffCounts(t, f); after != before {
				t.Fatalf("replay wrote state: %s != %s", after, before)
			}
			// A later input may advance this root; old exact receipt must not borrow it.
			later, _ := handoffSubmit(t, f, "later.txt")
			pendingBefore := handoffCounts(t, f)
			if got, err := postgres.NewCoordinator(f.st, f.cfg).ProcessSnapshot(f.ctx, f.root, f.snap); err != nil || got != f.out || handoffCounts(t, f) != pendingBefore {
				t.Fatal("historical receipt borrowed or displaced the newer pending head")
			}
			if _, err := postgres.NewCoordinator(f.st, f.cfg).ProcessSnapshot(f.ctx, f.root, later); err != nil {
				t.Fatal(err)
			}
			before = handoffCounts(t, f)
			got, err := postgres.NewCoordinator(f.st, f.cfg).ProcessSnapshot(f.ctx, f.root, f.snap)
			if err != nil || got != f.out || handoffCounts(t, f) != before {
				t.Fatalf("later input corrupted historical receipt: %+v %v", got, err)
			}
		})
	}
}

func TestIssue130TerminalEvidenceFailClosed(t *testing.T) {
	cases := map[string]func(*testing.T, handoffFixture){
		"missing admission": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `DELETE FROM index_admission WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"duplicate admission": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `INSERT INTO index_admission(root_id,admission_seq,snapshot_id,status,applied_generation) SELECT root_id,admission_seq+10,snapshot_id,status,applied_generation FROM index_admission WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"wrong snapshot binding": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_admission SET snapshot_id=$3 WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq, reconcile.NewUUID())
		},
		"wrong admission root": func(t *testing.T, f handoffFixture) {
			other := reconcile.NewUUID()
			if err := f.st.CreateRoot(f.ctx, f.st.Pool(), other, []byte(`{}`), domain.RootActive); err != nil {
				t.Fatal(err)
			}
			execHandoff(t, f, `UPDATE index_admission SET root_id=$3 WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq, other)
		},
		"wrong result root": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_reconcile_result SET root_id=$3 WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq, reconcile.NewUUID())
		},
		"wrong result seq": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_reconcile_result SET admission_seq=admission_seq+10 WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"missing result": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `DELETE FROM index_reconcile_result WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"duplicate result": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `INSERT INTO index_reconcile_result(reconcile_id,root_id,admission_seq,generation_number,outcome) SELECT gen_random_uuid(),root_id,admission_seq,generation_number,outcome FROM index_reconcile_result WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"result generation": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_reconcile_result SET generation_number=generation_number+1 WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"missing result generation": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_reconcile_result SET generation_number=NULL WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"future admission generation": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_admission SET applied_generation=99 WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"negative admission generation": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_admission SET applied_generation=-1 WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"missing admission generation": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_admission SET applied_generation=NULL WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"missing generation provenance": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `DELETE FROM index_generation WHERE root_id=$1`, f.root)
		},
		"wrong generation provenance": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_generation SET produced_by_admission_seq=99 WHERE root_id=$1`, f.root)
		},
		"missing applied history": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `DELETE FROM index_applied_snapshot WHERE root_id=$1`, f.root)
		},
		"wrong applied history": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_applied_snapshot SET applied_admission_seq=99 WHERE root_id=$1`, f.root)
		},
		"wrong outcome": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_reconcile_result SET outcome='NOOP' WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"reason on success": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_reconcile_result SET rejection_reason='synthetic rejection' WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
		},
		"null acceptance": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_snapshot SET acceptance_state=NULL WHERE snapshot_id=$1`, f.snap)
		},
		"failed acceptance": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_snapshot SET acceptance_state='FAILED' WHERE snapshot_id=$1`, f.snap)
		},
		"submitted with terminal admission": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_snapshot SET lifecycle_state='SUBMITTED' WHERE snapshot_id=$1`, f.snap)
		},
		"rejected snapshot": func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_snapshot SET lifecycle_state='REJECTED' WHERE snapshot_id=$1`, f.snap)
		},
	}
	for _, state := range []string{"REJECTED", "STALE_INPUT", "FAILED", "PENDING"} {
		s := state
		cases["admission "+s] = func(t *testing.T, f handoffFixture) {
			execHandoff(t, f, `UPDATE index_admission SET status=$3 WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq, s)
		}
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := handoffSetup(t, false)
			mutate(t, f)
			before := handoffCounts(t, f)
			if _, err := postgres.NewCoordinator(f.st, f.cfg).ProcessSnapshot(f.ctx, f.root, f.snap); err == nil {
				t.Fatal("invalid evidence accepted")
			}
			if handoffCounts(t, f) != before {
				t.Fatal("invalid replay changed state")
			}
		})
	}
}
func execHandoff(t *testing.T, f handoffFixture, q string, args ...any) {
	t.Helper()
	if _, err := f.st.Pool().Exec(f.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestIssue130WrongRootAndMissingSnapshot(t *testing.T) {
	f := handoffSetup(t, false)
	other := reconcile.NewUUID()
	if err := f.st.CreateRoot(f.ctx, f.st.Pool(), other, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	before := handoffCounts(t, f)
	for _, ids := range [][2]string{{other, f.snap}, {f.root, reconcile.NewUUID()}} {
		if _, err := postgres.NewCoordinator(f.st, f.cfg).ProcessSnapshot(f.ctx, ids[0], ids[1]); err == nil {
			t.Fatal("wrong identity accepted")
		}
	}
	if handoffCounts(t, f) != before {
		t.Fatal("wrong identity wrote state")
	}
}

func TestIssue130FIFOOrdering(t *testing.T) {
	f := handoffSetup(t, false)
	older, _ := handoffSubmit(t, f, "older.txt")
	later, _ := handoffSubmit(t, f, "later.txt")
	c := postgres.NewCoordinator(f.st, f.cfg)
	if _, err := c.ProcessSnapshot(f.ctx, f.root, later); err != postgres.ErrNotHead {
		t.Fatalf("FIFO bypassed: %v", err)
	}
	if _, err := c.ProcessSnapshot(f.ctx, f.root, older); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ProcessSnapshot(f.ctx, f.root, later); err != nil {
		t.Fatal(err)
	}
}

func TestIssue130UncommittedReceiptAndBackendCrash(t *testing.T) {
	f := handoffSetup(t, false)
	snap, seq := handoffSubmit(t, f, "crash.txt")
	tx, err := f.st.Pool().Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var pid uint32
	pid = tx.Conn().PgConn().PID()
	if _, err = tx.Exec(f.ctx, `SELECT 1 FROM index_root WHERE root_id=$1 FOR UPDATE`, f.root); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, `UPDATE index_snapshot SET lifecycle_state='RECONCILED' WHERE snapshot_id=$1`, snap); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, `UPDATE index_admission SET status='APPLIED',applied_generation=99 WHERE root_id=$1 AND admission_seq=$2`, f.root, seq); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, `INSERT INTO index_reconcile_result(reconcile_id,root_id,admission_seq,generation_number,outcome) VALUES(gen_random_uuid(),$1,$2,99,'RECONCILED')`, f.root, seq); err != nil {
		t.Fatal(err)
	}
	reader := testutil.Issue130Pool(t, func(c *pgxpool.Config) { c.ConnConfig.RuntimeParams["application_name"] = "issue130-blocked-reader" })
	done := make(chan struct {
		out postgres.ReconcileOutcome
		err error
	}, 1)
	go func() {
		out, e := postgres.NewCoordinator(postgres.New(reader), f.cfg).ProcessSnapshot(f.ctx, f.root, snap)
		done <- struct {
			out postgres.ReconcileOutcome
			err error
		}{out, e}
	}()
	for {
		var waiting bool
		if err = f.st.Pool().QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name='issue130-blocked-reader' AND wait_event_type='Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("reader returned before writer transaction ended: %+v", result)
		case <-f.ctx.Done():
			t.Fatal("reader never waited on root")
		case <-time.After(time.Millisecond):
		}
	}
	// Only this owned disposable PG backend is killed. Its uncommitted fake
	// terminal result must roll back; this is a real backend crash, not a fresh pool.
	var killed bool
	if err = f.st.Pool().QueryRow(f.ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&killed); err != nil || !killed {
		t.Fatalf("backend crash failed: %v", err)
	}
	result := <-done
	if result.err != nil || result.out.AppliedGeneration == 99 || result.out.Status != domain.AdmissionApplied {
		t.Fatalf("uncertain result accepted: %+v", result)
	}
	before := handoffCounts(t, f)
	if _, err = postgres.NewCoordinator(postgres.New(reader), f.cfg).ProcessSnapshot(f.ctx, f.root, snap); err != nil {
		t.Fatal(err)
	}
	if handoffCounts(t, f) != before {
		t.Fatal("restart replay duplicated history")
	}
}

func TestIssue130QueryAndCASFaultsStayErrors(t *testing.T) {
	t.Run("query failure", func(t *testing.T) {
		f := handoffSetup(t, false)
		execHandoff(t, f, `ALTER TABLE index_reconcile_result RENAME TO hidden_result`)
		_, err := postgres.NewCoordinator(f.st, f.cfg).ProcessSnapshot(f.ctx, f.root, f.snap)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "42P01" {
			t.Fatalf("query error hidden: %v", err)
		}
	})
	t.Run("generation CAS/store fault", func(t *testing.T) {
		f := handoffSetup(t, false)
		snap, _ := handoffSubmit(t, f, "fault.txt")
		execHandoff(t, f, `CREATE FUNCTION issue130_cas_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.current_generation<>OLD.current_generation THEN RAISE EXCEPTION 'synthetic CAS fault' USING ERRCODE='40001'; END IF; RETURN NEW; END $$; CREATE TRIGGER issue130_cas_fault BEFORE UPDATE ON index_root FOR EACH ROW EXECUTE FUNCTION issue130_cas_fault()`)
		before := handoffCounts(t, f)
		_, err := postgres.NewCoordinator(f.st, f.cfg).ProcessSnapshot(f.ctx, f.root, snap)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "40001" {
			t.Fatalf("CAS/store failure hidden: %v", err)
		}
		if handoffCounts(t, f) != before {
			t.Fatal("failed transaction partially committed")
		}
	})
	t.Run("closed pool", func(t *testing.T) {
		f := handoffSetup(t, false)
		f.st.Pool().Close()
		if _, err := postgres.NewCoordinator(f.st, f.cfg).ProcessSnapshot(f.ctx, f.root, f.snap); err == nil {
			t.Fatal("closed database accepted")
		}
	})
}

func TestIssue130NoopReceiptNegatives(t *testing.T) {
	for _, which := range []string{"missing IO3 anchor", "NOOP owns generation", "NOOP owns application", "wrong outcome"} {
		t.Run(which, func(t *testing.T) {
			f := handoffSetup(t, true)
			switch which {
			case "missing IO3 anchor":
				execHandoff(t, f, `DELETE FROM index_applied_snapshot WHERE root_id=$1`, f.root)
			case "NOOP owns generation":
				execHandoff(t, f, `UPDATE index_generation SET produced_by_snapshot_id=$2,produced_by_admission_seq=$3 WHERE root_id=$1`, f.root, f.snap, f.seq)
			case "NOOP owns application":
				execHandoff(t, f, `UPDATE index_applied_snapshot SET snapshot_id=$2,applied_admission_seq=$3 WHERE root_id=$1`, f.root, f.snap, f.seq)
			case "wrong outcome":
				execHandoff(t, f, `UPDATE index_reconcile_result SET outcome='RECONCILED' WHERE root_id=$1 AND admission_seq=$2`, f.root, f.seq)
			}
			before := handoffCounts(t, f)
			if _, err := postgres.NewCoordinator(f.st, f.cfg).ProcessSnapshot(f.ctx, f.root, f.snap); err == nil {
				t.Fatal("invalid NOOP accepted")
			}
			if handoffCounts(t, f) != before {
				t.Fatal("invalid NOOP wrote state")
			}
		})
	}
}

type handoffInternalScanner struct{}

func (handoffInternalScanner) ScanScope(context.Context, string, string, int) (scan.Result, error) {
	return scan.Result{}, scan.NewScopeError(scan.ScopeFailureInternal, errors.New("synthetic unrelated failure"))
}

func TestIssue130UnrelatedInternalStillFatal(t *testing.T) {
	f := handoffSetup(t, false)
	before := handoffCounts(t, f)
	if _, err := f.st.MergeSignal(f.ctx, state.DirtySignal{RootID: f.root, ScopeKey: "/", Source: state.SourceManualOperator, Reason: state.ReasonManualVerify, Priority: state.PriorityNormal, SeenAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	one, err := incrementalexec.New(f.st, handoffInternalScanner{}, incrementalexec.Config{MaxEntriesPerScope: 10, Retry: incrementalexec.RetryPolicy{TransientProvider: time.Minute, Throttled: time.Minute, Internal: time.Minute}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := incrementalexec.NewCycleRunner(one)
	if err != nil {
		t.Fatal(err)
	}
	orch, err := incrementalorch.NewRunner(f.st, cycle, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := incrementalruntime.New(f.st, orch, incrementalruntime.Config{WakeInterval: time.Second, MaxRetryPromotions: 1, Cycle: incrementalruntime.DefaultCycleConfig()}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = rt.Run(f.ctx); !errors.Is(err, incrementalexec.ErrScannerFailed) {
		t.Fatalf("unrelated INTERNAL swallowed: %v", err)
	}
	var ws, ec string
	if err = f.st.Pool().QueryRow(f.ctx, `SELECT work_state,last_error_class FROM index_dirty_scope_work WHERE root_id=$1`, f.root).Scan(&ws, &ec); err != nil {
		t.Fatal(err)
	}
	if ws != "RETRY_WAIT" || ec != "INTERNAL" || handoffCounts(t, f) != before {
		t.Fatal("unrelated failure changed canonical truth or was not durable")
	}
}
