package completeness

import "github.com/nathanxiangang-web/index-core/internal/domain"

// Observation is one admitted, independent prior observation usable for deriving
// scope_shrink_corroboration (Gate 1B Sec 3.1.3).
type Observation struct {
	Shrunk      bool
	Fresh       bool
	NoError     bool
	NoSkips     bool
	StrongAssur bool
}

// DeriveShrinkCorroboration derives the Kernel-owned corroboration of a
// significant shrink from later, independent admitted observations. It MUST NOT
// reuse the observation that first raised the shrink signal, and it never lets
// the Collector self-declare CORROBORATED (doc E Sec 2.5).
//
// It returns NONE when no independent observation exists; CORROBORATED when an
// independent fresh, error-free, skip-free, STRONG observation confirms the
// reduced scope; CONTRADICTED when an independent observation contradicts it.
func DeriveShrinkCorroboration(significantShrink bool, independent []Observation) domain.ScopeShrinkCorroboration {
	if !significantShrink {
		return domain.ShrinkNone
	}
	for _, o := range independent {
		if o.Fresh && o.NoError && o.NoSkips && o.StrongAssur {
			if o.Shrunk {
				return domain.ShrinkCorroborated
			}
			return domain.ShrinkContradicted
		}
	}
	return domain.ShrinkNone
}
