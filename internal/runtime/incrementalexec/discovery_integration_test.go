package incrementalexec_test

import (
	"context"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalexec"
)

func TestDiscoveryNestedFoldersExistingReplayAndNamespace(t *testing.T) {
	st, mock, root := p4SetupRootPath(t, "/library")
	ctx := context.Background()
	scanner := p4RealScanner(st)
	mock.set("/library", p4Dir("cloud"), p4Dir("other"))
	if _, err := scanner.ScanScope(ctx, root, "/", 1000); err != nil {
		t.Fatal(err)
	}
	mock.set("/library/cloud", p4Dir("download"))
	mock.set("/library/cloud/download", p4Dir("nested"), p4File("video.mp4", 5, "aaa"))
	mock.set("/library/cloud/download/nested", p4File("readme.txt", 5, "bbb"))
	cfg := p4Config()
	cfg.DiscoveryScope = "/cloud"
	now := p4Now
	ex, err := incrementalexec.New(st, scanner, cfg, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	p4SeedPending(t, st, ctx, root, "/cloud", now, nil)
	before := mock.refreshCount()
	for i := 0; i < 3; i++ {
		if _, err := ex.ExecuteOne(ctx); err != nil {
			t.Fatal(err)
		}
		if mock.refreshCount() != before+i+1 {
			t.Fatal("more than one refresh per claim")
		}
	}
	p4AssertPresent(t, st, "/library/cloud/download/video.mp4")
	p4AssertPresent(t, st, "/library/cloud/download/nested/readme.txt")
	if _, err := ex.ExecuteOne(ctx); err != incrementalexec.ErrNoEligibleWork {
		t.Fatalf("unbounded work: %v", err)
	}
	// A repeated parent hint must revisit existing directories, not just new IDs.
	// A partial empty child must never remove earlier positive observations.
	mock.set("/library/cloud/download/nested")
	now = now.Add(time.Minute)
	p4SeedPending(t, st, ctx, root, "/cloud", now, nil)
	for i := 0; i < 3; i++ {
		if _, err := ex.ExecuteOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	p4AssertPresent(t, st, "/library/cloud/download/nested/readme.txt")
	p4AssertNoRemovalEvidence(t, st, "/library/cloud/download/nested/readme.txt")
	mock.set("/library/other", p4Dir("not-expanded"))
	p4SeedPending(t, st, ctx, root, "/other", now, nil)
	if _, err := ex.ExecuteOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.ExecuteOne(ctx); err != incrementalexec.ErrNoEligibleWork {
		t.Fatalf("escaped configured subtree: %v", err)
	}
	// All observed work terminates as VERIFIED, using the existing state machine.
	var count int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM index_dirty_scope_work WHERE work_state<>$1`, state.WorkVerified).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unverified: %d %v", count, err)
	}
}
