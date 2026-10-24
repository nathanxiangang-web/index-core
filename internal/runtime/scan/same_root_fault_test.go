package scan_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func s138Roster(t *testing.T, st *postgres.Store, checkpoint string) []string {
	t.Helper()
	snap, err := st.GetSnapshot(context.Background(), st.Pool(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Fence postgres.ScopedFetchFence `json:"scoped_fetch_fence"`
	}
	if err := json.Unmarshal(snap.Provenance, &meta); err != nil || meta.Fence.Lineage == nil {
		t.Fatal(err)
	}
	return meta.Fence.Lineage.Snapshots
}

func TestIssue138SameRootNonMutatingAndNoopPrefix(t *testing.T) {
	st, m, root, p, claims, _ := s138Setup(t, 2, 2, true)
	for i, c := range claims {
		m.set(c.ScopeKey, fileEntry("late.bin", int64(100+i), fmt.Sprintf("hash-%d", i)))
		if _, err := p4ScanService(st).ScanScope(context.Background(), root, c.ScopeKey, 1000); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := st.GetRoot(context.Background(), st.Pool(), root)
	journal := f138Count(t, st, "index_journal_event")
	for round := 0; round < 2; round++ {
		if round > 0 {
			for i, c := range claims {
				claims[i] = f138Claim(t, st, root, c.ScopeKey)
			}
		}
		b, err := p.FetchBatch(context.Background(), claims)
		if err != nil {
			t.Fatal(err)
		}
		for rank, c := range claims {
			res, err := p.Commit(context.Background(), b, rank)
			if err != nil {
				b.Close()
				t.Fatal(err)
			}
			if res.Outcome.Mutated || res.Outcome.AppliedGeneration != before.CurrentGeneration {
				b.Close()
				t.Fatal("invented generation for non-mutating prefix", res)
			}
			if round == 1 && res.Outcome.Status != domain.AdmissionNoop {
				b.Close()
				t.Fatal("expected exact NOOP receipt", res)
			}
			s138Complete(t, st, c)
		}
		b.Close()
	}
	if f138Count(t, st, "index_journal_event") != journal {
		t.Fatal("NOOP prefix duplicated Journal")
	}
}

func TestIssue138SameRootPeerFailureLeavesNoDraft(t *testing.T) {
	for _, failure := range []int64{500, -1, 0} {
		t.Run(fmt.Sprintf("mode=%d", failure), func(t *testing.T) {
			st, _, _, _, claims, src := s138Setup(t, 2, 2, true)
			p, err := scan.NewSameRootPrototype(scan.New(st, "", "", 200*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil))), 2)
			if err != nil {
				t.Fatal(err)
			}
			if failure == 0 {
				src.delay.Store(int64(time.Second))
			} else {
				src.code.Store(failure)
			}
			before := f138Count(t, st, "index_snapshot")
			if _, err := p.FetchBatch(context.Background(), claims); err == nil {
				t.Fatal("failed peer froze input")
			}
			deadline := time.Now().Add(3 * time.Second)
			for src.active.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if f138Count(t, st, "index_snapshot") != before || src.active.Load() != 0 {
				t.Fatal("partial input or undrained peer", src.active.Load())
			}
		})
	}
}

func TestIssue138SameRootCompetingBatchCannotConsumeClaimTwice(t *testing.T) {
	st, _, _, p, claims, _ := s138Setup(t, 2, 2, true)
	b, err := p.FetchBatch(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	other, err := scan.NewSameRootPrototype(p4ScanService(st), 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Commit(context.Background(), b, 0); err != nil {
		t.Fatal(err)
	}
	duplicate, err := other.FetchBatch(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Close()
	before := f138Count(t, st, "index_journal_event")
	admissions := f138Count(t, st, "index_admission")
	if _, err := other.Commit(context.Background(), duplicate, 0); !errors.Is(err, postgres.ErrScopedFetchStale) {
		t.Fatal("claim consumed twice", err)
	}
	if f138Count(t, st, "index_journal_event") != before {
		t.Fatal("duplicate input wrote Q8")
	}
	if f138Count(t, st, "index_admission") != admissions {
		t.Fatal("duplicate claim allocated a new admission")
	}
}

func TestIssue138SameRootResumeUnacknowledgedCommit(t *testing.T) {
	st, _, _, p, claims, src := s138Setup(t, 2, 2, true)
	b, err := p.FetchBatch(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := b.Checkpoint()
	first, err := p.Commit(context.Background(), b, 0)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	before := f138Count(t, st, "index_journal_event")
	restarted, err := scan.NewSameRootPrototype(p4ScanService(st), 2)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := restarted.ResumeBatch(context.Background(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	replay, err := restarted.Commit(context.Background(), resumed, 0)
	if err != nil || replay.SnapshotID != first.SnapshotID || replay.Outcome != first.Outcome {
		t.Fatal("lost original unacknowledged receipt", replay, first, err)
	}
	if f138Count(t, st, "index_journal_event") != before {
		t.Fatal("terminal receipt reapplied")
	}
	s138Complete(t, st, claims[0])
	if _, err := restarted.Commit(context.Background(), resumed, 1); err != nil {
		t.Fatal(err)
	}
	s138Complete(t, st, claims[1])
	if src.calls.Load() != 2 {
		t.Fatal("terminal recovery called provider")
	}
}

func TestIssue138SameRootStoreRejectsCallbackSelectedOrder(t *testing.T) {
	st, _, _, p, claims, _ := s138Setup(t, 2, 2, true)
	b, err := p.FetchBatch(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	original := s138Roster(t, st, b.Checkpoint())
	ids := []string{reconcile.NewUUID(), reconcile.NewUUID()}
	inputs := make([]postgres.FrozenScopeInput, 2)
	for i := range inputs {
		snap, err := st.GetSnapshot(context.Background(), st.Pool(), original[1-i])
		if err != nil {
			t.Fatal(err)
		}
		entries, err := st.ListSnapshotEntries(context.Background(), st.Pool(), snap.SnapshotID)
		if err != nil {
			t.Fatal(err)
		}
		var meta map[string]json.RawMessage
		json.Unmarshal(snap.Provenance, &meta)
		var f postgres.ScopedFetchFence
		json.Unmarshal(meta["scoped_fetch_fence"], &f)
		f.Lineage.Snapshots = ids
		f.Lineage.Rank = i
		meta["scoped_fetch_fence"], _ = json.Marshal(f)
		snap.Provenance, _ = json.Marshal(meta)
		snap.SnapshotID = ids[i]
		inputs[i] = postgres.FrozenScopeInput{Snapshot: snap, Entries: entries}
	}
	before := f138Count(t, st, "index_snapshot")
	if err := st.FreezeScopedReadBatch(context.Background(), inputs); !errors.Is(err, postgres.ErrScopedFetchStale) {
		t.Fatal("Store trusted callback ordering", err)
	}
	if f138Count(t, st, "index_snapshot") != before {
		t.Fatal("invalid order partially persisted")
	}
}

func TestIssue138SameRootAllAdmissionPathsEnforceOrder(t *testing.T) {
	for _, api := range []string{"admit", "resume"} {
		t.Run(api, func(t *testing.T) {
			st, _, root, p, claims, _ := s138Setup(t, 2, 2, true)
			b, err := p.FetchBatch(context.Background(), claims)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			id := s138Roster(t, st, b.Checkpoint())[1]
			if err := st.MarkSnapshotSubmitted(context.Background(), st.Pool(), id); err != nil {
				t.Fatal(err)
			}
			if api == "admit" {
				_, err = st.AdmitSnapshot(context.Background(), root, id)
			} else {
				_, _, err = st.AdmitOrResumeSnapshot(context.Background(), root, id)
			}
			if !errors.Is(err, postgres.ErrScopedFetchStale) {
				t.Fatal("Stage1 bypass", api, err)
			}
			if f138Count(t, st, "index_admission") != 1 {
				t.Fatal("out-of-order allocation")
			}
		})
	}
}

func TestIssue138SameRootMalformedRosterCannotGainAuthority(t *testing.T) {
	for _, fault := range []string{"roster-duplicate", "foreign-root", "digest", "coverage-base", "roster-rank"} {
		t.Run(fault, func(t *testing.T) {
			st, _, _, p, claims, _ := s138Setup(t, 2, 2, true)
			b, err := p.FetchBatch(context.Background(), claims)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			id := b.Checkpoint()
			snap, err := st.GetSnapshot(context.Background(), st.Pool(), id)
			if err != nil {
				t.Fatal(err)
			}
			var meta map[string]json.RawMessage
			json.Unmarshal(snap.Provenance, &meta)
			var f postgres.ScopedFetchFence
			json.Unmarshal(meta["scoped_fetch_fence"], &f)
			switch fault {
			case "roster-duplicate":
				f.Lineage.Snapshots[1] = id
			case "foreign-root":
				f.Lineage.Snapshots[1] = "00000000-0000-4000-8000-000000000001"
			case "digest":
				f.Lineage.DependencyDigest = "invalid"
			case "roster-rank":
				f.Lineage.Rank = 1
			case "coverage-base":
				var c domain.DirectoryCoverage
				json.Unmarshal(meta["directory_coverage"], &c)
				c.Generation++
				meta["directory_coverage"], _ = json.Marshal(c)
			}
			meta["scoped_fetch_fence"], _ = json.Marshal(f)
			raw, _ := json.Marshal(meta)
			if _, err := st.Pool().Exec(context.Background(), `UPDATE index_snapshot SET provenance=$2 WHERE snapshot_id=$1`, id, raw); err != nil {
				t.Fatal(err)
			}
			before := f138Count(t, st, "index_journal_event")
			if _, err := p.Commit(context.Background(), b, 0); err == nil {
				t.Fatal("malformed input accepted", fault)
			}
			if f138Count(t, st, "index_journal_event") != before {
				t.Fatal("malformed input wrote Q8")
			}
		})
	}
}

func TestIssue138SameRootVirtualEmptyCannotRemoveRealChild(t *testing.T) {
	st, m, root, p, claims, src := s138Setup(t, 2, 2, true)
	m.set("/d001", fileEntry("keep.txt", 777, "keep"))
	if _, err := p4ScanService(st).ScanScope(context.Background(), root, "/d001", 1000); err != nil {
		t.Fatal(err)
	}
	m.set("/d001")
	src.virtual.Store(true)
	b, err := p.FetchBatch(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range claims {
		if res, err := p.Commit(context.Background(), b, i); err != nil || !res.MirrorPresence {
			t.Fatal(err)
		}
		s138Complete(t, st, c)
	}
	b.Close()
	qr := postgres.NewQueryReader(st.Pool())
	r, err := qr.ResolvePath(context.Background(), root, "/d001/keep.txt", query.ReadOptions{})
	if err != nil || len(r.Matches) != 1 {
		t.Fatal("virtual empty removed real file", r, err)
	}
	src.virtual.Store(false)
	next := f138Claim(t, st, root, "/d001")
	one, err := p.FetchBatch(context.Background(), []state.DirtyScopeWork{next})
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if _, err := p.Commit(context.Background(), one, 0); err != nil {
		t.Fatal(err)
	}
	s138Complete(t, st, next)
	r, err = qr.ResolvePath(context.Background(), root, "/d001/keep.txt", query.ReadOptions{})
	if err != nil || len(r.Matches) != 0 {
		t.Fatal("qualified empty did not remove", r, err)
	}
}

// Kill only a backend blocked in THIS purpose-guarded disposable DB.
func s138TerminateBlocked(t *testing.T, st *postgres.Store, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		var pids []int32
		if err := st.Pool().QueryRow(ctx, `SELECT coalesce(array_agg(pid),'{}'::int[]) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND wait_event='advisory' AND pid<>pg_backend_pid()`).Scan(&pids); err != nil {
			t.Fatal(err)
		}
		if len(pids) == 1 {
			var killed bool
			if err := st.Pool().QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pids[0]).Scan(&killed); err != nil || !killed {
				t.Fatal(killed, err)
			}
			return
		}
		if len(pids) > 1 {
			t.Fatal("ambiguous backend")
		}
		select {
		case err := <-done:
			t.Fatalf("operation returned before fault: %v", err)
		case <-ctx.Done():
			t.Fatal("backend did not block")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestIssue138SameRootBackendKillRollsBackFreezeAndCommit(t *testing.T) {
	for _, phase := range []string{"freeze", "commit"} {
		t.Run(phase, func(t *testing.T) {
			st, _, root, p, claims, src := s138Setup(t, 2, 2, true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			controller, err := st.Pool().Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Release()
			if _, err := controller.Exec(ctx, `SELECT pg_advisory_lock(138991)`); err != nil {
				t.Fatal(err)
			}
			defer controller.Exec(context.Background(), `SELECT pg_advisory_unlock(138991)`)
			var b *scan.SameRootBatch
			if phase == "commit" {
				b, err = p.FetchBatch(ctx, claims)
				if err != nil {
					t.Fatal(err)
				}
				defer b.Close()
			}
			var sql string
			if phase == "freeze" {
				sql = `CREATE FUNCTION issue138_freeze_stop() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.provenance->'scoped_fetch_fence'->'lineage'->>'rank'='1' THEN PERFORM pg_advisory_xact_lock(138991); END IF; RETURN NEW; END $$; CREATE TRIGGER issue138_freeze_stop BEFORE INSERT ON index_snapshot FOR EACH ROW EXECUTE FUNCTION issue138_freeze_stop()`
			} else {
				sql = `CREATE FUNCTION issue138_commit_stop() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(138991); RETURN NEW; END $$; CREATE TRIGGER issue138_commit_stop BEFORE INSERT ON index_generation FOR EACH ROW EXECUTE FUNCTION issue138_commit_stop()`
			}
			if _, err := st.Pool().Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			before := f138Count(t, st, "index_journal_event")
			done := make(chan error, 1)
			go func() {
				if phase == "freeze" {
					got, e := p.FetchBatch(ctx, claims)
					if got != nil {
						got.Close()
					}
					done <- e
				} else {
					_, e := p.Commit(ctx, b, 0)
					done <- e
				}
			}()
			s138TerminateBlocked(t, st, done)
			if err := <-done; err == nil {
				t.Fatal("killed transaction reported success")
			}
			if f138Count(t, st, "index_journal_event") != before {
				t.Fatal("partial Q8 escaped transaction")
			}
			var frozen int
			if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_snapshot WHERE provenance->'scoped_fetch_fence'->>'version'='2'`).Scan(&frozen); err != nil {
				t.Fatal(err)
			}
			if phase == "freeze" {
				if frozen != 0 {
					t.Fatal("partial frozen roster", frozen)
				}
				return
			}
			if frozen != 2 {
				t.Fatal("frozen facts lost", frozen)
			}
			if _, err := st.Pool().Exec(ctx, `DROP TRIGGER issue138_commit_stop ON index_generation`); err != nil {
				t.Fatal(err)
			}
			if _, err := controller.Exec(ctx, `SELECT pg_advisory_unlock(138991)`); err != nil {
				t.Fatal(err)
			}
			restarted, err := scan.NewSameRootPrototype(p4ScanService(st), 2)
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := restarted.ResumeBatch(ctx, b.Checkpoint())
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close()
			for i, c := range claims {
				if _, err := restarted.Commit(ctx, resumed, i); err != nil {
					t.Fatal(err)
				}
				s138Complete(t, st, c)
			}
			if src.calls.Load() != 2 || f138Count(t, st, "index_journal_event") != before+2 {
				t.Fatal("durable recovery refetched or duplicated")
			}
			r, err := st.GetRoot(ctx, st.Pool(), root)
			if err != nil || r.CurrentGeneration != 3 {
				t.Fatal("partial generation survived", r, err)
			}
		})
	}
}
