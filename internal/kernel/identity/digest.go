package identity

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// The final IO3 digest is Kernel-owned: Namespace is the canonicalization
// rule-set id and Version is its version. They are NOT adapter-declared
// (doc A Sec 3.8, doc E Sec 2.4.3).
const (
	DigestNamespace = "kernel.index-core/io3"
	DigestVersion   = "v1"
)

// Entry is a normalized SnapshotEntry field set that contributes to the digest.
// Every field that can alter a reconcile decision is included (B4): hash and
// hash_algorithm (R3 compares both), provider object id/scope and identity
// assurance (R1 eligibility), and content_type (UPDATE semantics).
type Entry struct {
	ParentRef             string
	Name                  string
	IsDir                 bool
	Size                  *int64
	MTimeUnixNano         *int64
	ContentHash           string
	HashAlgorithm         string
	ContentType           string
	ProviderObjectID      string
	ProviderObjectIDScope string
	ProviderAssurance     string
}

// Evidence is the normalized, reconcile-decision-relevant evidence included in
// the digest. Wall-clock, admission timing and DB timing MUST NOT appear here.
type Evidence struct {
	TraversalStatus          string
	ErrorSummaryCanonical    string
	SkippedScopesCanonical   string // "UNKNOWN" | "CONFIRMED_EMPTY" | canonical sorted list
	Freshness                string
	Assurance                string
	ScopeShrinkCorroboration string // NONE | CORROBORATED | CONTRADICTED
}

// FinalDigest computes the Kernel-finalized evaluated-Snapshot IO3 identity as a
// DETERMINISTIC_DIGEST. Two evaluated snapshots that can drive different
// reconcile decisions MUST NOT collapse into the same digest (B4).
func FinalDigest(entries []Entry, ev Evidence) domain.SnapshotIdentity {
	norm := make([]Entry, len(entries))
	copy(norm, entries)
	sort.Slice(norm, func(i, j int) bool { return lessEntry(norm[i], norm[j]) })

	h := sha256.New()
	writeTag(h, "index-core/io3")
	writeTag(h, DigestVersion)

	for _, e := range norm {
		writeTag(h, "entry")
		writeTag(h, e.ParentRef)
		writeTag(h, e.Name)
		writeBool(h, e.IsDir)
		writeOptInt(h, e.Size)
		writeOptInt(h, e.MTimeUnixNano)
		writeTag(h, e.ContentHash)
		writeTag(h, e.HashAlgorithm)
		writeTag(h, e.ContentType)
		writeTag(h, e.ProviderObjectID)
		writeTag(h, e.ProviderObjectIDScope)
		writeTag(h, e.ProviderAssurance)
	}

	writeTag(h, "evidence")
	writeTag(h, ev.TraversalStatus)
	writeTag(h, ev.ErrorSummaryCanonical)
	writeTag(h, ev.SkippedScopesCanonical)
	writeTag(h, ev.Freshness)
	writeTag(h, ev.Assurance)
	writeTag(h, ev.ScopeShrinkCorroboration)

	return domain.SnapshotIdentity{
		Kind:      domain.IdentityDeterministicDigest,
		Namespace: DigestNamespace,
		Version:   DigestVersion,
		Value:     hex.EncodeToString(h.Sum(nil)),
	}
}

func lessEntry(a, b Entry) bool {
	if a.ParentRef != b.ParentRef {
		return a.ParentRef < b.ParentRef
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ContentHash < b.ContentHash
}

func writeTag(h interface{ Write([]byte) (int, error) }, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	_, _ = h.Write(n[:])
	_, _ = h.Write([]byte(s))
}

func writeBool(h interface{ Write([]byte) (int, error) }, v bool) {
	if v {
		_, _ = h.Write([]byte{1})
		return
	}
	_, _ = h.Write([]byte{0})
}

func writeOptInt(h interface{ Write([]byte) (int, error) }, v *int64) {
	if v == nil {
		_, _ = h.Write([]byte{0})
		return
	}
	_, _ = h.Write([]byte{1})
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(*v))
	_, _ = h.Write(n[:])
}
