package rclone

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	if scan.Assurance != domain.WeakFailureVisibility {
		t.Fatalf("default failure visibility must be WEAK (safe), got %s", scan.Assurance)
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

// A successful scan never yields COMPLETE regardless of qualified assurance,
// because skipped_scopes stays UNKNOWN (ADR-001, doc E Sec 7.1).
func TestRcloneCannotClaimComplete(t *testing.T) {
	for _, ass := range []domain.FailureVisibility{domain.WeakFailureVisibility, domain.StrongFailureVisibility} {
		scan := NormalizeWithAssurance([]byte(sampleLSJSON), nil, ass)
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
			t.Fatalf("rclone UNKNOWN skip evidence must never yield COMPLETE (assurance=%s), got %s", ass, state)
		}
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

// TestAdapterRealProcessLocalBackend exercises Adapter.Scan across a real process
// boundary using rclone's implicit local backend against a deterministic temp dir
// (B7). It skips only if the rclone binary is unavailable.
func TestAdapterRealProcessLocalBackend(t *testing.T) {
	bin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone binary not installed; skipping live process-boundary test")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("nested"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}

	ad := Adapter{Binary: bin}
	ctx := context.Background()
	scan, err := ad.Scan(ctx, dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.TraversalStatus != domain.TraversalSuccess {
		t.Fatalf("local scan must succeed, got %s (%s)", scan.TraversalStatus, scan.ErrorSummary)
	}
	if len(scan.Entries) < 2 {
		t.Fatalf("expected >=2 entries from local backend, got %d", len(scan.Entries))
	}
	if scan.SkippedKnownEmpty {
		t.Fatal("skip evidence must remain UNKNOWN for rclone")
	}
	// A missing path must surface a process failure, not a silent success.
	bad, err := ad.Scan(ctx, filepath.Join(dir, "does-not-exist"))
	if err != nil {
		t.Fatalf("scan missing path returned error: %v", err)
	}
	if bad.TraversalStatus != domain.TraversalFailed {
		t.Fatalf("missing path must be FAILED, got %s", bad.TraversalStatus)
	}
}
