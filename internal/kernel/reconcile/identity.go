package reconcile

import (
	"path"
	"strconv"
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

// PriorIndex indexes the canonical prior so identity resolution is O(1) per
// entry instead of scanning all prior resources per entry (Gate 3 P8: avoids an
// O(N^2) hot path at 20k+ resources). Maps hold only PRESENT resources unless
// noted; REMOVED tombstones are never matched in place (B1.4).
type PriorIndex struct {
	presentByProvider map[string][]*PriorResource // key: scope\x00id (STABLE only)
	presentByHash     map[string][]*PriorResource // key: algorithm\x00hash
	presentByPath     map[string][]*PriorResource
	missingByHash     map[string][]*PriorResource // PRESENT + MISSING evidence
	missingBySize     map[string][]*PriorResource // PRESENT + MISSING evidence, key: size
}

// BuildPriorIndex indexes the prior canonical inventory for one reconcile.
func BuildPriorIndex(prior []PriorResource) *PriorIndex {
	ix := &PriorIndex{
		presentByProvider: map[string][]*PriorResource{},
		presentByHash:     map[string][]*PriorResource{},
		presentByPath:     map[string][]*PriorResource{},
		missingByHash:     map[string][]*PriorResource{},
		missingBySize:     map[string][]*PriorResource{},
	}
	for i := range prior {
		p := &prior[i]
		if p.isRemoved() {
			continue
		}
		if p.isPresent() {
			if p.ProviderIDAssurance == domain.IdentityStableWithinScope &&
				p.ProviderObjectID != nil && p.ProviderObjectIDScope != nil {
				k := *p.ProviderObjectIDScope + "\x00" + *p.ProviderObjectID
				ix.presentByProvider[k] = append(ix.presentByProvider[k], p)
			}
			if p.ContentHash != nil && *p.ContentHash != "" {
				k := derefStr(p.HashAlgorithm) + "\x00" + *p.ContentHash
				ix.presentByHash[k] = append(ix.presentByHash[k], p)
			}
			ix.presentByPath[p.canonicalPath()] = append(ix.presentByPath[p.canonicalPath()], p)

			if p.hasMissingEvidence() {
				if p.ContentHash != nil && *p.ContentHash != "" {
					k := derefStr(p.HashAlgorithm) + "\x00" + *p.ContentHash
					ix.missingByHash[k] = append(ix.missingByHash[k], p)
				}
				if p.Size != nil {
					ix.missingBySize[strconv.FormatInt(*p.Size, 10)] = append(ix.missingBySize[strconv.FormatInt(*p.Size, 10)], p)
				}
			}
		}
	}
	return ix
}

// resolve implements the FROZEN Gate 1B Stable Identity v1 rules for the
// scenarios the PoC exercises (R1, R3, R4, R5 overlay, R8, R10, R11).
func (ix *PriorIndex) resolve(entry domain.SnapshotEntry, cfg Config, now time.Time) (IdentityResolution, *PriorResource, string) {
	entryPath := EntryPath(entry)

	// R1: STABLE_WITHIN_SCOPE provider_object_id on BOTH sides.
	entryStable := entry.ProviderIdentityAssurance != nil &&
		*entry.ProviderIdentityAssurance == domain.IdentityStableWithinScope
	if entryStable && entry.ProviderObjectID != nil && entry.ProviderObjectIDScope != nil {
		matches := ix.presentByProvider[*entry.ProviderObjectIDScope+"\x00"+*entry.ProviderObjectID]
		switch len(matches) {
		case 1:
			return ResolutionMatched, matches[0], "R1 provider_object_id"
		case 0:
			for _, p := range ix.presentByPath[entryPath] {
				if p.ProviderObjectID != nil && derefStr(p.ProviderObjectID) != *entry.ProviderObjectID {
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
		matches := ix.presentByHash[derefStr(entry.HashAlgorithm)+"\x00"+*entry.ContentHash]
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
			// no hash agreement anywhere
		default:
			return ResolutionConflict, nil, "R3 multiple content_hash matches"
		}
	}

	// R4: path + size + mtime heuristic.
	samePath := ix.presentByPath[entryPath]
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
		// R5 moved candidate: hash-absent degraded path only; a present strong
		// fingerprint contradiction must not be overridden by size+mtime (R2-2).
		if entryHasHash {
			return ResolutionNewResource, nil, "no candidate -> new resource"
		}
		var moved []*PriorResource
		if entry.Size != nil {
			for _, p := range ix.missingBySize[strconv.FormatInt(*entry.Size, 10)] {
				if !withinMoveHorizon(p, cfg, now) {
					continue
				}
				if equalTimePtr(p.Mtime, entry.Mtime) && p.Mtime != nil && entry.Mtime != nil {
					moved = append(moved, p)
				}
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

func entryHashComparable(e domain.SnapshotEntry) bool {
	return e.ContentHash != nil && *e.ContentHash != "" && e.HashAlgorithm != nil && *e.HashAlgorithm != ""
}

func hashAgrees(a, b *string, aAlg, bAlg *string) bool {
	if a == nil || b == nil || *a == "" || *b == "" {
		return false
	}
	return *a == *b && derefStr(aAlg) == derefStr(bAlg)
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
	// Compare at microsecond precision: PostgreSQL timestamptz stores microseconds.
	return a.UTC().Truncate(time.Microsecond).Equal(b.UTC().Truncate(time.Microsecond))
}

// PathDir / PathBase are small path helpers shared with the reconcile core.
func PathDir(p string) string  { return path.Dir(p) }
func PathBase(p string) string { return path.Base(p) }
