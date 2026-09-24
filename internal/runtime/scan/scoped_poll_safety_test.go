package scan_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/scan"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// TestPollHarnessReconcileSafety drives two bounded poll cycles through the
// harness on real PostgreSQL + a deterministic AList mock, and proves the P1
// safety contract (Issue #66 §9/§12): one changed hot scope adds exactly its
// positive change; unchanged scopes are NOOP; unrelated resources remain; no
// removal evidence; same-root FIFO intact.
func TestPollHarnessReconcileSafety(t *testing.T) {
	st, mock, rootID := setupScopedRoot(t)
	ctx := context.Background()
	svc := scan.New(st, "", "", 10*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	qr := postgres.NewQueryReader(st.Pool())

	// Hot scopes: "/" (root), "/a", "/b". "/" must expose the child directories so
	// the non-root scope parent guard is satisfied.
	mock.set("/", fileEntry("root.txt", 1, "r"), dirEntry("a"), dirEntry("b"))
	mock.set("/a", fileEntry("a1.txt", 2, "a"))
	mock.set("/b", fileEntry("b1.txt", 3, "b"))

	clk := &testClock{t: time.Now()}
	h := newPollHarness(defaultPollLimits(), clk.now, func(ctx context.Context, scope string) (scan.Result, error) {
		return svc.ScanScope(ctx, rootID, scope, 100)
	})
	for _, p := range []string{"/", "/a", "/b"} {
		if !h.addScope(p, cadenceHot) {
			t.Fatalf("add hot scope %s", p)
		}
	}

	// Phase A: baseline cycle for all hot scopes.
	r0 := h.runCycle(ctx)
	if len(r0.results) != 3 {
		t.Fatalf("baseline cycle must poll all 3 hot scopes, got %v", r0.polledPaths())
	}
	base := activePaths(t, qr, rootID)
	for _, p := range []string{"/root.txt", "/a", "/b", "/a/a1.txt", "/b/b1.txt"} {
		if !base[p] {
			t.Fatalf("baseline must contain %s, got %v", p, base)
		}
	}
	baseCount := len(base)

	// T1 (simulated out-of-band write): a new file appears in /a only.
	mock.set("/a", fileEntry("a1.txt", 2, "a"), fileEntry("a2.txt", 4, "a2"))

	// Next cycle (after the minimum interval).
	clk.advance(121 * time.Second)
	r1 := h.runCycle(ctx)
	if len(r1.results) != 3 {
		t.Fatalf("second cycle must poll all 3 due scopes, got %v", r1.polledPaths())
	}

	after := activePaths(t, qr, rootID)
	if !after["/a/a2.txt"] {
		t.Fatalf("changed scope must add /a/a2.txt, got %v", after)
	}
	if len(after) != baseCount+1 {
		t.Fatalf("exactly one resource must be added, before=%d after=%d", baseCount, len(after))
	}
	for _, p := range []string{"/root.txt", "/a/a1.txt", "/b/b1.txt", "/a", "/b"} {
		if !after[p] {
			t.Fatalf("unchanged resource %s must remain, got %v", p, after)
		}
	}

	byPath := map[string]string{}
	byMutated := map[string]bool{}
	for _, res := range r1.results {
		byPath[res.path] = res.outcome
		byMutated[res.path] = res.mutated
	}
	t.Logf("cycle0 results: %+v", r0.results)
	t.Logf("cycle1 results: %+v", r1.results)

	// Additive-safe: unchanged hot scopes must not mutate Canonical state. Across
	// a generation change the Kernel records the application (Status=APPLIED)
	// while Mutated stays false; only the changed scope mutates.
	if byMutated["/"] || byMutated["/b"] {
		t.Fatalf("unchanged hot scopes must not mutate canonical state, got mutated=%v", byMutated)
	}
	if byPath["/a"] != string(domain.AdmissionApplied) || !byMutated["/a"] {
		t.Fatalf("changed hot scope must APPLY with mutation, got outcome=%v mutated=%v", byPath, byMutated)
	}

	assertRemovalEvidenceClean(t, st, rootID, "/root.txt")
	assertRemovalEvidenceClean(t, st, rootID, "/b/b1.txt")
	assertRemovalEvidenceClean(t, st, rootID, "/a/a1.txt")
	removed, err := qr.ListRemovedPage(ctx, rootID, nil, 100)
	if err != nil {
		t.Fatalf("Q7 removed page: %v", err)
	}
	if len(removed.Items) != 0 {
		t.Fatalf("polling must not produce removal evidence, got %d", len(removed.Items))
	}

	seqs := admissionSeqs(t, st, rootID)
	if len(seqs) != 6 {
		t.Fatalf("expected 6 admissions (2 cycles x 3 scopes), got %v", seqs)
	}
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("same-root FIFO admission_seq must be 1..N, got %v", seqs)
		}
	}
}
