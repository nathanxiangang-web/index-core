package postgres_test

import (
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

func TestP10ListDueRetryWorkAllowlist(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()

	seed := func(scope string, class state.ErrorClass, retryAt time.Time) {
		t.Helper()
		if _, err := st.MergeSignal(ctx, p4Signal(p3RootActive, scope, state.PriorityNormal,
			state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
			t.Fatalf("merge %s: %v", scope, err)
		}
		claimed, err := p3Claim(t, st, ctx, p3RootActive, scope, now)
		if err != nil {
			t.Fatalf("claim %s: %v", scope, err)
		}
		if _, err := st.CompleteFailure(ctx, p3RootActive, scope, *claimed.ClaimedSignalSeq, class, &retryAt, now); err != nil {
			t.Fatalf("fail %s: %v", scope, err)
		}
	}

	due := now.Add(time.Second)
	future := now.Add(time.Hour)
	seed("/transient", state.ErrorTransientProvider, due)
	seed("/throttled", state.ErrorThrottled, due)
	seed("/internal", state.ErrorInternal, due)
	seed("/auth", state.ErrorAuthOrPermission, due)
	seed("/future", state.ErrorTransientProvider, future)

	got, err := st.ListDueRetryWork(ctx, now.Add(2*time.Second), 10)
	if err != nil {
		t.Fatalf("list due retry work: %v", err)
	}
	scopes := map[string]bool{}
	for _, w := range got {
		scopes[w.ScopeKey] = true
	}
	if !scopes["/transient"] || !scopes["/throttled"] {
		t.Fatalf("allowlisted classes must be returned, got %v", scopes)
	}
	for _, bad := range []string{"/internal", "/auth", "/future"} {
		if scopes[bad] {
			t.Fatalf("%s must not be returned, got %v", bad, scopes)
		}
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}

	// Bounded by limit.
	limited, err := st.ListDueRetryWork(ctx, now.Add(2*time.Second), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Fatalf("limit=1 rows = %d, want 1", len(limited))
	}

	// Nothing is due yet before the retry barrier.
	early, err := st.ListDueRetryWork(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(early) != 0 {
		t.Fatalf("rows before due = %d, want 0", len(early))
	}
}

func TestP10ListInflightRoots(t *testing.T) {
	st, ctx := p3Store(t)
	now := p3Time()
	p4AddActiveRoot(t, st, ctx, p4RootActive2)

	for _, root := range []string{p3RootActive, p4RootActive2} {
		if _, err := st.MergeSignal(ctx, p4Signal(root, "/a", state.PriorityNormal,
			state.SourceManualOperator, state.ReasonManualVerify, now, nil)); err != nil {
			t.Fatalf("merge %s: %v", root, err)
		}
		if _, err := p3Claim(t, st, ctx, root, "/a", now); err != nil {
			t.Fatalf("claim %s: %v", root, err)
		}
	}

	got, err := st.ListInflightRoots(ctx, 10)
	if err != nil {
		t.Fatalf("list inflight roots: %v", err)
	}
	if len(got) != 2 || got[0] != p3RootActive || got[1] != p4RootActive2 {
		t.Fatalf("inflight roots = %v, want sorted [%s %s]", got, p3RootActive, p4RootActive2)
	}

	one, err := st.ListInflightRoots(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0] != p3RootActive {
		t.Fatalf("limit=1 roots = %v, want [%s]", one, p3RootActive)
	}
}
