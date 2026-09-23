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

// resolveIdentity implements the FROZEN Gate 1B Stable Identity v1 rules for the
// scenarios the PoC exercises (R1, R3, R4, R5 overlay, R8, R10, R11; R9 is the
// reconcile-level disappearance rule). It never force-matches: insufficient
// evidence is first-class UNRESOLVED (R11), and a copy at a new path is
// NEW_RESOURCE rather than an auto-MATCH (R3 continuity context).
func resolveIdentity(entry domain.SnapshotEntry, prior []PriorResource, cfg Config, now time.Time) (IdentityResolution, *PriorResource, string) {
	entryPath := EntryPath(entry)

	// R1: STABLE_WITHIN_SCOPE provider_object_id is the only STRONG continuity signal.
	if entry.ProviderObjectID != nil && entry.ProviderObjectIDScope != nil {
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
			// A STABLE_WITHIN_SCOPE id is strong continuity even across paths, so a
			// different path is a recognized move/rename (path updates in place).
			return ResolutionMatched, matches[0], "R1 provider_object_id"
		case 0:
			// R10: a differing provider id at an otherwise continuous path is
			// UNRESOLVED, never a guess.
			for i := range prior {
				p := &prior[i]
				if p.isPresent() && p.canonicalPath() == entryPath && p.ProviderObjectID != nil &&
					derefStr(p.ProviderObjectID) != *entry.ProviderObjectID {
					return ResolutionUnresolved, nil, "R10 provider_object_id changed at a continuous path"
				}
			}
			// otherwise fall through to R3/R4
		default:
			return ResolutionConflict, nil, "R1 multiple provider_object_id matches"
		}
	}

	// R3: content_hash + hash_algorithm. A cross-path hash MATCH requires
	// continuity context (the candidate is MISSING within the move horizon).
	if entry.ContentHash != nil && *entry.ContentHash != "" {
		var matches []*PriorResource
		for i := range prior {
			p := &prior[i]
			if p.isRemoved() {
				continue
			}
			if derefStr(p.ContentHash) == *entry.ContentHash &&
				derefStr(p.HashAlgorithm) == derefStr(entry.HashAlgorithm) {
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
			// Two live resources with identical content at different paths.
			return ResolutionConflict, nil, "R3 identical content at two live paths"
		case 0:
			// proceed to R4
		default:
			return ResolutionConflict, nil, "R3 multiple content_hash matches"
		}
	}

	// R4: path + size + mtime heuristic (hash absent, or no hash match).
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
		bothHashes := c.ContentHash != nil && entry.ContentHash != nil
		hashesDiffer := bothHashes && *c.ContentHash != *entry.ContentHash
		mtimeBothPresent := c.Mtime != nil && entry.Mtime != nil

		if hashesDiffer {
			// R8 imposter: same path, different strong content.
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
		// R5: moved candidate — a resource with MISSING evidence within the horizon.
		var moved []*PriorResource
		for i := range prior {
			p := &prior[i]
			if !p.hasMissingEvidence() || !withinMoveHorizon(p, cfg, now) {
				continue
			}
			if equalInt64(p.Size, entry.Size) && p.Size != nil &&
				p.Mtime != nil && entry.Mtime != nil && equalTimePtr(p.Mtime, entry.Mtime) {
				moved = append(moved, p)
			}
		}
		switch len(moved) {
		case 1:
			return ResolutionMatched, moved[0], "R5 move (size+mtime within horizon)"
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
// PoC assumption (CANDIDATE): ParentRef is the parent's canonical path.
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
	return a.UTC().Equal(b.UTC())
}

// PathDir / PathBase are small path helpers shared with the reconcile core.
func PathDir(p string) string  { return path.Dir(p) }
func PathBase(p string) string { return path.Base(p) }
