package identity

import "testing"

// R2-10: the entry sort must be a TOTAL canonical order. Two entries that tie on
// parent/name/content_hash but differ in a later digested field must still order
// deterministically, so the digest is order-independent.
func TestDigestTotalOrderForTiedCoordinates(t *testing.T) {
	ev := Evidence{TraversalStatus: "SUCCESS", SkippedScopesCanonical: "CONFIRMED_EMPTY",
		Freshness: "FRESH_DIRECT", Assurance: "STRONG_FAILURE_VISIBILITY", ScopeShrinkCorroboration: "NONE"}

	a := Entry{ParentRef: "/", Name: "a.txt", ContentHash: "h", HashAlgorithm: "sha256", ProviderObjectID: "obj-1"}
	b := Entry{ParentRef: "/", Name: "a.txt", ContentHash: "h", HashAlgorithm: "sha256", ProviderObjectID: "obj-2"}

	ab := FinalDigest([]Entry{a, b}, ev)
	ba := FinalDigest([]Entry{b, a}, ev)
	if !ab.Equal(ba) {
		t.Fatalf("digest must be order-independent for tied coordinates: %s vs %s", ab.Value, ba.Value)
	}
}
