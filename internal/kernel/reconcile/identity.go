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

// resolveIdentity applies a PoC subset of the frozen Gate 1B identity rules
// (R1 provider object id, R3 content hash, R4 path+size+mtime, R5/R8 move vs
// imposter, R11 unresolved fallback). It never weakens ambiguity: multiple
// candidates or contradictory strong evidence yield CONFLICT/NEW_RESOURCE, never
// a forced single winner.
//
// PoC assumption (documented as a CANDIDATE implementation choice): an entry's
// ParentRef is the parent's canonical path, so an entry's canonical_path is
// join(ParentRef, Name). This keeps matching provider-neutral.
func resolveIdentity(entry domain.SnapshotEntry, prior []PriorResource) (IdentityResolution, *PriorResource, string) {
	// R1: stable provider object id within scope.
	if entry.ProviderObjectID != nil && entry.ProviderObjectIDScope != nil {
		var matches []*PriorResource
		for i := range prior {
			p := &prior[i]
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
		default:
			return ResolutionConflict, nil, "R1 multiple provider_object_id matches"
		}
	}

	// R3: content hash match among PRESENT resources.
	if entry.ContentHash != nil && *entry.ContentHash != "" {
		var matches []*PriorResource
		for i := range prior {
			p := &prior[i]
			if p.ResourcePresence == domain.ResourcePresent && derefStr(p.ContentHash) == *entry.ContentHash {
				matches = append(matches, p)
			}
		}
		switch len(matches) {
		case 1:
			return ResolutionMatched, matches[0], "R3 content_hash"
		case 0:
		default:
			return ResolutionConflict, nil, "R3 multiple content_hash matches"
		}
	}

	entryPath := EntryPath(entry)

	// R4/R8: same-path candidate.
	var samePath []*PriorResource
	for i := range prior {
		p := &prior[i]
		if p.ResourcePresence == domain.ResourcePresent && derefStr(p.CanonicalPath) == entryPath {
			samePath = append(samePath, p)
		}
	}
	switch len(samePath) {
	case 1:
		p := samePath[0]
		if entry.ContentHash != nil && *entry.ContentHash != "" &&
			p.ContentHash != nil && *p.ContentHash != "" && *p.ContentHash != *entry.ContentHash {
			// R8 imposter: same path, different strong content -> a NEW resource
			// that does not inherit the old resource_id; the old stays PRESENT.
			return ResolutionNewResource, nil, "R8 imposter (different content_hash)"
		}
		if equalInt64(p.Size, entry.Size) && equalTimePtr(p.Mtime, entry.Mtime) && p.Size != nil && p.Mtime != nil {
			return ResolutionMatched, p, "R4 path+size+mtime"
		}
		return ResolutionUnresolved, nil, "R4 weak/incomplete attributes"
	case 0:
	default:
		return ResolutionConflict, nil, "R4 multiple PRESENT rows at path"
	}

	// R5: move/rename of a MISSING resource within the recognition horizon.
	for i := range prior {
		p := &prior[i]
		if p.ResourcePresence != domain.ResourcePresent || p.RemovalEvidenceState == domain.RemovalEvidenceNone {
			continue
		}
		if entry.ContentHash != nil && *entry.ContentHash != "" &&
			derefStr(p.ContentHash) == *entry.ContentHash {
			return ResolutionMatched, p, "R5 move (same content_hash)"
		}
		if equalInt64(p.Size, entry.Size) && equalTimePtr(p.Mtime, entry.Mtime) && p.Size != nil && p.Mtime != nil {
			return ResolutionMatched, p, "R5 move (size+mtime)"
		}
	}

	// R11: not enough evidence -> first-class UNRESOLVED (never forced match).
	return ResolutionNewResource, nil, "R11 no evidence -> new resource"
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
	return a.UTC().Equal(b.UTC())
}

// PathDir and PathBase are small path helpers shared with the reconcile core.
func PathDir(p string) string  { return path.Dir(p) }
func PathBase(p string) string { return path.Base(p) }
