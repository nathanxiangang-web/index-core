package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func TestIssue144StaleHeadReceiptRejectsCorruptEvidence(t *testing.T) {
	for _, name := range []string{"valid", "FAILED", "REJECTED", "PENDING", "wrong-snapshot", "wrong-root", "wrong-admission-snapshot", "wrong-result", "missing-result", "duplicate-result", "result-generation", "applied-generation", "nonempty-counts", "null-counts", "conflicts", "wrong-reason", "null-reason", "duplicate-admission", "applied-history", "produced-generation", "store-error"} {
		t.Run(name, func(t *testing.T) {
			st, _, root, _, old := issue144OldSubmitted(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			policy, e := st.RootReconcileConfig(ctx, root)
			if e != nil {
				t.Fatal(e)
			}
			gate := &issue144HeadGate{paused: make(chan struct{}), release: make(chan struct{})}
			release := sync.OnceFunc(func() { close(gate.release) })
			defer release()
			cfg := st.Pool().Config()
			cfg.ConnConfig.Tracer = gate
			pool, e := pgxpool.NewWithConfig(ctx, cfg)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(pool.Close)
			type receipt struct {
				out  postgres.ReconcileOutcome
				done bool
				err  error
			}
			result := make(chan receipt, 1)
			actorDone := make(chan struct{})
			go func() {
				defer close(actorDone)
				o, d, e := postgres.NewCoordinator(postgres.New(pool), policy).ProcessHead(ctx, root)
				result <- receipt{o, d, e}
			}()
			t.Cleanup(func() {
				cancel()
				release()
				select {
				case <-actorDone:
				case <-time.After(3 * time.Second):
					t.Error("head actor not joined")
				}
			})
			select {
			case <-gate.paused:
			case <-ctx.Done():
				t.Fatal("no selected-head barrier")
			}
			out, done, e := postgres.NewCoordinator(st, policy).ProcessHead(ctx, root)
			if e != nil || !done || out.Status != domain.AdmissionStaleInput {
				t.Fatal(out, done, e)
			}
			var seq int64
			var digest string
			if e = st.Pool().QueryRow(ctx, `SELECT admission_seq FROM index_admission WHERE snapshot_id=$1`, old).Scan(&seq); e != nil {
				t.Fatal(e)
			}
			if e = st.Pool().QueryRow(ctx, `SELECT md5(string_agg(row_to_json(j)::text,'|'ORDER BY root_id,event_seq))FROM index_journal_event j`).Scan(&digest); e != nil {
				t.Fatal(e)
			}
			var sql string
			switch name {
			case "valid":
			case "FAILED", "REJECTED", "PENDING":
				sql = `UPDATE index_admission SET status='` + name + `' WHERE snapshot_id=$1`
			case "wrong-snapshot":
				sql = `UPDATE index_snapshot SET lifecycle_state='RETIRED' WHERE snapshot_id=$1`
			case "wrong-root":
				sql = `WITH other AS(INSERT INTO index_root(root_id,scope_descriptor,lifecycle_state)VALUES(gen_random_uuid(),'{}','ACTIVE')RETURNING root_id)UPDATE index_snapshot SET root_id=(SELECT root_id FROM other)WHERE snapshot_id=$1`
			case "wrong-admission-snapshot":
				sql = `UPDATE index_admission SET snapshot_id=(SELECT produced_by_snapshot_id FROM index_generation WHERE generation_number=1 LIMIT 1) WHERE snapshot_id=$1`
			case "wrong-result":
				sql = `UPDATE index_reconcile_result SET outcome='FAILED'WHERE root_id=(SELECT root_id FROM index_snapshot WHERE snapshot_id=$1)AND admission_seq=(SELECT admission_seq FROM index_admission WHERE snapshot_id=$1)`
			case "missing-result":
				sql = `DELETE FROM index_reconcile_result WHERE admission_seq=(SELECT admission_seq FROM index_admission WHERE snapshot_id=$1)`
			case "duplicate-result":
				sql = `INSERT INTO index_reconcile_result SELECT gen_random_uuid(),root_id,admission_seq,generation_number,outcome,counts,conflicts,rejection_reason,created_at FROM index_reconcile_result WHERE admission_seq=(SELECT admission_seq FROM index_admission WHERE snapshot_id=$1)`
			case "result-generation":
				sql = `UPDATE index_reconcile_result SET generation_number=1 WHERE admission_seq=(SELECT admission_seq FROM index_admission WHERE snapshot_id=$1)`
			case "applied-generation":
				sql = `UPDATE index_admission SET applied_generation=1 WHERE snapshot_id=$1`
			case "nonempty-counts":
				sql = `UPDATE index_reconcile_result SET counts='{"resource_removed":1}'WHERE admission_seq=(SELECT admission_seq FROM index_admission WHERE snapshot_id=$1)`
			case "null-counts":
				sql = `UPDATE index_reconcile_result SET counts='null'WHERE admission_seq=(SELECT admission_seq FROM index_admission WHERE snapshot_id=$1)`
			case "conflicts":
				sql = `UPDATE index_reconcile_result SET conflicts='[]'WHERE admission_seq=(SELECT admission_seq FROM index_admission WHERE snapshot_id=$1)`
			case "wrong-reason":
				sql = `UPDATE index_reconcile_result SET rejection_reason='unknown'WHERE admission_seq=(SELECT admission_seq FROM index_admission WHERE snapshot_id=$1)`
			case "null-reason":
				sql = `UPDATE index_reconcile_result SET rejection_reason=NULL WHERE admission_seq=(SELECT admission_seq FROM index_admission WHERE snapshot_id=$1)`
			case "duplicate-admission":
				sql = `WITH n AS(UPDATE index_root SET latest_admission_seq=latest_admission_seq+1 WHERE root_id=(SELECT root_id FROM index_snapshot WHERE snapshot_id=$1)RETURNING root_id,latest_admission_seq)INSERT INTO index_admission(root_id,admission_seq,snapshot_id,status)SELECT root_id,latest_admission_seq,$1,'FAILED'FROM n`
			case "applied-history":
				sql = `INSERT INTO index_applied_snapshot(root_id,snapshot_identity_kind,snapshot_identity_namespace,snapshot_identity_version,snapshot_identity_value,snapshot_id,applied_generation,applied_admission_seq)SELECT root_id,'DETERMINISTIC_DIGEST','negative','1','negative',$1,1,admission_seq FROM index_admission WHERE snapshot_id=$1`
			case "produced-generation":
				sql = `INSERT INTO index_generation(root_id,generation_number,produced_by_snapshot_id,produced_by_admission_seq,summary)SELECT root_id,2,$1,admission_seq,'{}'FROM index_admission WHERE snapshot_id=$1`
			case "store-error":
				_, e = st.Pool().Exec(ctx, `DROP TABLE index_reconcile_result`)
			}
			if sql != "" {
				_, e = st.Pool().Exec(ctx, sql, old)
			}
			if e != nil {
				t.Fatal("negative fixture failed", e)
			}
			release()
			var got receipt
			select {
			case got = <-result:
			case <-ctx.Done():
				t.Fatal("head actor not joined")
			}
			if name == "valid" {
				if !errors.Is(got.err, postgres.ErrStaleHeadConsumed) || !got.done || got.out.Status != "" || got.out.Mutated {
					t.Fatal("negative progress was fabricated as success", got)
				}
			} else if got.err == nil || errors.Is(got.err, postgres.ErrStaleHeadConsumed) || got.out.Mutated {
				t.Fatal("invalid terminal state accepted", name, seq, got)
			}
			var after string
			if e = st.Pool().QueryRow(ctx, `SELECT md5(string_agg(row_to_json(j)::text,'|'ORDER BY root_id,event_seq))FROM index_journal_event j`).Scan(&after); e != nil || after != digest {
				t.Fatal("handoff modified Journal", e)
			}
			// ProcessSnapshot must retain its refusal: stale/rejected is never an
			// APPLIED/NOOP positive receipt or a route back to SUBMITTED.
			if name == "valid" {
				if _, e = postgres.NewCoordinator(st, policy).ProcessSnapshot(ctx, root, old); e == nil {
					t.Fatal("stale input replayed as success")
				}
			}
		})
	}
}

func TestIssue144UnknownStatusCannotBePersisted(t *testing.T) {
	st, _, _, _, old := issue144OldSubmitted(t)
	_, err := st.Pool().Exec(context.Background(), `UPDATE index_admission SET status='STALLED'WHERE snapshot_id=$1`, old)
	var sqlerr interface{ SQLState() string }
	if !errors.As(err, &sqlerr) || sqlerr.SQLState() != "23514" {
		t.Fatal("unknown schema status was accepted", err)
	}
	var status string
	if err = st.Pool().QueryRow(context.Background(), `SELECT status FROM index_admission WHERE snapshot_id=$1`, old).Scan(&status); err != nil || status != "PENDING" {
		t.Fatal("failed invalid-state write changed admission", status, err)
	}
}
