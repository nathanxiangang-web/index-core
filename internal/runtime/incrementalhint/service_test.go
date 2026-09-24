package incrementalhint_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/incrementalhint"
)

var p8Now = time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)

type fakeStore struct {
	calls int
	last  state.DirtySignal
	err   error
}

func (f *fakeStore) MergeSignal(_ context.Context, sig state.DirtySignal) (state.DirtyScopeWork, error) {
	f.calls++
	f.last = sig
	if f.err != nil {
		return state.DirtyScopeWork{}, f.err
	}
	return state.DirtyScopeWork{RootID: sig.RootID, ScopeKey: sig.ScopeKey}, nil
}

func p8Service(t *testing.T, st incrementalhint.Store) *incrementalhint.Service {
	t.Helper()
	s, err := incrementalhint.New(st, func() time.Time { return p8Now })
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return s
}

func TestP8NewRequiresStore(t *testing.T) {
	if _, err := incrementalhint.New(nil, nil); err == nil {
		t.Fatal("nil store must be rejected")
	}
}

func TestP8RejectsMalformedScopeBeforeStore(t *testing.T) {
	for _, scope := range []string{
		"", "a", "/a/", "//a", "/a/../b", "/a/./b", `\`, "/a\\b", " /a", "/a//b",
	} {
		st := &fakeStore{}
		_, err := p8Service(t, st).IngestOne(context.Background(), incrementalhint.Request{RootID: "r", ScopeKey: scope})
		if err == nil {
			t.Fatalf("scope %q must be rejected", scope)
		}
		if st.calls != 0 {
			t.Fatalf("scope %q must be rejected before Store, got %d calls", scope, st.calls)
		}
	}
}

func TestP8AcceptsCanonicalScopes(t *testing.T) {
	for _, scope := range []string{"/", "/a", "/a/b"} {
		st := &fakeStore{}
		if _, err := p8Service(t, st).IngestOne(context.Background(), incrementalhint.Request{RootID: "r", ScopeKey: scope}); err != nil {
			t.Fatalf("scope %q must be accepted: %v", scope, err)
		}
		if st.calls != 1 {
			t.Fatalf("scope %q must call Store exactly once, got %d", scope, st.calls)
		}
	}
}

func TestP8RejectsNonHintReasonsBeforeStore(t *testing.T) {
	for _, r := range []state.TriggerReason{
		state.ReasonManualVerify, state.ReasonDriftVerify, state.ReasonRetry, "BOGUS",
	} {
		st := &fakeStore{}
		_, err := p8Service(t, st).IngestOne(context.Background(),
			incrementalhint.Request{RootID: "r", ScopeKey: "/", Reason: r})
		if err == nil {
			t.Fatalf("reason %q must be rejected", r)
		}
		if st.calls != 0 {
			t.Fatalf("reason %q must be rejected before Store", r)
		}
	}
}

func TestP8EmptyReasonDefaultsToPossibleChange(t *testing.T) {
	st := &fakeStore{}
	if _, err := p8Service(t, st).IngestOne(context.Background(),
		incrementalhint.Request{RootID: "r", ScopeKey: "/"}); err != nil {
		t.Fatal(err)
	}
	if st.last.Reason != state.ReasonPossibleChange {
		t.Fatalf("reason = %q, want POSSIBLE_CHANGE", st.last.Reason)
	}
}

func TestP8RejectsEmptyRootID(t *testing.T) {
	st := &fakeStore{}
	if _, err := p8Service(t, st).IngestOne(context.Background(),
		incrementalhint.Request{ScopeKey: "/"}); err == nil {
		t.Fatal("empty root_id must be rejected")
	}
	if st.calls != 0 {
		t.Fatal("empty root_id must be rejected before Store")
	}
}

func TestP8SignalMapping(t *testing.T) {
	for _, r := range []state.TriggerReason{
		state.ReasonPossibleChange, state.ReasonDeleteHint,
		state.ReasonMoveUncertain, state.ReasonMetadataUncertain,
	} {
		st := &fakeStore{}
		if _, err := p8Service(t, st).IngestOne(context.Background(),
			incrementalhint.Request{RootID: "root-1", ScopeKey: "/a/b", Reason: r}); err != nil {
			t.Fatalf("%s: %v", r, err)
		}
		if st.calls != 1 {
			t.Fatalf("%s: exactly one MergeSignal required, got %d", r, st.calls)
		}
		sig := st.last
		if sig.RootID != "root-1" || sig.ScopeKey != "/a/b" {
			t.Fatalf("%s: unexpected identity %+v", r, sig)
		}
		if sig.Source != state.SourceMutationHint {
			t.Fatalf("%s: source = %q, want MUTATION_HINT", r, sig.Source)
		}
		if sig.Priority != state.PriorityHigh {
			t.Fatalf("%s: priority = %q, want HIGH", r, sig.Priority)
		}
		if sig.Reason != r {
			t.Fatalf("%s: reason = %q", r, sig.Reason)
		}
		if !sig.SeenAt.Equal(p8Now) {
			t.Fatalf("%s: seen_at = %v, want %v", r, sig.SeenAt, p8Now)
		}
		if sig.NotBefore == nil || !sig.NotBefore.Equal(p8Now) {
			t.Fatalf("%s: not_before = %v, want %v", r, sig.NotBefore, p8Now)
		}
	}
}

func TestP8StoreErrorPropagates(t *testing.T) {
	st := &fakeStore{err: errors.New("db down")}
	if _, err := p8Service(t, st).IngestOne(context.Background(),
		incrementalhint.Request{RootID: "r", ScopeKey: "/"}); err == nil {
		t.Fatal("Store error must propagate")
	}
	if st.calls != 1 {
		t.Fatalf("Store must be called exactly once, got %d", st.calls)
	}
}

// TestP8ProductionPackageHasNoProviderDependencies proves the ingress package
// performs no provider traversal or execution: it must not import the collector,
// scan, executor, orchestrator, or transport packages.
func TestP8ProductionPackageHasNoProviderDependencies(t *testing.T) {
	b, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, forbidden := range []string{
		"internal/collector",
		"internal/runtime/scan",
		"internal/runtime/incrementalexec",
		"internal/runtime/incrementalorch",
		"internal/transport",
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("ingress package must not depend on %s", forbidden)
		}
	}
}
