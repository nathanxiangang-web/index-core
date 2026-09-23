package reconcile

import (
	"path"
	"strings"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// IdentityResolution mirrors the Gate 1B Domain identity outcomes.
type IdentityResolution string

const (
	ResolutionMatched     IdentityResolution = "MATCHED"
	ResolutionNewResource IdentityResolution = "NEW_RESOURCE"
	ResolutionUnresolved  IdentityResolution = "UNRESOLVED"
	ResolutionConflict    IdentityResolution = "CONFLICT"
)

// entryHashComparable reports whether the current entry carries a comparable
// content hash (hash + algorithm).
func entryHashComparable(e domain.SnapshotEntry) bool {
	return e.ContentHash != nil && *e.ContentHash != "" && e.HashAlgorithm != nil && *e.HashAlgorithm != ""
}

func hashAgrees(a, b *string, aAlg, bAlg *string) bool {
	if a == nil || b == nil || *a == "" || *b == "" {
		return false
	}
	return *a == *b && derefStr(aAlg) == derefStr(bAlg)
}

// resolveIdentity implements the FROZEN Gate 1B Stable Identity v1 rules for the
// scenarios the PoC exercises (R1, R3, R4, R5 overlay, R8, R10, R11).
//
// R2-1: R1 fires only when the CURRENT entry declares STABLE_WITHIN_SCOPE; an
// unverified/unstable current id never qualifies as STRONG.
// R2-2: a present strong fingerprint contradiction must not fall through to the
// hash-absent size+mtime move fallback.
func resolveIdentity(entry domain.SnapshotEntry, prior []PriorResource, cfg Config, now time.Time) (IdentityResolution, *PriorResource, string) {
	entryPath := EntryPath(entry)

	// R1: STABLE_WITHIN_SCOPE provider_object_id on BOTH sides.
	entryStable := entry.ProviderIdentityAssurance != nil &&
		*entry.ProviderIdentityAssurance == domain.IdentityStableWithinScope
	if entryStable && entry.ProviderObjectID != nil && entry.ProviderObjectIDScope != nil {
		var matches []*PriorResource
		for i := range prior {
			p := &prior[i]
			if p.isRemoved() {
				continue // B1.4: a REMOVED tombstone is never re-matched in place.
			}
			if p.ProviderIDAssurance == domain.IdentityStableWithinScope &&
				derefStr(p.ProviderObjectID) == *entry.ProviderObjectID &&
				derefStr(p.ProviderObjectIDScope) == *entry.ProviderObjectIDScope {
				matches = append(matches, p)
			}
		}
		switch len(matches) {
		case 1:
			return ResolutionMatched, matches[0], "R1 provider_object_id"
		case 0:
			for i := range prior {
				p := &prior[i]
				if p.isPresent() && p.canonicalPath() == entryPath && p.ProviderObjectID != nil &&
					derefStr(p.ProviderObjectID) != *entry.ProviderObjectID {
					return ResolutionUnresolved, nil, "R10 provider_object_id changed at a continuous path"
				}
			}
		default:
			return ResolutionConflict, nil, "R1 multiple provider_object_id matches"
		}
	}

	// R3: content_hash + hash_algorithm with continuity context.
	entryHasHash := entryHashComparable(entry)
	if entryHasHash {
		var matches []*PriorResource
		for i := range prior {
			p := &prior[i]
			if p.isRemoved() {
				continue
			}
			if hashAgrees(p.ContentHash, entry.ContentHash, p.HashAlgorithm, entry.HashAlgorithm) {
				matches = append(matches, p)
			}
		}
		switch len(matches) {
		case 1:
			c := matches[0]
			if c.canonicalPath() == entryPath {
				return ResolutionMatched, c, "R3 same-path content_hash"
			}
			if c.hasMissingEvidence() {
				if withinMoveHorizon(c, cfg, now) {
					return ResolutionMatched, c, "R3/R5 cross-path content_hash within horizon"
				}
				return ResolutionUnresolved, nil, "R3 cross-path content_hash outside horizon"
			}
			return ResolutionConflict, nil, "R3 identical content at two live paths"
		case 0:
			// No hash agreement anywhere.
		default:
			return ResolutionConflict, nil, "R3 multiple content_hash matches"
		}
	}

	// R4: path + size + mtime heuristic.
	var samePath []*PriorResource
	for i := range prior {
		p := &prior[i]
		if p.isPresent() && p.canonicalPath() == entryPath {
			samePath = append(samePath, p)
		}
	}
	switch len(samePath) {
	case 1:
		c := samePath[0]
		sizeMatches := equalInt64(c.Size, entry.Size) && c.Size != nil
		bothHashes := entryHasHash && c.ContentHash != nil && *c.ContentHash != ""
		hashesDiffer := bothHashes && !hashAgrees(c.ContentHash, entry.ContentHash, c.HashAlgorithm, entry.HashAlgorithm)
		mtimeBothPresent := c.Mtime != nil && entry.Mtime != nil

		if hashesDiffer {
			return ResolutionNewResource, nil, "R8 imposter (different content_hash)"
		}
		if sizeMatches && mtimeBothPresent && equalTimePtr(c.Mtime, entry.Mtime) {
			return ResolutionMatched, c, "R4 path+size+mtime"
		}
		if sizeMatches && mtimeBothPresent {
			return ResolutionUnresolved, nil, "R4/R8 same path+size, mtime differs"
		}
		return ResolutionUnresolved, nil, "R4 insufficient (size mismatch or mtime absent)"
	case 0:
		// R5 moved candidate: hash-absent degraded path only. A present strong
		// fingerprint that contradicts the candidate must NOT be overridden by
		// size+mtime (R2-2).
		var moved []*PriorResource
		for i := range prior {
			p := &prior[i]
			if !p.hasMissingEvidence() || !withinMoveHorizon(p, cfg, now) {
				continue
			}
			if entryHasHash {
				continue // hash-present entries must match by hash (R3), not size+mtime
			}
			if equalInt64(p.Size, entry.Size) && p.Size != nil &&
				p.Mtime != nil && entry.Mtime != nil && equalTimePtr(p.Mtime, entry.Mtime) {
				moved = append(moved, p)
			}
		}
		switch len(moved) {
		case 1:
			return ResolutionMatched, moved[0], "R5 move (size+mtime within horizon, hash absent)"
		case 0:
			return ResolutionNewResource, nil, "no candidate -> new resource"
		default:
			return ResolutionConflict, nil, "R7 multiple moved candidates"
		}
	default:
		return ResolutionConflict, nil, "R4 multiple PRESENT rows at path"
	}
}

// withinMoveHorizon reports whether a MISSING candidate is still inside the
// move-recognition horizon. Zero disables move recognition (Gate 1B Sec 2.4).
func withinMoveHorizon(p *PriorResource, cfg Config, now time.Time) bool {
	if cfg.MoveRecognitionHorizon <= 0 || p.MissingSince == nil {
		return false
	}
	return now.Sub(*p.MissingSince) <= cfg.MoveRecognitionHorizon
}

// EntryPath derives an entry's canonical path from its Collector-local refs.
func EntryPath(entry domain.SnapshotEntry) string {
	if entry.ParentRef == "" || entry.ParentRef == "/" {
		return "/" + entry.Name
	}
	return strings.TrimSuffix(entry.ParentRef, "/") + "/" + entry.Name
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func equalInt64(a *int64, b *int64) bool {
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func equalTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return false
	}
	// Compare at microsecond precision: PostgreSQL timestamptz stores microseconds,
	// so a round-tripped observation time differs from the Go nanosecond value.
	return a.UTC().Truncate(time.Microsecond).Equal(b.UTC().Truncate(time.Microsecond))
}

// PathDir / PathBase are small path helpers shared with the reconcile core.
func PathDir(p string) string  { return path.Dir(p) }
func PathBase(p string) string { return path.Base(p) }
