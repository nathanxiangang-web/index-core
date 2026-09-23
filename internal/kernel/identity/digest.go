package identity

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// The final IO3 digest is Kernel-owned: Namespace is the canonicalization
// rule-set id and Version is its version (doc A Sec 3.8, doc E Sec 2.4.3).
const (
	DigestNamespace = "kernel.index-core/io3"
	DigestVersion   = "v2"
)

// Entry is a normalized SnapshotEntry field set that contributes to the digest.
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
// the digest. RemovalDecisionContext is a Kernel-derived canonicalization of the
// per-resource removal-decision state (first absence vs independent confirmation
// vs reappearance reset) so semantically identical raw entry sets that drive
// different removal decisions cannot collapse into one IO3 identity (R2-4).
// Wall-clock, admission, and DB timing MUST NOT appear here.
type Evidence struct {
	TraversalStatus          string
	ErrorSummaryCanonical    string
	SkippedScopesCanonical   string
	Freshness                string
	Assurance                string
	ScopeShrinkCorroboration string
	RemovalDecisionContext   string
}

// FinalDigest computes the Kernel-finalized evaluated-Snapshot IO3 identity.
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
	writeTag(h, ev.RemovalDecisionContext)

	return domain.SnapshotIdentity{
		Kind:      domain.IdentityDeterministicDigest,
		Namespace: DigestNamespace,
		Version:   DigestVersion,
		Value:     hex.EncodeToString(h.Sum(nil)),
	}
}

// lessEntry is a TOTAL canonical order over every digested entry field, so the
// digest is genuinely order-independent even for entries that tie on
// parent/name/hash (R2-10).
func lessEntry(a, b Entry) bool {
	if c := cmpStr(a.ParentRef, b.ParentRef); c != 0 {
		return c < 0
	}
	if c := cmpStr(a.Name, b.Name); c != 0 {
		return c < 0
	}
	if a.IsDir != b.IsDir {
		return !a.IsDir
	}
	if c := cmpOptInt(a.Size, b.Size); c != 0 {
		return c < 0
	}
	if c := cmpOptInt(a.MTimeUnixNano, b.MTimeUnixNano); c != 0 {
		return c < 0
	}
	if c := cmpStr(a.ContentHash, b.ContentHash); c != 0 {
		return c < 0
	}
	if c := cmpStr(a.HashAlgorithm, b.HashAlgorithm); c != 0 {
		return c < 0
	}
	if c := cmpStr(a.ContentType, b.ContentType); c != 0 {
		return c < 0
	}
	if c := cmpStr(a.ProviderObjectID, b.ProviderObjectID); c != 0 {
		return c < 0
	}
	if c := cmpStr(a.ProviderObjectIDScope, b.ProviderObjectIDScope); c != 0 {
		return c < 0
	}
	return cmpStr(a.ProviderAssurance, b.ProviderAssurance) < 0
}

func cmpStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func cmpOptInt(a, b *int64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	case *a < *b:
		return -1
	case *a > *b:
		return 1
	default:
		return 0
	}
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
