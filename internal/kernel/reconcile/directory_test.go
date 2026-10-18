package reconcile

import (
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"testing"
	"time"
)

func TestDirectoryMirrorRemovesDeletedTreeNotSiblings(t *testing.T) {
	dir := priorFile("dir", "/gallery", "", 0, epoch)
	isDir := true
	dir.IsDir = &isDir
	prior := []PriorResource{dir, priorFile("photo", "/gallery/a.jpg", "a", 1, epoch), priorFile("nested", "/gallery/deep/b.jpg", "b", 2, epoch), priorFile("sibling", "/gallery-other/a.jpg", "c", 3, epoch), priorFile("other", "/docs/a.txt", "d", 4, epoch)}
	res := ReconcileDirectory(prior, nil, "/", Config{RemovalGracePeriod: 24 * time.Hour, MoveRecognitionHorizon: 24 * time.Hour}, epoch, snap1)
	if res.Counts.Removed != 3 {
		t.Fatalf("expected directory and descendants, got %+v", res.Counts)
	}
	for _, tr := range res.Transitions {
		if tr.ResourceID == "sibling" || tr.ResourceID == "other" {
			t.Fatal("unrelated subtree removed")
		}
	}
	ordinary := Reconcile(prior, nil, domain.AcceptancePartial, Config{}, epoch, snap1)
	if ordinary.Counts.Removed != 0 {
		t.Fatal("ordinary PARTIAL became destructive")
	}
}

func TestDirectoryMirrorFilesAndEmptyScope(t *testing.T) {
	prior := []PriorResource{priorFile("gone", "/docs/gone.txt", "a", 1, epoch), priorFile("keep", "/docs/keep.txt", "b", 2, epoch), priorFile("outside", "/elsewhere/x", "c", 3, epoch)}
	entries := []domain.SnapshotEntry{entryFile("keep.txt", "/docs", "b", 2, epoch)}
	r := ReconcileDirectory(prior, entries, "/docs", Config{}, epoch, snap1)
	if r.Counts.Removed != 1 {
		t.Fatalf("counts %+v", r.Counts)
	}
	r = ReconcileDirectory(prior, nil, "/docs", Config{}, epoch, snap2)
	if r.Counts.Removed != 2 {
		t.Fatalf("empty directory must remove only its direct files: %+v", r.Counts)
	}
}
