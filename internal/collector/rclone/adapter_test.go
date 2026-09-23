package rclone

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/completeness"
)

const sampleLSJSON = `[
  {"Path":"docs/a.txt","Name":"a.txt","Size":12,"MimeType":"text/plain","ModTime":"2026-01-02T03:04:05.000000000Z","IsDir":false,"ID":"obj-1","Hashes":{"SHA-1":"abc123"}},
  {"Path":"docs/sub","Name":"sub","Size":-1,"MimeType":"inode/directory","ModTime":"2026-01-02T03:04:05.000000000Z","IsDir":true}
]`

func TestNormalizeSuccess(t *testing.T) {
	scan := Normalize([]byte(sampleLSJSON), nil)
	if scan.TraversalStatus != domain.TraversalSuccess {
		t.Fatalf("valid output must be SUCCESS, got %s", scan.TraversalStatus)
	}
	if len(scan.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(scan.Entries))
	}
	if scan.SkippedKnownEmpty || scan.SkippedScopes != nil {
		t.Fatal("rclone has no structured skipped-set: skipped_scopes must stay UNKNOWN")
	}
	file := scan.Entries[0]
	if file.ContentHash == nil || *file.ContentHash != "abc123" || file.HashAlgorithm == nil || *file.HashAlgorithm != "sha-1" {
		t.Fatalf("hash must be normalized with algorithm, got %+v", file)
	}
	if file.ProviderObjectID == nil || *file.ProviderObjectID != "obj-1" {
		t.Fatalf("provider object id must be preserved as evidence, got %+v", file)
	}
	if file.ParentRef != "/docs" {
		t.Fatalf("parent ref must be the parent path, got %s", file.ParentRef)
	}
	if scan.Entries[1].Size != nil {
		t.Fatal("directory size must not be treated as a meaningful size")
	}
}

// The rclone adapter MUST NOT be able to claim destructive-safe COMPLETE while
// skip evidence is UNKNOWN (ADR-001, doc E Sec 7.1).
func TestRcloneCannotClaimComplete(t *testing.T) {
	scan := Normalize([]byte(sampleLSJSON), nil)
	state := completeness.Evaluate(completeness.Evidence{
		TraversalStatus:   scan.TraversalStatus,
		HasSkippedScopes:  len(scan.SkippedScopes) > 0,
		SkippedKnownEmpty: scan.SkippedKnownEmpty,
		SkippedUnknown:    scan.SkippedScopes == nil,
		Freshness:         scan.Freshness,
		Assurance:         scan.Assurance,
		EntryCount:        int64(len(scan.Entries)),
	}, completeness.Config{})
	if state == domain.AcceptanceComplete {
		t.Fatalf("rclone UNKNOWN skip evidence must never yield COMPLETE, got %s", state)
	}
}

func TestNormalizeFailure(t *testing.T) {
	scan := Normalize(nil, errors.New("exit status 1"))
	if scan.TraversalStatus != domain.TraversalFailed {
		t.Fatalf("process error must be FAILED, got %s", scan.TraversalStatus)
	}
	if len(scan.ErrorSummary) == 0 {
		t.Fatal("failure must carry an error summary")
	}
	scan2 := Normalize([]byte("not json"), nil)
	if scan2.TraversalStatus != domain.TraversalFailed {
		t.Fatalf("unparseable output must be FAILED, got %s", scan2.TraversalStatus)
	}
}

func TestAdapterAgainstLocalRcloneIfPresent(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone binary not installed; skipping live adapter test")
	}
	// Live remote configuration is deployment-specific; the normalized contract is
	// already covered by Normalize tests above.
	t.Skip("no rclone test remote configured")
}
