package completeness

import "github.com/nathanxiangang-web/index-core/internal/domain"

// Evidence is the normalized, Kernel-facing view of a submitted Snapshot used by
// the completeness gate. It never contains provider-specific fields.
type Evidence struct {
	TraversalStatus   domain.TraversalStatus
	HasErrorSummary   bool
	HasSkippedScopes  bool // a non-empty skip list was observed
	SkippedKnownEmpty bool // positively established as empty
	SkippedUnknown    bool // skip evidence absent/unobservable
	Freshness         domain.FreshnessEvidence
	Assurance         domain.FailureVisibility
	UsedCache         bool
	EntryCount        int64
	PriorPresent      int64
	Corroboration     *domain.ScopeShrinkCorroboration
}

// Config carries Gate 1C runtime thresholds (D-DEFER-7).
type Config struct {
	// SignificantShrinkRatio is the fractional drop in entry count vs prior
	// canonical that counts as a significant shrink. Zero selects the default.
	SignificantShrinkRatio float64
}

func (c Config) ratio() float64 {
	if c.SignificantShrinkRatio <= 0 || c.SignificantShrinkRatio >= 1 {
		return 0.5
	}
	return c.SignificantShrinkRatio
}

func (e Evidence) freshness() domain.FreshnessEvidence {
	if e.Freshness == "" {
		return domain.FreshnessUnknown
	}
	return e.Freshness
}

func (e Evidence) assurance() domain.FailureVisibility {
	if e.Assurance == "" {
		return domain.UnknownFailureVisibility
	}
	return e.Assurance
}

// Evaluate derives the Kernel-owned acceptance_state using the conservative
// first-match cascade C-1..C-10 (Gate 1B Snapshot Completeness Sec 3.3). Only
// COMPLETE may authorize destructive reconcile.
func Evaluate(ev Evidence, cfg Config) domain.AcceptanceState {
	// C-1: traversal failed.
	if ev.TraversalStatus == domain.TraversalFailed {
		return domain.AcceptanceFailed
	}
	// C-2/C-3: traversal incomplete.
	if ev.TraversalStatus == domain.TraversalInterrupted || ev.TraversalStatus == domain.TraversalPartial {
		return domain.AcceptancePartial
	}
	// C-4: any reported error on a non-failed traversal.
	if ev.HasErrorSummary {
		return domain.AcceptancePartial
	}
	// C-5: any explicitly skipped scope.
	if ev.HasSkippedScopes {
		return domain.AcceptancePartial
	}
	// C-6: stale (or unknown freshness with cache use).
	if ev.freshness() == domain.FreshnessStale ||
		(ev.freshness() == domain.FreshnessUnknown && ev.UsedCache) {
		return domain.AcceptanceStale
	}
	// C-7: significant shrink not (yet) corroborated.
	if ev.TraversalStatus == domain.TraversalSuccess && ev.PriorPresent > 0 &&
		significantShrink(ev, cfg) && !corroborated(ev) {
		return domain.AcceptanceSuspicious
	}
	// C-8: empty scope where canonically non-empty.
	if ev.TraversalStatus == domain.TraversalSuccess && ev.PriorPresent > 0 && ev.EntryCount == 0 {
		return domain.AcceptanceSuspicious
	}
	// C-9: all positive, requiring confirmed-empty skips AND strong failure visibility.
	freshEnough := ev.freshness() == domain.FreshDirect ||
		ev.freshness() == domain.FreshRefreshed ||
		ev.freshness() == domain.CachedFresh
	if ev.TraversalStatus == domain.TraversalSuccess &&
		!ev.HasErrorSummary && ev.SkippedKnownEmpty && !ev.SkippedUnknown &&
		freshEnough && ev.assurance() == domain.StrongFailureVisibility &&
		!(significantShrink(ev, cfg) && !corroborated(ev)) {
		return domain.AcceptanceComplete
	}
	// C-9a: C-9 dimensions positive but weak/unknown failure visibility.
	if ev.TraversalStatus == domain.TraversalSuccess &&
		(ev.assurance() == domain.WeakFailureVisibility ||
			ev.assurance() == domain.UnknownFailureVisibility) {
		return domain.AcceptanceSuspicious
	}
	// C-10: conservative default.
	return domain.AcceptancePartial
}

func significantShrink(ev Evidence, cfg Config) bool {
	if ev.PriorPresent <= 0 {
		return false
	}
	drop := ev.PriorPresent - ev.EntryCount
	if drop <= 0 {
		return false
	}
	return float64(drop)/float64(ev.PriorPresent) >= cfg.ratio()
}

func corroborated(ev Evidence) bool {
	return ev.Corroboration != nil && *ev.Corroboration == domain.ShrinkCorroborated
}
