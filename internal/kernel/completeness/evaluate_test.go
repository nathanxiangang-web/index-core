package completeness

import (
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

func corroboratedShrink() *domain.ScopeShrinkCorroboration {
	v := domain.ShrinkCorroborated
	return &v
}

func TestEvaluateCascade(t *testing.T) {
	positive := Evidence{
		TraversalStatus:   domain.TraversalSuccess,
		SkippedKnownEmpty: true,
		Freshness:         domain.FreshDirect,
		Assurance:         domain.StrongFailureVisibility,
		EntryCount:        10,
	}
	cases := []struct {
		name string
		ev   Evidence
		want domain.AcceptanceState
	}{
		{"failed traversal", Evidence{TraversalStatus: domain.TraversalFailed}, domain.AcceptanceFailed},
		{"partial traversal", Evidence{TraversalStatus: domain.TraversalPartial}, domain.AcceptancePartial},
		{"interrupted traversal", Evidence{TraversalStatus: domain.TraversalInterrupted}, domain.AcceptancePartial},
		{"error summary", Evidence{TraversalStatus: domain.TraversalSuccess, HasErrorSummary: true}, domain.AcceptancePartial},
		{"skipped scopes", Evidence{TraversalStatus: domain.TraversalSuccess, HasSkippedScopes: true}, domain.AcceptancePartial},
		{"stale freshness", Evidence{TraversalStatus: domain.TraversalSuccess, Freshness: domain.FreshnessStale}, domain.AcceptanceStale},
		{"unknown freshness with cache", Evidence{TraversalStatus: domain.TraversalSuccess, UsedCache: true}, domain.AcceptanceStale},
		{
			"significant shrink unconfirmed",
			Evidence{TraversalStatus: domain.TraversalSuccess, SkippedKnownEmpty: true,
				Freshness: domain.FreshDirect, Assurance: domain.StrongFailureVisibility,
				PriorPresent: 100, EntryCount: 10},
			domain.AcceptanceSuspicious,
		},
		{
			"empty scope where prior non-empty",
			Evidence{TraversalStatus: domain.TraversalSuccess, SkippedKnownEmpty: true,
				Freshness: domain.FreshDirect, Assurance: domain.StrongFailureVisibility,
				PriorPresent: 100, EntryCount: 0},
			domain.AcceptanceSuspicious,
		},
		{"all positive", positive, domain.AcceptanceComplete},
		{
			"corroborated significant shrink is complete",
			Evidence{TraversalStatus: domain.TraversalSuccess, SkippedKnownEmpty: true,
				Freshness: domain.FreshDirect, Assurance: domain.StrongFailureVisibility,
				PriorPresent: 100, EntryCount: 10, Corroboration: corroboratedShrink()},
			domain.AcceptanceComplete,
		},
		{
			"unknown skips cannot be complete",
			Evidence{TraversalStatus: domain.TraversalSuccess, SkippedUnknown: true,
				Freshness: domain.FreshDirect, Assurance: domain.StrongFailureVisibility, EntryCount: 10},
			domain.AcceptancePartial,
		},
		{
			"weak assurance is suspicious (C-9a)",
			Evidence{TraversalStatus: domain.TraversalSuccess, SkippedKnownEmpty: true,
				Freshness: domain.FreshDirect, Assurance: domain.WeakFailureVisibility, EntryCount: 10},
			domain.AcceptanceSuspicious,
		},
		{
			"unknown assurance is suspicious (C-9a)",
			Evidence{TraversalStatus: domain.TraversalSuccess, SkippedKnownEmpty: true,
				Freshness: domain.FreshDirect, EntryCount: 10},
			domain.AcceptanceSuspicious,
		},
	}
	for _, tc := range cases {
		if got := Evaluate(tc.ev, Config{}); got != tc.want {
			t.Errorf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

func TestDeriveShrinkCorroboration(t *testing.T) {
	strong := Observation{Shrunk: true, Fresh: true, NoError: true, NoSkips: true, StrongAssur: true}
	if got := DeriveShrinkCorroboration(false, nil); got != domain.ShrinkNone {
		t.Errorf("no significant shrink must be NONE, got %s", got)
	}
	if got := DeriveShrinkCorroboration(true, nil); got != domain.ShrinkNone {
		t.Errorf("no independent observation must be NONE, got %s", got)
	}
	if got := DeriveShrinkCorroboration(true, []Observation{strong}); got != domain.ShrinkCorroborated {
		t.Errorf("independent strong shrink must be CORROBORATED, got %s", got)
	}
	contra := strong
	contra.Shrunk = false
	if got := DeriveShrinkCorroboration(true, []Observation{contra}); got != domain.ShrinkContradicted {
		t.Errorf("independent non-shrink must be CONTRADICTED, got %s", got)
	}
	weak := Observation{Shrunk: true, Fresh: true, NoError: true, NoSkips: true}
	if got := DeriveShrinkCorroboration(true, []Observation{weak}); got != domain.ShrinkNone {
		t.Errorf("weak observation must not corroborate, got %s", got)
	}
}
