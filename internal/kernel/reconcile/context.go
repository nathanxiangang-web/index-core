package reconcile

import (
	"sort"
	"strings"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// RemovalDecisionContext canonicalizes the Kernel-owned removal-decision state
// using the SAME identity resolution that reconcile uses (R3-1). It is derived
// from identity outcomes, not from path membership:
//
//   - a prior resource that is identity-observed and has no missing evidence is
//     omitted;
//   - a prior resource re-observed after MISSING is marked "reappears_reset";
//   - a prior PRESENT resource not identity-observed (including a still-MISSING
//     resource whose path was taken by an imposter) is marked "absent" with its
//     current removal-evidence state.
//
// It contains no snapshot_id, admission_seq, or timing, so two semantically
// identical raw entry sets that drive different removal decisions cannot collapse
// into one IO3 identity.
func RemovalDecisionContext(prior []PriorResource, entries []domain.SnapshotEntry, cfg Config, now time.Time) string {
	observed := make(map[string]bool, len(prior))
	reset := make(map[string]bool)
	ix := BuildPriorIndex(prior)
	for i := range entries {
		status, p, _ := ix.resolve(entries[i], cfg, now)
		if status == ResolutionMatched {
			observed[p.ResourceID] = true
			if p.hasMissingEvidence() || p.MissingSince != nil || p.ConsecutiveCompleteMissing > 0 {
				reset[p.ResourceID] = true
			}
		}
	}

	var lines []string
	for i := range prior {
		p := &prior[i]
		if !p.isPresent() {
			continue
		}
		tag := "absent"
		switch {
		case reset[p.ResourceID]:
			tag = "reappears_reset"
		case observed[p.ResourceID]:
			continue // identity-observed, no removal decision change
		}
		first := "0"
		if p.MissingFirstSnapshotID != nil && *p.MissingFirstSnapshotID != "" {
			first = "1"
		}
		lines = append(lines, strings.Join([]string{
			tag,
			"rid=" + p.ResourceID,
			"state=" + string(p.RemovalEvidenceState),
			"consec=" + itoa(int(p.ConsecutiveCompleteMissing)),
			"first=" + first,
		}, "|"))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
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
