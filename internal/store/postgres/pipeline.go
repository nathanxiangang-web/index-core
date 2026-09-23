package postgres

import "github.com/nathanxiangang-web/index-core/internal/domain"

// Evaluation is now performed inside the safe coordinator under the per-root
// lock (see coordinator.go), so acceptance and the final IO3 identity are bound
// to the canonical generation being reconciled (R3-3). This file keeps the small
// evidence-normalization helpers shared by the coordinator.

func freshOrUnknown(f *domain.FreshnessEvidence) domain.FreshnessEvidence {
	if f == nil {
		return domain.FreshnessUnknown
	}
	return *f
}

func assuranceOrUnknown(a *domain.FailureVisibility) domain.FailureVisibility {
	if a == nil {
		return domain.UnknownFailureVisibility
	}
	return *a
}
