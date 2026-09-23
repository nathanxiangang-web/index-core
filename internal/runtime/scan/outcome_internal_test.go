package scan

import (
	"errors"
	"testing"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// G3-R5.4: a Kernel policy rejection of a SUCCESSFUL observation must NOT be
// reported as a source failure. Only FAILED/INTERRUPTED traversal is
// ErrSourceFailed; PARTIAL is a legal additive-safe input.
func TestScanOutcomeErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		traversal domain.TraversalStatus
		out       postgres.ReconcileOutcome
		want      error
	}{
		{"success applied", domain.TraversalSuccess, postgres.ReconcileOutcome{Status: domain.AdmissionApplied}, nil},
		{"partial applied", domain.TraversalPartial, postgres.ReconcileOutcome{Status: domain.AdmissionApplied}, nil},
		{"success noop", domain.TraversalSuccess, postgres.ReconcileOutcome{Status: domain.AdmissionNoop}, nil},
		{"failed traversal", domain.TraversalFailed, postgres.ReconcileOutcome{Status: domain.AdmissionRejected}, ErrSourceFailed},
		{"interrupted traversal", domain.TraversalInterrupted, postgres.ReconcileOutcome{Status: domain.AdmissionRejected}, ErrSourceFailed},
		{"success but kernel rejected", domain.TraversalSuccess, postgres.ReconcileOutcome{Status: domain.AdmissionRejected, SnapshotLifecycle: domain.SnapshotRejected}, ErrReconcileRejected},
		{"partial but kernel rejected", domain.TraversalPartial, postgres.ReconcileOutcome{Status: domain.AdmissionRejected, SnapshotLifecycle: domain.SnapshotRejected}, ErrReconcileRejected},
	}
	for _, tc := range cases {
		err := scanOutcomeError(tc.traversal, tc.out)
		if tc.want == nil {
			if err != nil {
				t.Errorf("%s: expected nil, got %v", tc.name, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: expected %v, got %v", tc.name, tc.want, err)
		}
		if tc.want == ErrReconcileRejected && errors.Is(err, ErrSourceFailed) {
			t.Errorf("%s: kernel rejection must not be ErrSourceFailed", tc.name)
		}
	}
}
