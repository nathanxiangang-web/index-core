package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

const (
	txRoot = "aaaaaaaa-0000-0000-0000-000000000001"
	txSnap = "bbbbbbbb-0000-0000-0000-000000000001"
)

func seedRootAndSnapshot(t *testing.T, st *postgres.Store, ctx context.Context) {
	t.Helper()
	if err := st.CreateRoot(ctx, st.Pool(), txRoot, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	snap := domain.Snapshot{
		SnapshotID: txSnap, RootID: txRoot, Provenance: []byte(`{}`), ObservedAt: time.Now().UTC(),
		TraversalStatus: domain.TraversalSuccess, CompletenessFlag: domain.CompletenessFlagComplete,
		LifecycleState: domain.SnapshotDraft,
	}
	if err := st.InsertSnapshotStub(ctx, st.Pool(), snap); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	// Stage 2 only accepts an EVALUATED snapshot (R2-12).
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), txSnap); err != nil {
		t.Fatalf("submit snapshot: %v", err)
	}
	if err := st.SetSnapshotEvaluated(ctx, st.Pool(), txSnap, domain.AcceptanceComplete, nil); err != nil {
		t.Fatalf("evaluate snapshot: %v", err)
	}
}

func identity(v string) domain.SnapshotIdentity {
	return domain.SnapshotIdentity{
		Kind: domain.IdentityDeterministicDigest, Namespace: "kernel.index-core/io3", Version: "v1", Value: v,
	}
}

func generationOf(t *testing.T, st *postgres.Store, ctx context.Context) int64 {
	t.Helper()
	r, err := st.GetRoot(ctx, st.Pool(), txRoot)
	if err != nil {
		t.Fatalf("get root: %v", err)
	}
	return r.CurrentGeneration
}

func countJournal(t *testing.T, st *postgres.Store, ctx context.Context) int {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_journal_event WHERE root_id=$1::uuid`, txRoot).Scan(&n); err != nil {
		t.Fatalf("count journal: %v", err)
	}
	return n
}

func TestReconcileZeroMutationKeepsGenerationAndRecordsApplication(t *testing.T) {
	st, ctx := newStore(t)
	seedRootAndSnapshot(t, st, ctx)
	seq, err := st.AllocateAdmission(ctx, st.Pool(), txRoot, txSnap)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	out, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: txRoot, AdmissionSeq: seq, SnapshotID: txSnap, Identity: identity("d1"),
	}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		return &postgres.Plan{MutatesCanonical: false}, nil
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if out.Status != domain.AdmissionApplied || out.Mutated {
		t.Fatalf("zero-mutation reconcile must APPLY without mutation, got %+v", out)
	}
	if gen := generationOf(t, st, ctx); gen != 0 {
		t.Fatalf("zero mutation must NOT advance generation, got %d", gen)
	}
	if n := countJournal(t, st, ctx); n != 0 {
		t.Fatalf("zero mutation must emit no journal event, got %d", n)
	}
	// Yet the application is recorded at the unchanged generation, so a second
	// identical collection at generation 0 becomes a NO-OP (C-AS3).
	ok, _ := st.AppliedExistsAtGeneration(ctx, st.Pool(), txRoot, identity("d1"), 0)
	if !ok {
		t.Fatal("zero-mutation reconcile must record an application row at the unchanged generation")
	}
}

func TestReconcileMutationAdvancesGenerationAndAppendsJournal(t *testing.T) {
	st, ctx := newStore(t)
	seedRootAndSnapshot(t, st, ctx)
	seq, _ := st.AllocateAdmission(ctx, st.Pool(), txRoot, txSnap)
	out, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: txRoot, AdmissionSeq: seq, SnapshotID: txSnap, Identity: identity("d2"),
	}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		return &postgres.Plan{
			MutatesCanonical: true,
			Events: []domain.JournalEvent{
				{EventType: domain.EventResourceAdded, Payload: []byte(`{}`)},
				{EventType: domain.EventResourceUpdated, Payload: []byte(`{}`)},
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !out.Mutated || out.AppliedGeneration != 1 {
		t.Fatalf("mutation must advance to generation 1, got %+v", out)
	}
	if gen := generationOf(t, st, ctx); gen != 1 {
		t.Fatalf("generation must be 1, got %d", gen)
	}
	events, err := st.ReadJournal(ctx, st.Pool(), txRoot, 0, 10)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(events) != 2 || events[0].EventSeq != 1 || events[1].EventSeq != 2 {
		t.Fatalf("journal must contain ordered seq 1,2, got %+v", events)
	}
	if events[0].IntraGenerationSeq != 1 || events[1].IntraGenerationSeq != 2 {
		t.Fatalf("same-generation intra seq must be 1 then 2, got %d,%d",
			events[0].IntraGenerationSeq, events[1].IntraGenerationSeq)
	}
}

func TestReconcileRejectsNonHeadAdmission(t *testing.T) {
	st, ctx := newStore(t)
	seedRootAndSnapshot(t, st, ctx)
	if _, err := st.AllocateAdmission(ctx, st.Pool(), txRoot, txSnap); err != nil {
		t.Fatalf("alloc 1: %v", err)
	}
	seq2, _ := st.AllocateAdmission(ctx, st.Pool(), txRoot, txSnap)
	_, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: txRoot, AdmissionSeq: seq2, SnapshotID: txSnap, Identity: identity("d3"),
	}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		return &postgres.Plan{}, nil
	})
	if !errors.Is(err, postgres.ErrNotHead) {
		t.Fatalf("processing a non-head admission must be rejected with ErrNotHead, got %v", err)
	}
}

func TestReconcileCASConflictLeavesNoPartialState(t *testing.T) {
	st, ctx := newStore(t)
	seedRootAndSnapshot(t, st, ctx)
	seq, _ := st.AllocateAdmission(ctx, st.Pool(), txRoot, txSnap)
	stale := int64(99)
	_, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: txRoot, AdmissionSeq: seq, SnapshotID: txSnap, Identity: identity("d4"),
		ExpectedGeneration: &stale,
	}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		return &postgres.Plan{MutatesCanonical: true, Events: []domain.JournalEvent{
			{EventType: domain.EventResourceAdded, Payload: []byte(`{}`)},
		}}, nil
	})
	if !errors.Is(err, postgres.ErrCASConflict) {
		t.Fatalf("stale expected generation must raise ErrCASConflict, got %v", err)
	}
	if gen := generationOf(t, st, ctx); gen != 0 {
		t.Fatalf("CAS conflict must not change generation, got %d", gen)
	}
	if n := countJournal(t, st, ctx); n != 0 {
		t.Fatalf("CAS conflict must leave no journal event, got %d", n)
	}
}

func TestReconcileRollbackLeavesNoPartialStateThenFailed(t *testing.T) {
	st, ctx := newStore(t)
	seedRootAndSnapshot(t, st, ctx)
	seq, _ := st.AllocateAdmission(ctx, st.Pool(), txRoot, txSnap)
	_, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: txRoot, AdmissionSeq: seq, SnapshotID: txSnap, Identity: identity("d5"),
	}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		return &postgres.Plan{
			MutatesCanonical: true,
			Apply: func(context.Context, pgx.Tx, int64) error {
				return errors.New("injected failure after generation advance")
			},
			Events: []domain.JournalEvent{{EventType: domain.EventResourceAdded, Payload: []byte(`{}`)}},
		}, nil
	})
	if err == nil {
		t.Fatal("injected Apply failure must surface as an error")
	}
	if gen := generationOf(t, st, ctx); gen != 0 {
		t.Fatalf("rolled-back reconcile must not advance generation, got %d", gen)
	}
	if n := countJournal(t, st, ctx); n != 0 {
		t.Fatalf("rolled-back reconcile must leave no journal event, got %d", n)
	}
	if ok, _ := st.AppliedExistsAtGeneration(ctx, st.Pool(), txRoot, identity("d5"), 1); ok {
		t.Fatal("rolled-back reconcile must leave no application row")
	}
	adm, _ := st.GetAdmission(ctx, st.Pool(), txRoot, seq)
	if adm.Status != domain.AdmissionPending {
		t.Fatalf("admission must remain PENDING after rollback, got %s", adm.Status)
	}
	if err := st.MarkAdmissionFailed(ctx, txRoot, seq, "injected"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	adm, _ = st.GetAdmission(ctx, st.Pool(), txRoot, seq)
	if adm.Status != domain.AdmissionFailed {
		t.Fatalf("admission must become FAILED in a later transaction, got %s", adm.Status)
	}
}

func TestReconcileNOOPOnSameGenerationReplay(t *testing.T) {
	st, ctx := newStore(t)
	seedRootAndSnapshot(t, st, ctx)
	id := identity("d6")
	if err := st.InsertAppliedSnapshot(ctx, st.Pool(), domain.AppliedSnapshot{
		RootID: txRoot, SnapshotIdentityKind: id.Kind, SnapshotIdentityNamespace: id.Namespace,
		SnapshotIdentityVersion: id.Version, SnapshotIdentityValue: id.Value,
		SnapshotID: txSnap, AppliedGeneration: 0, AppliedAdmissionSeq: 0,
	}); err != nil {
		t.Fatalf("seed applied: %v", err)
	}
	seq, _ := st.AllocateAdmission(ctx, st.Pool(), txRoot, txSnap)
	out, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: txRoot, AdmissionSeq: seq, SnapshotID: txSnap, Identity: id,
	}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		t.Fatal("NO-OP must not compute or apply a plan")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if out.Status != domain.AdmissionNoop {
		t.Fatalf("same identity at same generation must be NOOP, got %s", out.Status)
	}
	if n := countJournal(t, st, ctx); n != 0 {
		t.Fatalf("NOOP must emit no journal event, got %d", n)
	}
}

func TestReconcileDELETEDRootRejectsWithoutMutation(t *testing.T) {
	st, ctx := newStore(t)
	seedRootAndSnapshot(t, st, ctx)
	if err := st.SetRootLifecycle(ctx, st.Pool(), txRoot, domain.RootDeleted); err != nil {
		t.Fatalf("delete root: %v", err)
	}
	seq, _ := st.AllocateAdmission(ctx, st.Pool(), txRoot, txSnap)
	out, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: txRoot, AdmissionSeq: seq, SnapshotID: txSnap, Identity: identity("d7"),
	}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		t.Fatal("a DELETED root must be rejected before plan computation")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if out.Status != domain.AdmissionRejected {
		t.Fatalf("DELETED root must REJECT the reconcile, got %s", out.Status)
	}
	if n := countJournal(t, st, ctx); n != 0 {
		t.Fatalf("DELETED-root rejection must emit no journal event, got %d", n)
	}
}
