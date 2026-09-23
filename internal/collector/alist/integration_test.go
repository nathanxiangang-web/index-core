package alist_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// Real AList/OpenList instance integration (G3-R1). Opt-in:
//
//	INDEXCORE_ALIST_URL=http://host:5244 INDEXCORE_ALIST_USER=admin \
//	INDEXCORE_ALIST_PASS=... INDEXCORE_ALIST_PATH=/loc \
//	go test ./internal/collector/alist -run TestRealAListInstance -v
func TestRealAListInstance(t *testing.T) {
	base := os.Getenv("INDEXCORE_ALIST_URL")
	if base == "" {
		t.Skip("set INDEXCORE_ALIST_URL/INDEXCORE_ALIST_USER/INDEXCORE_ALIST_PASS/INDEXCORE_ALIST_PATH to run against a real AList/OpenList instance")
	}
	path := os.Getenv("INDEXCORE_ALIST_PATH")
	if path == "" {
		path = "/"
	}
	raw, err := alist.Adapter{
		BaseURL:  base,
		Username: os.Getenv("INDEXCORE_ALIST_USER"),
		Password: os.Getenv("INDEXCORE_ALIST_PASS"),
		Timeout:  30 * time.Second,
	}.Scan(context.Background(), path)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if raw.TraversalStatus != domain.TraversalSuccess {
		t.Fatalf("real AList scan must succeed, got %s (%s)", raw.TraversalStatus, raw.ErrorSummary)
	}
	if len(raw.Entries) == 0 {
		t.Fatal("real AList scan returned no entries")
	}
	if raw.SkippedKnownEmpty || raw.SkippedScopes != nil {
		t.Fatal("AList skip evidence must stay UNKNOWN")
	}
	if raw.Assurance != domain.WeakFailureVisibility {
		t.Fatalf("AList assurance must be WEAK, got %s", raw.Assurance)
	}
	t.Logf("real AList scan: %d entries from %s%s", len(raw.Entries), base, path)
}
