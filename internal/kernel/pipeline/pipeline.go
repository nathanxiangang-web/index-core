// Package pipeline is a thin Kernel coordinator that wires the frozen P3 stages
// in the correct ownership order for one submitted Snapshot:
//
//	SUBMITTED -> Stage-1 admission_seq -> Kernel evaluation -> final identity ->
//	Stage-2 reconcile
//
// The final IO3 identity is computed after evaluation and includes Kernel-derived
// decision evidence (scope-shrink corroboration and the removal-decision context)
// so semantically identical raw entry sets that drive different reconcile
// decisions cannot collapse into one identity (doc A Sec 3.8, R2-4/R2-6).
package pipeline

import (
	"sort"
	"strings"

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
	// Corroboration is the Kernel-derived significance corroboration from later
	// independent admitted observations. It must be derived by the Kernel before
	// calling Evaluate, never supplied by an adapter/caller verdict (R2-6).
	Corroboration domain.ScopeShrinkCorroboration
}

// Evaluate runs completeness evaluation and final-digest identity computation.
// prior is the canonical inventory enriched with identity evidence; it is needed
// to derive the removal-decision context in the digest.
func Evaluate(entries []domain.SnapshotEntry, prior []reconcile.PriorResource, ev Evidence) Result {
	skippedUnknown := ev.SkippedScopes == nil && !ev.SkippedKnownEmpty
	corroboration := ev.Corroboration
	if corroboration == "" {
		corroboration = domain.ShrinkNone
	}

	// entry_count is derived from the normalized entries, never trusted from
	// metadata (R2-10).
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
		RemovalDecisionContext:   RemovalDecisionContext(entries, prior),
	})

	return Result{Acceptance: acceptance, Identity: id, Corroboration: corroboration}
}

// RemovalDecisionContext canonicalizes the Kernel-owned removal-decision state of
// prior resources absent from the current entries. It includes only decision
// booleans/states (never snapshot_id, admission_seq, or timing), so a first
// absence and a later independent confirmation of the same raw entry set produce
// different evaluated identities (R2-4).
func RemovalDecisionContext(entries []domain.SnapshotEntry, prior []reconcile.PriorResource) string {
	observed := make(map[string]bool, len(entries))
	for _, e := range entries {
		observed[reconcile.EntryPath(e)] = true
	}
	var lines []string
	for i := range prior {
		p := &prior[i]
		if p.ResourcePresence != domain.ResourcePresent {
			continue
		}
		if observed[deref(p.CanonicalPath)] {
			continue
		}
		firstSet := "0"
		if p.MissingFirstSnapshotID != nil && *p.MissingFirstSnapshotID != "" {
			firstSet = "1"
		}
		lines = append(lines, strings.Join([]string{
			"absent",
			"rid=" + p.ResourceID,
			"state=" + string(p.RemovalEvidenceState),
			"consec=" + itoa(int(p.ConsecutiveCompleteMissing)),
			"first=" + firstSet,
		}, "|"))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
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

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
