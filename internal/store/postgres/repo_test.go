package postgres_test

import (
	"context"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func newStore(t *testing.T) (*postgres.Store, context.Context) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return postgres.New(pool), context.Background()
}

const rootA = "11111111-1111-1111-1111-111111111111"

func TestRootAndAdmissionFIFO(t *testing.T) {
	st, ctx := newStore(t)
	if err := st.CreateRoot(ctx, st.Pool(), rootA, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	root, err := st.GetRoot(ctx, st.Pool(), rootA)
	if err != nil {
		t.Fatalf("get root: %v", err)
	}
	if root.CurrentGeneration != 0 || root.LatestAdmissionSeq != 0 {
		t.Fatalf("fresh root must start at generation 0 / admission 0, got %d/%d",
			root.CurrentGeneration, root.LatestAdmissionSeq)
	}

	seq1, err := st.AllocateAdmission(ctx, st.Pool(), rootA, "22222222-2222-2222-2222-222222222201")
	if err != nil {
		t.Fatalf("alloc 1: %v", err)
	}
	seq2, err := st.AllocateAdmission(ctx, st.Pool(), rootA, "22222222-2222-2222-2222-222222222202")
	if err != nil {
		t.Fatalf("alloc 2: %v", err)
	}
	if seq1 != 1 || seq2 != 2 {
		t.Fatalf("admission_seq must be per-root serial 1,2; got %d,%d", seq1, seq2)
	}

	head, err := st.HeadAdmission(ctx, st.Pool(), rootA)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.AdmissionSeq != 1 {
		t.Fatalf("head-of-line must be the min PENDING seq (1), got %d", head.AdmissionSeq)
	}
}

func TestJournalSeqAllocationAndReadCursor(t *testing.T) {
	st, ctx := newStore(t)
	if err := st.CreateRoot(ctx, st.Pool(), rootA, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	next, err := st.NextEventSeq(ctx, st.Pool(), rootA)
	if err != nil || next != 1 {
		t.Fatalf("empty journal next event_seq must be 1, got %d err=%v", next, err)
	}
	intra, err := st.NextIntraGenerationSeq(ctx, st.Pool(), rootA, 1)
	if err != nil || intra != 1 {
		t.Fatalf("new generation intra seq must be 1, got %d err=%v", intra, err)
	}
	ev := domain.JournalEvent{
		RootID: rootA, EventSeq: 1, GenerationNumber: 1, IntraGenerationSeq: 1,
		EventType: domain.EventResourceAdded, Payload: []byte(`{}`),
	}
	if err := st.AppendJournalEvent(ctx, st.Pool(), ev); err != nil {
		t.Fatalf("append: %v", err)
	}
	// A second event in the same generation appends after the current max.
	intra2, _ := st.NextIntraGenerationSeq(ctx, st.Pool(), rootA, 1)
	if intra2 != 2 {
		t.Fatalf("second same-generation intra seq must be 2, got %d", intra2)
	}
	ev2 := ev
	ev2.EventSeq = 2
	ev2.IntraGenerationSeq = intra2
	ev2.EventType = domain.EventResourceUpdated
	if err := st.AppendJournalEvent(ctx, st.Pool(), ev2); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	tail, _ := st.MaxEventSeq(ctx, st.Pool(), rootA)
	if tail != 2 {
		t.Fatalf("journal tail must be 2, got %d", tail)
	}
	got, err := st.ReadJournal(ctx, st.Pool(), rootA, 1, 10)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(got) != 1 || got[0].EventSeq != 2 {
		t.Fatalf("cursor read after seq 1 must return exactly seq 2, got %+v", got)
	}
}

func TestAppliedSnapshotNOOPRequiresSameGeneration(t *testing.T) {
	st, ctx := newStore(t)
	if err := st.CreateRoot(ctx, st.Pool(), rootA, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	id := domain.SnapshotIdentity{
		Kind: domain.IdentityDeterministicDigest, Namespace: "kernel.index-core/io3",
		Version: "v1", Value: "deadbeef",
	}
	rec := domain.AppliedSnapshot{
		RootID: rootA, SnapshotIdentityKind: id.Kind, SnapshotIdentityNamespace: id.Namespace,
		SnapshotIdentityVersion: id.Version, SnapshotIdentityValue: id.Value,
		SnapshotID: "33333333-3333-3333-3333-333333333301", AppliedGeneration: 5, AppliedAdmissionSeq: 1,
	}
	if err := st.InsertAppliedSnapshot(ctx, st.Pool(), rec); err != nil {
		t.Fatalf("insert applied: %v", err)
	}
	at5, _ := st.AppliedExistsAtGeneration(ctx, st.Pool(), rootA, id, 5)
	if !at5 {
		t.Fatal("identity must be NO-OP at the generation it was applied")
	}
	at6, _ := st.AppliedExistsAtGeneration(ctx, st.Pool(), rootA, id, 6)
	if at6 {
		t.Fatal("identical identity after generation advanced must NOT be a NO-OP (G14/T12)")
	}
	max, _ := st.MaxAppliedGeneration(ctx, st.Pool(), rootA, id)
	if max != 5 {
		t.Fatalf("max applied generation must be 5, got %d", max)
	}
}

func TestCanonicalPathOverlapAndPresenceInternal(t *testing.T) {
	st, ctx := newStore(t)
	if err := st.CreateRoot(ctx, st.Pool(), rootA, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	path := "/docs/report.pdf"
	mk := func(id string) domain.CanonicalResource {
		return domain.CanonicalResource{
			ResourceID: id, RootID: rootA, IntroducedAtGeneration: 1, LastConfirmedGeneration: 1,
			ResourcePresence: domain.ResourcePresent, RemovalEvidenceState: domain.RemovalEvidenceNone,
			CanonicalPath: &path, CurrentAttributes: []byte(`{}`),
		}
	}
	if err := st.InsertCanonicalResource(ctx, st.Pool(), mk("44444444-4444-4444-4444-444444444401")); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	// R8 imposter: a second PRESENT resource may share the same canonical_path.
	if err := st.InsertCanonicalResource(ctx, st.Pool(), mk("44444444-4444-4444-4444-444444444402")); err != nil {
		t.Fatalf("insert imposter at same path must be allowed: %v", err)
	}
	atPath, err := st.PresentResourcesAtPath(ctx, st.Pool(), rootA, path)
	if err != nil {
		t.Fatalf("present at path: %v", err)
	}
	if len(atPath) != 2 {
		t.Fatalf("resolve must surface BOTH overlapping PRESENT rows, got %d", len(atPath))
	}
}