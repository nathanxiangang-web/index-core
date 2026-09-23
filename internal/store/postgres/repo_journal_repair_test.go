package postgres_test

import (
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func TestRepairJournalOnDeletedRootAppendsWithoutCanonicalOrGenerationChange(t *testing.T) {
	st, ctx := newStore(t)
	seedRootAndSnapshot(t, st, ctx)

	// Build generation 1 with one journal event (seq 1, intra 1).
	seq, _ := st.AllocateAdmission(ctx, st.Pool(), txRoot, txSnap)
	if _, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: txRoot, AdmissionSeq: seq, SnapshotID: txSnap, Identity: identity("repair1"),
	}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		return &postgres.Plan{
			MutatesCanonical: true,
			Events:           []domain.JournalEvent{{EventType: domain.EventResourceAdded, Payload: []byte(`{}`)}},
		}, nil
	}); err != nil {
		t.Fatalf("seed reconcile: %v", err)
	}
	genBefore := generationOf(t, st, ctx)
	eventsBefore := countJournal(t, st, ctx)
	if genBefore != 1 || eventsBefore != 1 {
		t.Fatalf("setup expected generation 1 / 1 event, got %d / %d", genBefore, eventsBefore)
	}

	// A DELETED root still permits internal Journal audit repair (JD16).
	if err := st.SetRootLifecycle(ctx, st.Pool(), txRoot, domain.RootDeleted); err != nil {
		t.Fatalf("delete root: %v", err)
	}

	corrected, err := st.RepairJournal(ctx, txRoot, domain.JournalEvent{
		EventType: domain.EventResourceAdded, Payload: []byte(`{"repair":true}`),
	})
	if err != nil {
		t.Fatalf("repair journal on DELETED root must succeed: %v", err)
	}
	if corrected.GenerationNumber != genBefore {
		t.Fatalf("repair must assert the current unchanged generation %d, got %d", genBefore, corrected.GenerationNumber)
	}
	if corrected.EventSeq != int64(eventsBefore)+1 {
		t.Fatalf("repair must use the next per-root event_seq %d, got %d", eventsBefore+1, corrected.EventSeq)
	}
	if corrected.IntraGenerationSeq != 2 {
		t.Fatalf("repair intra_generation_seq must be MAX+1 (=2) for the existing generation, got %d", corrected.IntraGenerationSeq)
	}

	// Generation and canonical truth are unchanged; the journal only grew by one.
	if gen := generationOf(t, st, ctx); gen != genBefore {
		t.Fatalf("repair must not advance generation, got %d", gen)
	}
	if n := countJournal(t, st, ctx); n != eventsBefore+1 {
		t.Fatalf("repair must append exactly one event, got %d", n)
	}

	// A new external reconcile on the same DELETED root is still rejected.
	seq2, _ := st.AllocateAdmission(ctx, st.Pool(), txRoot, txSnap)
	out, err := st.ReconcileHead(ctx, postgres.ReconcileInput{
		RootID: txRoot, AdmissionSeq: seq2, SnapshotID: txSnap, Identity: identity("repair2"),
	}, func(_ []reconcile.PriorResource, _ domain.Snapshot, gen int64) (*postgres.Plan, error) {
		t.Fatal("DELETED root must reject before plan computation")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if out.Status != domain.AdmissionRejected {
		t.Fatalf("DELETED root must still reject new external reconciles, got %s", out.Status)
	}
}

func TestRepairJournalNormalRootAlsoAppendsWithoutBump(t *testing.T) {
	st, ctx := newStore(t)
	seedRootAndSnapshot(t, st, ctx)
	// Fresh root at generation 0; repair appends seq 1, intra 1 at generation 0.
	corrected, err := st.RepairJournal(ctx, txRoot, domain.JournalEvent{
		EventType: domain.EventResourceUpdated, Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if corrected.GenerationNumber != 0 || corrected.EventSeq != 1 || corrected.IntraGenerationSeq != 1 {
		t.Fatalf("repair on fresh root must be gen0/seq1/intra1, got %+v", corrected)
	}
	if gen := generationOf(t, st, ctx); gen != 0 {
		t.Fatalf("repair must not advance a zero generation, got %d", gen)
	}
}
