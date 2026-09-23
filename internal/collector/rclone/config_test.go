package rclone_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/collector/rclone"
	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// G3-R6: a configured rclone config path must actually be passed to the rclone
// process. We prove it with a named remote that only exists in our config file.
func TestConfiguredConfigPathIsUsed(t *testing.T) {
	bin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone binary not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "rclone.conf")
	if err := os.WriteFile(cfgPath, []byte("[mylocal]\ntype = local\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// With the config, the named remote resolves.
	ok, err := rclone.Adapter{Binary: bin, Remote: "mylocal", ConfigPath: cfgPath, Timeout: 30 * time.Second}.Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if ok.TraversalStatus != domain.TraversalSuccess || len(ok.Entries) == 0 {
		t.Fatalf("named remote with config must succeed, got %s (%s)", ok.TraversalStatus, ok.ErrorSummary)
	}

	// Without the config (empty file), the named remote is unknown -> FAILED.
	emptyCfg := filepath.Join(t.TempDir(), "empty.conf")
	if err := os.WriteFile(emptyCfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bad, err := rclone.Adapter{Binary: bin, Remote: "mylocal", ConfigPath: emptyCfg, Timeout: 30 * time.Second}.Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if bad.TraversalStatus != domain.TraversalFailed {
		t.Fatalf("unknown remote without config must FAIL, got %s", bad.TraversalStatus)
	}
}
