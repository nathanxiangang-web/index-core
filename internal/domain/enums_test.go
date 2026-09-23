package domain_test

import (
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// G3-R4(4): only FAILED/INTERRUPTED traversal is a genuine source failure.
// PARTIAL is a legal additive-safe input that the Kernel still evaluates, so the
// runtime command must NOT fail the scan for it.
func TestTraversalStatusIsSourceFailure(t *testing.T) {
	cases := map[domain.TraversalStatus]bool{
		domain.TraversalSuccess:     false,
		domain.TraversalPartial:     false,
		domain.TraversalFailed:      true,
		domain.TraversalInterrupted: true,
	}
	for st, want := range cases {
		if got := st.IsSourceFailure(); got != want {
			t.Errorf("TraversalStatus(%s).IsSourceFailure() = %v, want %v", st, got, want)
		}
	}
}
