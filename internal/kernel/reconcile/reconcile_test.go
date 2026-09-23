package reconcile

import (
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func i64(v int64) *int64        { return &v }
func tm(t time.Time) *time.Time { return &t }
func sp(s string) *string       { return &s }

const snap1 = "s1"
const snap2 = "s2"

func priorFile(id, path, hash string, size int64, mtime time.Time) PriorResource {
	return PriorResource{
		CanonicalResource: domain.CanonicalResource{
			ResourceID: id, ResourcePresence: domain.ResourcePresent,
			RemovalEvidenceState: domain.RemovalEvidenceNone,
			CanonicalPath:        sp(path), Name: sp(base(path)),
			Size: i64(size), Mtime: tm(mtime), ContentHash: sp(hash), HashAlgorithm: sp("sha256"),
		},
		ProviderIDAssurance: domain.IdentityUnavailable,
	}
}

func priorWithProvider(id, path, provID, hash string, size int64, mtime time.Time) PriorResource {
	p := priorFile(id, path, hash, size, mtime)
	p.ProviderObjectID = sp(provID)
	p.ProviderObjectIDScope = sp("root")
	p.ProviderIDAssurance = domain.IdentityStableWithinScope
	return p
}

// priorMissing is a live resource carrying MISSING evidence since missSince.
func priorMissing(id, path, hash string, size int64, mtime, missSince time.Time, firstSnap string) PriorResource {
	p := priorFile(id, path, hash, size, mtime)
	p.RemovalEvidenceState = domain.RemovalEvidenceMissingConfirmedByComplete
	p.MissingSince = tm(missSince)
	p.ConsecutiveCompleteMissing = 1
	p.MissingFirstSnapshotID = sp(firstSnap)
	return p
}

func priorRemoved(id, path, provID, hash string, size int64, mtime time.Time) PriorResource {
	p := priorWithProvider(id, path, provID, hash, size, mtime)
	p.ResourcePresence = domain.ResourceRemoved
	return p
}

func entryFile(name, parent, hash string, size int64, mtime time.Time) domain.SnapshotEntry {
	alg := "sha256"
	return domain.SnapshotEntry{
		EntryLocalID: name, Name: name, ParentRef: parent,
		Size: i64(size), Mtime: tm(mtime), ContentHash: sp(hash), HashAlgorithm: &alg,
	}
}

func entryWithProvider(name, parent, provID, hash string, size int64, mtime time.Time) domain.SnapshotEntry {
	e := entryFile(name, parent, hash, size, mtime)
	e.ProviderObjectID = sp(provID)
	e.ProviderObjectIDScope = sp("root")
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

func TestInitialAdd(t *testing.T) {
	entries := []domain.SnapshotEntry{
		entryFile("a.txt", "/", "h1", 1, epoch),
		entryFile("b.txt", "/", "h2", 2, epoch),
	}
	res := Reconcile(nil, entries, domain.AcceptanceComplete, Config{}, epoch, snap1)
	if res.Counts.Added != 2 || len(res.Observations) != 2 {
		t.Fatalf("initial snapshot must ADD 2 with 2 observations, got %+v / %d obs", res.Counts, len(res.Observations))
	}
}

func TestWithProviderUpdate(t *testing.T) {
	prior := []PriorResource{priorWithProvider("r1", "/a.txt", "P1", "h1", 1, epoch)}
	entries := []domain.SnapshotEntry{entryWithProvider("a.txt", "/", "P1", "h2", 5, epoch.Add(time.Hour))}
	res := Reconcile(prior, entries, domain.AcceptanceComplete, Config{}, epoch, snap2)
	if res.Counts.Updated != 1 {
		t.Fatalf("stable provider id + changed content must UPDATE, got %+v", res.Counts)
	}
	if len(res.Observations) != 1 {
		t.Fatalf("a matched entry must append an observation, got %d", len(res.Observations))
	}
}

// B1.1: same content at a NEW path while the original is still PRESENT (no
// MISSING candidate) must NOT auto-MATCH. Two live same-content resources -> CONFLICT.
func TestCrossPathHashAgainstPresentIsConflict(t *testing.T) {
	prior := []PriorResource{priorFile("r1", "/a.txt", "h1", 1, epoch)}
	res := Reconcile(prior, []domain.SnapshotEntry{entryFile("copy.txt", "/", "h1", 1, epoch)},
		domain.AcceptanceComplete, Config{}, epoch, snap2)
	if res.Counts.Conflict != 1 || res.Counts.Renamed != 0 || res.Counts.Moved != 0 {
		t.Fatalf("identical content at a new path with original PRESENT must CONFLICT, got %+v", res.Counts)
	}
}

// B1.3: cross-path hash to a MISSING candidate inside the horizon -> MATCHED (R5).
func TestCrossPathHashToMissingWithinHorizonMatches(t *testing.T) {
	cfg := Config{MoveRecognitionHorizon: 2 * time.Hour, RemovalGracePeriod: 4 * time.Hour}
	prior := []PriorResource{priorMissing("r1", "/old.txt", "h1", 1, epoch, epoch.Add(-time.Hour), snap1)}
	res := Reconcile(prior, []domain.SnapshotEntry{entryFile("new.txt", "/", "h1", 1, epoch)},
		domain.AcceptanceComplete, cfg, epoch, snap2)
	if res.Counts.Moved != 1 && res.Counts.Renamed != 1 {
		t.Fatalf("cross-path hash to MISSING inside horizon must recognize the move, got %+v", res.Counts)
	}
	if res.Counts.Added != 0 {
		t.Fatalf("must not ADD a new resource when recognizing a move, got %+v", res.Counts)
	}
}

// B1.3: same MISSING candidate outside the horizon -> UNRESOLVED.
func TestCrossPathHashToMissingOutsideHorizonUnresolved(t *testing.T) {
	cfg := Config{MoveRecognitionHorizon: time.Hour, RemovalGracePeriod: 2 * time.Hour}
	prior := []PriorResource{priorMissing("r1", "/old.txt", "h1", 1, epoch, epoch.Add(-5*time.Hour), snap1)}
	res := Reconcile(prior, []domain.SnapshotEntry{entryFile("new.txt", "/", "h1", 1, epoch)},
		domain.AcceptanceComplete, cfg, epoch, snap2)
	if res.Counts.Conflict != 1 || res.Counts.Added != 0 {
		t.Fatalf("MISSING outside horizon must be UNRESOLVED (conflict signal), got %+v", res.Counts)
	}
}

// B1.2: weak/no evidence -> UNRESOLVED, never guessed NEW_RESOURCE.
func TestWeakEvidenceIsUnresolved(t *testing.T) {
	prior := []PriorResource{priorFile("r1", "/a.txt", "h1", 3, epoch)}
	e := domain.SnapshotEntry{EntryLocalID: "a.txt", Name: "a.txt", ParentRef: "/", Size: i64(3)}
	res := Reconcile(prior, []domain.SnapshotEntry{e}, domain.AcceptanceComplete, Config{}, epoch, snap2)
	if res.Counts.Added != 0 || res.Counts.Unchanged != 0 {
		t.Fatalf("weak same-path evidence must not ADD or MATCH, got %+v", res.Counts)
	}
	if !hasKind(res, KindConflict) {
		t.Fatalf("weak evidence must be UNRESOLVED (conflict signal), got %+v", res.Transitions)
	}
}

// B1.4: unexpected provider id change at a continuous path -> UNRESOLVED.
func TestProviderIDChangeIsUnresolved(t *testing.T) {
	prior := []PriorResource{priorWithProvider("r1", "/a.txt", "P1", "h1", 1, epoch)}
	res := Reconcile(prior, []domain.SnapshotEntry{entryWithProvider("a.txt", "/", "P2", "h1", 1, epoch)},
		domain.AcceptanceComplete, Config{}, epoch, snap2)
	if res.Counts.Added != 0 {
		t.Fatalf("provider id change must not become a new resource, got %+v", res.Counts)
	}
	if res.Counts.Conflict != 1 {
		t.Fatalf("provider id change must be UNRESOLVED/CONFLICT, got %+v", res.Counts)
	}
}

// B1.4: a REMOVED tombstone must never be re-matched; reappearance is a fresh ADD.
func TestRemovedReappearanceIsNewResource(t *testing.T) {
	prior := []PriorResource{priorRemoved("r1", "/a.txt", "P1", "h1", 1, epoch)}
	res := Reconcile(prior, []domain.SnapshotEntry{entryWithProvider("a.txt", "/", "P1", "h1", 1, epoch)},
		domain.AcceptanceComplete, Config{}, epoch, snap2)
	if res.Counts.Added != 1 {
		t.Fatalf("reappearance after REMOVED must be a fresh ADD, got %+v", res.Counts)
	}
	for _, tr := range res.Transitions {
		if tr.Kind == KindAdd && tr.ResourceID == "r1" {
			t.Fatal("reappearance must NOT inherit the tombstone resource_id")
		}
	}
}

// B2: the first MISSING observation cannot self-confirm; a later independent
// admitted Snapshot is required.
func TestRemovalRequiresIndependentConfirmation(t *testing.T) {
	cfg := Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1, RemovalGracePeriod: 0}
	prior := []PriorResource{priorFile("r1", "/a.txt", "h1", 1, epoch)}

	first := Reconcile(prior, nil, domain.AcceptanceComplete, cfg, epoch, snap1)
	if !hasKind(first, KindMissingEvidence) || first.Counts.Removed != 0 {
		t.Fatalf("first MISSING must only record evidence, got %+v", first.Counts)
	}

	p1 := prior[0]
	p1.RemovalEvidenceState = domain.RemovalEvidenceMissingConfirmedByComplete
	p1.MissingSince = tm(epoch)
	p1.ConsecutiveCompleteMissing = 1
	p1.MissingFirstSnapshotID = sp(snap1)

	replay := Reconcile([]PriorResource{p1}, nil, domain.AcceptanceComplete, cfg, epoch, snap1)
	if hasKind(replay, KindConfirmRemoved) {
		t.Fatal("same-snapshot re-admission must not confirm removal")
	}

	second := Reconcile([]PriorResource{p1}, nil, domain.AcceptanceComplete, cfg, epoch, snap2)
	if !hasKind(second, KindConfirmRemoved) || second.Counts.Removed != 1 {
		t.Fatalf("later independent COMPLETE snapshot must confirm removal, got %+v", second.Counts)
	}
}

// B2: PARTIAL absence must not advance removal evidence.
func TestPartialDoesNotAdvanceRemoval(t *testing.T) {
	prior := []PriorResource{priorFile("r1", "/a.txt", "h1", 1, epoch)}
	res := Reconcile(prior, nil, domain.AcceptancePartial, Config{}, epoch, snap1)
	if res.MutatesCanonical || len(res.Transitions) != 0 {
		t.Fatalf("PARTIAL must not advance removal evidence, got %+v", res)
	}
}

// B2: reappearance resets removal evidence.
func TestReappearanceResetsRemovalEvidence(t *testing.T) {
	prior := []PriorResource{priorMissing("r1", "/a.txt", "h1", 1, epoch, epoch.Add(-time.Hour), snap1)}
	res := Reconcile(prior, []domain.SnapshotEntry{entryFile("a.txt", "/", "h1", 1, epoch)},
		domain.AcceptanceComplete, Config{}, epoch, snap2)
	if !hasKind(res, KindResetRemovalEvidence) {
		t.Fatalf("reappearance must reset removal evidence, got %+v", res.Transitions)
	}
	if res.Counts.Added != 0 {
		t.Fatalf("reappearance of a MISSING resource must MATCH, not ADD, got %+v", res.Counts)
	}
}

func TestMovePlusUpdateOrderedPair(t *testing.T) {
	prior := []PriorResource{priorWithProvider("r1", "/dir/a.txt", "P1", "h1", 1, epoch)}
	res := Reconcile(prior,
		[]domain.SnapshotEntry{entryWithProvider("a.txt", "/other", "P1", "h2", 9, epoch.Add(time.Hour))},
		domain.AcceptanceComplete, Config{}, epoch, snap2)
	if len(res.Transitions) != 2 ||
		res.Transitions[0].Kind != KindMove || res.Transitions[1].Kind != KindUpdate {
		t.Fatalf("move+update must be ordered pair [MOVE, UPDATE], got %+v", res.Transitions)
	}
}
