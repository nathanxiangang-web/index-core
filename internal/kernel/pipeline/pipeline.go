// Package pipeline is a thin Kernel coordinator that wires the frozen P3 stages
// in the correct ownership order for one submitted Snapshot:
//
//	Snapshot evidence/entries -> completeness evaluation -> Kernel-owned final
//	DETERMINISTIC_DIGEST -> IO3 identity -> (caller) reconcile
//
// The final IO3 identity is computed here, after evaluation, and is never
// supplied by the adapter (doc A Sec 3.8, doc E Sec 2.4.3).
package pipeline

import (
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/completeness"
	"github.com/nathanxiangang-web/index-core/internal/kernel/identity"
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
	// IndependentCorroboration is the Kernel-derived significance corroboration
	// from later independent admitted observations (NONE otherwise).
	IndependentCorroboration *domain.ScopeShrinkCorroboration
}

// Evaluate runs completeness evaluation and final-digest identity computation.
func Evaluate(entries []domain.SnapshotEntry, ev Evidence) Result {
	skippedUnknown := ev.SkippedScopes == nil && !ev.SkippedKnownEmpty
	corroboration := domain.ShrinkNone
	if ev.IndependentCorroboration != nil {
		corroboration = *ev.IndependentCorroboration
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
		EntryCount:        ev.EntryCount,
		PriorPresent:      ev.PriorPresent,
		Corroboration:     ev.IndependentCorroboration,
	}, completeness.Config{})

	id := identity.FinalDigest(toDigestEntries(entries), identity.Evidence{
		TraversalStatus:          string(ev.TraversalStatus),
		ErrorSummaryCanonical:    errorCanonical(ev.HasErrorSummary),
		SkippedScopesCanonical:   skippedCanonical(ev),
		Freshness:                string(normalizeFreshness(ev.Freshness)),
		Assurance:                string(normalizeAssurance(ev.Assurance)),
		ScopeShrinkCorroboration: string(corroboration),
	})

	return Result{Acceptance: acceptance, Identity: id, Corroboration: corroboration}
}

func toDigestEntries(entries []domain.SnapshotEntry) []identity.Entry {
	out := make([]identity.Entry, 0, len(entries))
	for _, e := range entries {
		de := identity.Entry{
			ParentRef: e.ParentRef,
			Name:      e.Name,
			IsDir:     e.IsDir,
			Size:      e.Size,
		}
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
