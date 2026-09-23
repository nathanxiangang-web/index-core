package reconcile

import (
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func i64(v int64) *int64 { return &v }
func tm(t time.Time) *time.Time {
	return &t
}
func sp(s string) *string { return &s }

func priorFile(id, path, hash string, size int64, mtime time.Time) PriorResource {
	return PriorResource{
		CanonicalResource: domain.CanonicalResource{
			ResourceID: id, ResourcePresence: domain.ResourcePresent,
			RemovalEvidenceState: domain.RemovalEvidenceNone,
			CanonicalPath:        sp(path), Name: sp(base(path)),
			Size: i64(size), Mtime: tm(mtime), ContentHash: sp(hash),
		},
		ProviderIDAssurance: domain.IdentityUnavailable,
	}
}

func entryFile(name, parent, hash string, size int64, mtime time.Time) domain.SnapshotEntry {
	return domain.SnapshotEntry{
		EntryLocalID: name, Name: name, ParentRef: parent,
		Size: i64(size), Mtime: tm(mtime), ContentHash: sp(hash),
	}
}

func priorFileWithProvider(id, path, provID, scope, hash string, size int64, mtime time.Time) PriorResource {
	p := priorFile(id, path, hash, size, mtime)
	p.ProviderObjectID = sp(provID)
	p.ProviderObjectIDScope = sp(scope)
	p.ProviderIDAssurance = domain.IdentityStableWithinScope
	return p
}

func entryFileWithProvider(name, parent, provID, scope, hash string, size int64, mtime time.Time) domain.SnapshotEntry {
	e := entryFile(name, parent, hash, size, mtime)
	e.ProviderObjectID = sp(provID)
	e.ProviderObjectIDScope = sp(scope)
	return e
}

func base(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func hasKind(res Result, kind Kind) bool {
	for _, tr := range res.Transitions {
		if tr.Kind == kind {
			return true
		}
	}
	return false
}

func TestReconcileInitialAdd(t *testing.T) {
	entries := []domain.SnapshotEntry{
		entryFile("a.txt", "/", "h1", 1, epoch),
		entryFile("b.txt", "/", "h2", 2, epoch),
	}
	res := Reconcile(nil, entries, domain.AcceptanceComplete, Config{}, epoch)
	if !res.MutatesCanonical || res.Counts.Added != 2 {
		t.Fatalf("initial snapshot must ADD 2, got %+v", res.Counts)
	}
}

func TestReconcileUpdateAndAdd(t *testing.T) {
	// Identity continuity for an in-place content change requires a stable
	// provider_object_id (R1); without it a changed path+content is an R8 imposter.
	prior := []PriorResource{priorFileWithProvider("r1", "/a.txt", "P1", "root", "h1", 1, epoch)}
	entries := []domain.SnapshotEntry{
		entryFileWithProvider("a.txt", "/", "P1", "root", "h2", 5, epoch.Add(time.Hour)), // UPDATE
		entryFile("b.txt", "/", "h3", 2, epoch),                                          // ADD
	}
	res := Reconcile(prior, entries, domain.AcceptanceComplete, Config{}, epoch)
	if res.Counts.Updated != 1 || res.Counts.Added != 1 {
		t.Fatalf("expected 1 update + 1 add, got %+v", res.Counts)
	}
}

func TestReconcileRenameAndMove(t *testing.T) {
	prior := []PriorResource{priorFile("r1", "/dir/a.txt", "h1", 1, epoch)}
	rename := Reconcile(prior,
		[]domain.SnapshotEntry{entryFile("b.txt", "/dir", "h1", 1, epoch)},
		domain.AcceptanceComplete, Config{}, epoch)
	if rename.Counts.Renamed != 1 {
		t.Fatalf("same parent, changed name must RENAME, got %+v", rename.Counts)
	}

	move := Reconcile(prior,
		[]domain.SnapshotEntry{entryFile("a.txt", "/other", "h1", 1, epoch)},
		domain.AcceptanceComplete, Config{}, epoch)
	if move.Counts.Moved != 1 {
		t.Fatalf("changed parent must MOVE, got %+v", move.Counts)
	}
}

func TestReconcileMovePlusUpdateOrderedPair(t *testing.T) {
	prior := []PriorResource{priorFileWithProvider("r1", "/dir/a.txt", "P1", "root", "h1", 1, epoch)}
	res := Reconcile(prior,
		[]domain.SnapshotEntry{entryFileWithProvider("a.txt", "/other", "P1", "root", "h2", 9, epoch.Add(time.Hour))},
		domain.AcceptanceComplete, Config{}, epoch)
	if len(res.Transitions) != 2 ||
		res.Transitions[0].Kind != KindMove || res.Transitions[1].Kind != KindUpdate {
		t.Fatalf("move+update must be ordered pair [MOVE, UPDATE], got %+v", res.Transitions)
	}
}

func TestReconcilePartialDoesNotAdvanceRemoval(t *testing.T) {
	prior := []PriorResource{priorFile("r1", "/a.txt", "h1", 1, epoch)}
	res := Reconcile(prior, nil, domain.AcceptancePartial, Config{}, epoch)
	if res.MutatesCanonical || len(res.Transitions) != 0 {
		t.Fatalf("PARTIAL must not advance removal evidence, got %+v", res)
	}
}

func TestReconcileCompleteMissingThenConfirmedRemoval(t *testing.T) {
	// minConsecutive=2 so a single missing observation is still MISSING_EVIDENCE,
	// not yet a REMOVAL_CANDIDATE.
	first := Reconcile([]PriorResource{priorFile("r1", "/a.txt", "h1", 1, epoch)},
		nil, domain.AcceptanceComplete,
		Config{RemovalGracePeriod: time.Hour, MinConsecutiveCompleteMissing: 2}, epoch)
	if first.Counts.MissingEvidence != 1 || !hasKind(first, KindMissingEvidence) {
		t.Fatalf("first missing in COMPLETE must record MISSING_EVIDENCE, got %+v", first.Counts)
	}

	// A resource already missing long enough, complete snapshot -> CONFIRMED_REMOVED.
	missing := priorFile("r1", "/a.txt", "h1", 1, epoch)
	missing.MissingSince = tm(epoch.Add(-48 * time.Hour))
	missing.ConsecutiveCompleteMissing = 1
	cfg := Config{RemovalGracePeriod: time.Hour, MinConsecutiveCompleteMissing: 1}
	conf := Reconcile([]PriorResource{missing}, nil, domain.AcceptanceComplete, cfg, epoch)
	if !hasKind(conf, KindConfirmRemoved) || conf.Counts.Removed != 1 {
		t.Fatalf("expected CONFIRMED_REMOVED, got %+v", conf.Counts)
	}
}

func TestReconcileR8ImposterDoesNotInheritIdentity(t *testing.T) {
	prior := []PriorResource{priorFile("r1", "/p.txt", "h1", 1, epoch)}
	res := Reconcile(prior,
		[]domain.SnapshotEntry{entryFile("p.txt", "/", "h2", 7, epoch.Add(time.Hour))},
		domain.AcceptanceComplete, Config{}, epoch)
	if res.Counts.Added != 1 {
		t.Fatalf("same path with different content must be a NEW resource (R8), got %+v", res.Counts)
	}
	for _, tr := range res.Transitions {
		if tr.Kind == KindAdd && tr.ResourceID == "r1" {
			t.Fatal("imposter must NOT inherit the old resource_id")
		}
	}
}

func TestReconcileAmbiguousContentHashIsConflict(t *testing.T) {
	prior := []PriorResource{
		priorFile("r1", "/a.txt", "h1", 1, epoch),
		priorFile("r2", "/b.txt", "h1", 1, epoch),
	}
	res := Reconcile(prior,
		[]domain.SnapshotEntry{entryFile("c.txt", "/", "h1", 1, epoch)},
		domain.AcceptanceComplete, Config{}, epoch)
	if res.Counts.Conflict != 1 {
		t.Fatalf("ambiguous strong evidence must CONFLICT (never forced match), got %+v", res.Counts)
	}
}
