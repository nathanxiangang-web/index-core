// Package pipeline evaluates one admitted Snapshot into the Kernel-owned
// acceptance verdict and final IO3 identity. It is invoked by the safe Kernel
// coordinator under the per-root serialization domain, so the decision is
// computed against the canonical truth being reconciled.
package pipeline

import (
	"sort"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/completeness"
	"github.com/nathanxiangang-web/index-core/internal/kernel/identity"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
)

// Result is the Kernel evaluation output for one Snapshot.
type Result struct {
	Acceptance    domain.AcceptanceState
	Identity      domain.SnapshotIdentity
	Corroboration domain.ScopeShrinkCorroboration
}

// Evidence carries the normalized, reconcile-relevant Snapshot evidence.
type Evidence struct {
	TraversalStatus   domain.TraversalStatus
	SkippedScopes     []byte
	SkippedKnownEmpty bool
	HasErrorSummary   bool
	Freshness         domain.FreshnessEvidence
	Assurance         domain.FailureVisibility
	UsedCache         bool
	EntryCount        int64
	PriorPresent      int64
	// Corroboration is the Kernel-derived significance corroboration from a
	// qualifying earlier independently admitted observation (R2-6/R3-2).
	Corroboration domain.ScopeShrinkCorroboration
}

// Evaluate runs completeness evaluation and final-digest identity computation.
// prior is the canonical inventory enriched with identity evidence; cfg/now are
// needed to derive the removal-decision context with the SAME identity resolution
// reconcile uses (R3-1).
func Evaluate(entries []domain.SnapshotEntry, prior []reconcile.PriorResource, ev Evidence, cfg reconcile.Config, now time.Time) Result {
	skippedUnknown := ev.SkippedScopes == nil && !ev.SkippedKnownEmpty
	corroboration := ev.Corroboration
	if corroboration == "" {
		corroboration = domain.ShrinkNone
	}
	derivedCount := int64(len(entries))

	var corrPtr *domain.ScopeShrinkCorroboration
	if corroboration != domain.ShrinkNone {
		c := corroboration
		corrPtr = &c
	}

	acceptance := completeness.Evaluate(completeness.Evidence{
		TraversalStatus:   ev.TraversalStatus,
		HasErrorSummary:   ev.HasErrorSummary,
		HasSkippedScopes:  len(ev.SkippedScopes) > 0,
		SkippedKnownEmpty: ev.SkippedKnownEmpty,
		SkippedUnknown:    skippedUnknown,
		Freshness:         ev.Freshness,
		Assurance:         ev.Assurance,
		UsedCache:         ev.UsedCache,
		EntryCount:        derivedCount,
		PriorPresent:      ev.PriorPresent,
		Corroboration:     corrPtr,
	}, completeness.Config{})

	id := identity.FinalDigest(toDigestEntries(entries), identity.Evidence{
		TraversalStatus:          string(ev.TraversalStatus),
		ErrorSummaryCanonical:    errorCanonical(ev.HasErrorSummary),
		SkippedScopesCanonical:   skippedCanonical(ev),
		Freshness:                string(normalizeFreshness(ev.Freshness)),
		Assurance:                string(normalizeAssurance(ev.Assurance)),
		ScopeShrinkCorroboration: string(corroboration),
		RemovalDecisionContext:   reconcile.RemovalDecisionContext(prior, entries, cfg, now),
	})

	return Result{Acceptance: acceptance, Identity: id, Corroboration: corroboration}
}

func toDigestEntries(entries []domain.SnapshotEntry) []identity.Entry {
	out := make([]identity.Entry, 0, len(entries))
	for _, e := range entries {
		de := identity.Entry{ParentRef: e.ParentRef, Name: e.Name, IsDir: e.IsDir, Size: e.Size}
		if e.Mtime != nil {
			ns := e.Mtime.UnixNano()
			de.MTimeUnixNano = &ns
		}
		de.ContentHash = deref(e.ContentHash)
		de.HashAlgorithm = deref(e.HashAlgorithm)
		de.ContentType = deref(e.ContentType)
		de.ProviderObjectID = deref(e.ProviderObjectID)
		de.ProviderObjectIDScope = deref(e.ProviderObjectIDScope)
		if e.ProviderIdentityAssurance != nil {
			de.ProviderAssurance = string(*e.ProviderIdentityAssurance)
		}
		out = append(out, de)
	}
	return out
}

// RawScopeSignature is a deterministic signature of a normalized entry set used
// to prove a reduced scope was independently observed (R3-2). It is derived from
// actual entries, never from untrusted Snapshot metadata counts.
func RawScopeSignature(entries []domain.SnapshotEntry) string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.ParentRef+"\x00"+e.Name+"\x00"+deref(e.ContentHash)+"\x00"+deref(e.HashAlgorithm))
	}
	sort.Strings(lines)
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func skippedCanonical(ev Evidence) string {
	switch {
	case ev.SkippedKnownEmpty:
		return "CONFIRMED_EMPTY"
	case len(ev.SkippedScopes) > 0:
		return string(ev.SkippedScopes)
	default:
		return "UNKNOWN"
	}
}

func errorCanonical(has bool) string {
	if has {
		return "PRESENT"
	}
	return "NONE"
}

func normalizeFreshness(f domain.FreshnessEvidence) domain.FreshnessEvidence {
	if f == "" {
		return domain.FreshnessUnknown
	}
	return f
}

func normalizeAssurance(a domain.FailureVisibility) domain.FailureVisibility {
	if a == "" {
		return domain.UnknownFailureVisibility
	}
	return a
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
