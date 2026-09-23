package identity

import "testing"

// B4: every decision-relevant field must change the digest so two evaluated
// snapshots that can drive different reconcile decisions cannot collapse into a
// false IO3 NOOP.
func TestDigestDistinguishesDecisionRelevantFields(t *testing.T) {
	size := int64(7)
	base := []Entry{{
		ParentRef: "/", Name: "a.txt", Size: &size,
		ContentHash: "abc", HashAlgorithm: "sha256", ContentType: "text/plain",
		ProviderObjectID: "obj-1", ProviderObjectIDScope: "root", ProviderAssurance: "STABLE_WITHIN_SCOPE",
	}}
	ev := Evidence{TraversalStatus: "SUCCESS", SkippedScopesCanonical: "CONFIRMED_EMPTY",
		Freshness: "FRESH_DIRECT", Assurance: "STRONG_FAILURE_VISIBILITY", ScopeShrinkCorroboration: "NONE"}
	baseID := FinalDigest(base, ev)

	mutate := func(f func(e *Entry)) Entry {
		e := base[0]
		f(&e)
		return e
	}
	cases := map[string]Entry{
		"hash_algorithm":     mutate(func(e *Entry) { e.HashAlgorithm = "md5" }),
		"content_type":       mutate(func(e *Entry) { e.ContentType = "application/octet-stream" }),
		"provider_assurance": mutate(func(e *Entry) { e.ProviderAssurance = "UNVERIFIED" }),
		"provider_object_id": mutate(func(e *Entry) { e.ProviderObjectID = "obj-2" }),
	}
	for name, e := range cases {
		if got := FinalDigest([]Entry{e}, ev); got.Equal(baseID) {
			t.Fatalf("changing %s must change the IO3 digest", name)
		}
	}
}
