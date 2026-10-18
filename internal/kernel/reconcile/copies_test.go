package reconcile

import (
	"strings"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

func TestLiveCopiesIdentityBoundaries(t *testing.T) {
	live := priorFile("original", "/a.txt", "same", 10, epoch)
	copy := priorFile("copy", "/b.txt", "same", 10, epoch)
	missing := priorMissing("missing", "/old.txt", "same", 10, epoch, epoch.Add(-time.Minute), snap1)
	old := priorMissing("old", "/expired.txt", "same", 10, epoch, epoch.Add(-3*time.Hour), snap1)
	cfg := Config{MoveRecognitionHorizon: time.Hour, RemovalGracePeriod: 2 * time.Hour}
	cases := []struct {
		name    string
		prior   []PriorResource
		entries []domain.SnapshotEntry
		want    IdentityResolution
		id      string
	}{
		{"one live candidate", []PriorResource{live}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch)}, ResolutionNewResource, ""},
		{"two live candidates", []PriorResource{live, copy}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch)}, ResolutionNewResource, ""},
		{"same path among copies", []PriorResource{live, copy}, []domain.SnapshotEntry{entryFile("b.txt", "/", "same", 10, epoch)}, ResolutionMatched, "copy"},
		{"same path missing wins", []PriorResource{live, missing}, []domain.SnapshotEntry{entryFile("old.txt", "/", "same", 10, epoch)}, ResolutionMatched, "missing"},
		{"unique missing move", []PriorResource{missing}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch)}, ResolutionMatched, "missing"},
		{"expired missing", []PriorResource{old}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch)}, ResolutionUnresolved, ""},
		{"live and missing ambiguous", []PriorResource{live, missing}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch)}, ResolutionConflict, ""},
		{"two missing ambiguous", []PriorResource{old, missing}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch)}, ResolutionConflict, ""},
		{"two observed destinations", []PriorResource{missing}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch), entryFile("copy.txt", "/", "same", 10, epoch)}, ResolutionConflict, ""},
		{"old path still observed", []PriorResource{missing}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch), entryFile("old.txt", "/", "same", 10, epoch)}, ResolutionConflict, ""},
		{"impostor not merged elsewhere", []PriorResource{live, priorFile("old-content", "/new.txt", "other", 10, epoch)}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch)}, ResolutionNewResource, ""},
		{"ambiguous canonical path", []PriorResource{live, priorFile("duplicate", "/a.txt", "other", 10, epoch)}, []domain.SnapshotEntry{entryFile("a.txt", "/", "same", 10, epoch)}, ResolutionConflict, ""},
		{"tombstone plus live copy", []PriorResource{live, priorRemoved("removed", "/new.txt", "P1", "same", 10, epoch)}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch)}, ResolutionNewResource, ""},
		{"stable id remains authoritative", []PriorResource{priorWithProvider("stable", "/old.txt", "P1", "other", 10, epoch), live}, []domain.SnapshotEntry{entryWithProvider("new.txt", "/", "P1", "same", 10, epoch)}, ResolutionMatched, "stable"},
		{"duplicate observed path", []PriorResource{live}, []domain.SnapshotEntry{entryFile("new.txt", "/", "same", 10, epoch), entryFile("new.txt", "/", "same", 10, epoch)}, ResolutionConflict, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ix := BuildPriorIndex(tt.prior)
			ix.indexObservation(tt.entries)
			got, p, reason := ix.resolve(tt.entries[0], cfg, epoch)
			if got != tt.want || (tt.id != "" && (p == nil || p.ResourceID != tt.id)) || (tt.id == "" && p != nil) {
				t.Fatalf("got %s %v (%s), want %s %s", got, p, reason, tt.want, tt.id)
			}
		})
	}
}

func TestLiveCopiesReobserveAndRemoveIndependently(t *testing.T) {
	prior := []PriorResource{priorFile("a", "/a.txt", "same", 10, epoch), priorFile("b", "/b.txt", "same", 10, epoch), priorFile("c", "/c.txt", "same", 10, epoch)}
	entries := []domain.SnapshotEntry{entryFile("c.txt", "/", "same", 10, epoch), entryFile("a.txt", "/", "same", 10, epoch)}
	res := Reconcile(prior, entries, domain.AcceptancePartial, Config{}, epoch, snap2)
	if res.Counts.Unchanged != 2 || res.Counts.Conflict != 0 || res.Counts.Removed != 0 || res.MutatesCanonical {
		t.Fatalf("PARTIAL copies must retain independent continuity: %+v", res.Counts)
	}
	context := RemovalDecisionContext(prior, entries, Config{}, epoch)
	if !strings.Contains(context, "rid=b|") || strings.Contains(context, "rid=a|") || strings.Contains(context, "rid=c|") {
		t.Fatalf("removal context disagrees with copy matching: %s", context)
	}
	prior[1] = priorMissing("b", "/b.txt", "same", 10, epoch, epoch.Add(-time.Hour), snap1)
	res = Reconcile(prior, entries, domain.AcceptanceComplete, Config{MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1}, epoch, snap2)
	if res.Counts.Removed != 1 || res.Counts.Unchanged != 2 || res.Counts.Conflict != 0 {
		t.Fatalf("only missing copy should be removed: %+v", res.Counts)
	}
	for _, tr := range res.Transitions {
		if tr.Kind == KindConfirmRemoved && tr.ResourceID != "b" {
			t.Fatalf("removed a different copy: %s", tr.ResourceID)
		}
	}
}
