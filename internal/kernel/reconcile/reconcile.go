package reconcile

import (
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// Reconcile computes the Kernel decision for one accepted snapshot. acceptance is
// the Kernel-owned verdict from P3; only COMPLETE may advance removal evidence
// and authorize destructive removal (Gate 1B INV-004). A FAILED snapshot must be
// rejected by the caller before calling this.
func Reconcile(prior []PriorResource, entries []domain.SnapshotEntry, acceptance domain.AcceptanceState, cfg Config, now time.Time) Result {
	var res Result
	if acceptance == domain.AcceptanceFailed {
		return res
	}
	observed := make(map[string]bool, len(prior))

	for i := range entries {
		e := entries[i]
		status, p, reason := resolveIdentity(e, prior)
		switch status {
		case ResolutionMatched:
			observed[p.ResourceID] = true
			applyMatched(&res, p, &e)
		case ResolutionNewResource:
			id := cfg.newID()
			res.Transitions = append(res.Transitions,
				Transition{Kind: KindAdd, ResourceID: id, Entry: &e, Reason: reason})
			res.Counts.Added++
			res.MutatesCanonical = true
		default: // CONFLICT or UNRESOLVED
			res.Counts.Conflict++
			res.Conflicts = append(res.Conflicts, Conflict{EntryLocalID: e.EntryLocalID, Reason: reason})
			res.Transitions = append(res.Transitions, Transition{Kind: KindConflict, Entry: &e, Reason: reason})
		}
	}

	// Removal evidence advances only on a COMPLETE snapshot (INV-004).
	if acceptance == domain.AcceptanceComplete {
		for i := range prior {
			p := &prior[i]
			if p.ResourcePresence != domain.ResourcePresent || observed[p.ResourceID] {
				continue
			}
			res.MutatesCanonical = true
			consec := p.ConsecutiveCompleteMissing + 1
			missingSince := p.MissingSince
			if missingSince == nil {
				t := now
				missingSince = &t
			}
			tr := Transition{
				Kind: KindMissingEvidence, ResourceID: p.ResourceID, Prior: p,
				MissingSince: missingSince, ConsecutiveMissing: int32(consec),
				LastConfirmedGeneration: p.LastConfirmedGeneration,
				Reason:                  "MISSING in COMPLETE snapshot",
			}
			if int(consec) >= cfg.minConsecutive() {
				tr.Kind = KindRemovalCandidate
				if cfg.RemovalGracePeriod <= 0 || now.Sub(*missingSince) >= cfg.RemovalGracePeriod {
					tr.Kind = KindConfirmRemoved
					res.Counts.Removed++
				} else {
					res.Counts.MissingEvidence++
				}
			} else {
				res.Counts.MissingEvidence++
			}
			res.Transitions = append(res.Transitions, tr)
		}
	}
	return res
}

// applyMatched decides UNCHANGED vs UPDATE vs RENAME/MOVE. A RENAME/MOVE with a
// concurrent attribute change produces the ordered pair [path-change, update]
// (doc D Sec 6), which the Store assigns intra_generation_seq 1 then 2.
func applyMatched(res *Result, p *PriorResource, e *domain.SnapshotEntry) {
	newPath := EntryPath(*e)
	oldPath := derefStr(p.CanonicalPath)

	pathChanged := oldPath != "" && oldPath != newPath
	parentChanged := PathDir(oldPath) != PathDir(newPath)
	attrsChanged := !equalInt64(p.Size, e.Size) || !equalTimePtr(p.Mtime, e.Mtime) ||
		derefStr(p.ContentHash) != derefStr(e.ContentHash)

	if !pathChanged && !attrsChanged {
		res.Counts.Unchanged++
		res.Transitions = append(res.Transitions,
			Transition{Kind: KindUnchanged, ResourceID: p.ResourceID, Prior: p, Entry: e})
		return
	}

	res.MutatesCanonical = true
	if pathChanged {
		kind := KindRename
		if parentChanged {
			kind = KindMove

		}
		np := newPath
		res.Transitions = append(res.Transitions,
			Transition{Kind: kind, ResourceID: p.ResourceID, Prior: p, Entry: e, NewPath: &np, Reason: "path change"})
		if kind == KindMove {
			res.Counts.Moved++
		} else {
			res.Counts.Renamed++
		}
	}
	if attrsChanged {
		res.Transitions = append(res.Transitions,
			Transition{Kind: KindUpdate, ResourceID: p.ResourceID, Prior: p, Entry: e, Reason: "attribute change"})
		res.Counts.Updated++
	}
}
