package identity

import (
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

func sampleEntries() []Entry {
	size := int64(12)
	return []Entry{
		{ParentRef: "root", Name: "a.txt", Size: &size, ContentHash: "h1"},
		{ParentRef: "root", Name: "dir", IsDir: true},
	}
}

func TestDigestIsOrderIndependentAndTimingFree(t *testing.T) {
	ev := Evidence{
		TraversalStatus: "SUCCESS", SkippedScopesCanonical: "CONFIRMED_EMPTY",
		Freshness: "FRESH_DIRECT", Assurance: "STRONG_FAILURE_VISIBILITY",
		ScopeShrinkCorroboration: "NONE",
	}
	a := FinalDigest(sampleEntries(), ev)
	shuffled := sampleEntries()
	shuffled[0], shuffled[1] = shuffled[1], shuffled[0]
	b := FinalDigest(shuffled, ev)

	if !a.Equal(b) {
		t.Fatalf("entry order must not affect the digest: %s vs %s", a.Value, b.Value)
	}
	if a.Kind != domain.IdentityDeterministicDigest || a.Namespace != DigestNamespace || a.Version != DigestVersion {
		t.Fatalf("digest identity must be the Kernel-owned DETERMINISTIC_DIGEST tuple, got %+v", a)
	}
	// observed_at / admission / DB timing are not inputs, so recomputation is stable.
	if c := FinalDigest(sampleEntries(), ev); !a.Equal(c) {
		t.Fatalf("digest must be deterministic across recomputation")
	}
}

func TestDigestDistinguishesCorroborationAndAssurance(t *testing.T) {
	base := Evidence{
		TraversalStatus: "SUCCESS", SkippedScopesCanonical: "CONFIRMED_EMPTY",
		Freshness: "FRESH_DIRECT", Assurance: "STRONG_FAILURE_VISIBILITY",
		ScopeShrinkCorroboration: "NONE",
	}
	none := FinalDigest(sampleEntries(), base)

	corr := base
	corr.ScopeShrinkCorroboration = "CORROBORATED"
	if got := FinalDigest(sampleEntries(), corr); got.Equal(none) {
		t.Fatal("NONE vs CORROBORATED must yield different IO3 identities (EC13)")
	}

	weak := base
	weak.Assurance = "WEAK_FAILURE_VISIBILITY"
	if got := FinalDigest(sampleEntries(), weak); got.Equal(none) {
		t.Fatal("different failure visibility must yield different IO3 identities (EC10)")
	}

	unknownSkips := base
	unknownSkips.SkippedScopesCanonical = "UNKNOWN"
	if got := FinalDigest(sampleEntries(), unknownSkips); got.Equal(none) {
		t.Fatal("UNKNOWN vs confirmed-empty skips must yield different IO3 identities")
	}
}
