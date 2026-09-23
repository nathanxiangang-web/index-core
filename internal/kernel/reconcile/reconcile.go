package reconcile

import (
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// Reconcile computes the Kernel decision for one accepted snapshot.
//
// acceptance is the Kernel-owned verdict from P3; only COMPLETE may advance
// removal evidence and authorize destructive removal (Gate 1B INV-004).
// snapshotID is the accepted Snapshot being reconciled; it is required to prove
// removal-confirmation independence and to avoid double-counting one observation
// (V2c, R2-7). A FAILED snapshot must be rejected by the caller before calling this.
func Reconcile(prior []PriorResource, entries []domain.SnapshotEntry, acceptance domain.AcceptanceState, cfg Config, now time.Time, snapshotID string) Result {
	var res Result
	if acceptance == domain.AcceptanceFailed {
		return res
	}
	observed := make(map[string]bool, len(prior))

	for i := range entries {
		e := entries[i]
		status, p, reason := resolveIdentity(e, prior, cfg, now)
		switch status {
		case ResolutionMatched:
			observed[p.ResourceID] = true
			applyMatched(&res, p, &e)
			res.Observations = append(res.Observations, Observation{ResourceID: p.ResourceID, Entry: e})
		case ResolutionNewResource:
			id := cfg.newID()
			res.Transitions = append(res.Transitions,
				Transition{Kind: KindAdd, ResourceID: id, Entry: &e, Reason: reason})
			res.Counts.Added++
			res.MutatesCanonical = true
			res.Observations = append(res.Observations, Observation{ResourceID: id, Entry: e})
		default: // CONFLICT or UNRESOLVED
			res.Counts.Conflict++
			res.Conflicts = append(res.Conflicts, Conflict{EntryLocalID: e.EntryLocalID, Reason: reason})
			res.Transitions = append(res.Transitions, Transition{Kind: KindConflict, Entry: &e, Reason: reason})
		}
	}

	// R9: a PRESENT resource not observed. Removal evidence advances only on a
	// COMPLETE snapshot, and confirmation requires a later, independently admitted
	// Snapshot (V2c). Evidence-only transitions do NOT advance the generation (R2-3).
	if acceptance == domain.AcceptanceComplete {
		for i := range prior {
			p := &prior[i]
			if !p.isPresent() || observed[p.ResourceID] {
				continue
			}
			tr := missingTransition(p, cfg, now, snapshotID)
			if tr.Kind.GenerationProducing() {
				res.MutatesCanonical = true
			}
			if tr.Kind == KindConfirmRemoved {
				res.Counts.Removed++
			} else {
				res.Counts.MissingEvidence++
			}
			res.Transitions = append(res.Transitions, tr)
		}
	}
	return res
}

func missingTransition(p *PriorResource, cfg Config, now time.Time, snapshotID string) Transition {
	tr := Transition{
		Kind: KindMissingEvidence, ResourceID: p.ResourceID, Prior: p,
		LastConfirmedGeneration: p.LastConfirmedGeneration,
		Reason:                  "MISSING in COMPLETE snapshot",
	}

	switch {
	case p.MissingFirstSnapshotID == nil || *p.MissingFirstSnapshotID == "":
		ms := now
		first := snapshotID
		tr.MissingSince = &ms
		tr.ConsecutiveMissing = 1
		tr.MissingFirstSnapshotID = &first
		tr.MissingLastSnapshotID = &first
		tr.Reason = "first MISSING observation (evidence only)"
		return tr

	case *p.MissingFirstSnapshotID == snapshotID:
		tr.MissingSince = p.MissingSince
		tr.ConsecutiveMissing = p.ConsecutiveCompleteMissing
		tr.MissingFirstSnapshotID = p.MissingFirstSnapshotID
		tr.MissingLastSnapshotID = p.MissingLastSnapshotID
		tr.Reason = "same-snapshot re-admission is not independent confirmation"
		return tr

	case p.MissingLastSnapshotID != nil && *p.MissingLastSnapshotID == snapshotID:
		// This exact observation was already counted; do not double-count (R2-7).
		tr.MissingSince = p.MissingSince
		tr.ConsecutiveMissing = p.ConsecutiveCompleteMissing
		tr.MissingFirstSnapshotID = p.MissingFirstSnapshotID
		tr.MissingLastSnapshotID = p.MissingLastSnapshotID
		tr.Reason = "observation already counted; not an independent confirmation"
		return tr

	default:
		consec := p.ConsecutiveCompleteMissing + 1
		ms := p.MissingSince
		if ms == nil {
			t := now
			ms = &t
		}
		last := snapshotID
		tr.MissingSince = ms
		tr.ConsecutiveMissing = consec
		tr.MissingFirstSnapshotID = p.MissingFirstSnapshotID
		tr.MissingLastSnapshotID = &last
		if int(consec) >= cfg.minConsecutive() {
			tr.Kind = KindRemovalCandidate
			// effectiveGrace >= MoveRecognitionHorizon, so a move-recognizable
			// resource can never be confirmed removed (R2-8).
			if cfg.effectiveGrace() <= 0 || now.Sub(*ms) >= cfg.effectiveGrace() {
				tr.Kind = KindConfirmRemoved
			}
		}
		return tr
	}
}

// applyMatched decides UNCHANGED vs UPDATE vs RENAME/MOVE, resets removal
// evidence when a MISSING resource reappears (evidence-only), and emits the
// ordered pair [path-change, update] for a RENAME/MOVE with an attribute change.
func applyMatched(res *Result, p *PriorResource, e *domain.SnapshotEntry) {
	if p.hasMissingEvidence() || p.MissingSince != nil || p.ConsecutiveCompleteMissing > 0 {
		// Evidence-only: no generation advance (R2-3).
		res.Transitions = append(res.Transitions,
			Transition{Kind: KindResetRemovalEvidence, ResourceID: p.ResourceID, Prior: p,
				Reason: "observed again; reset removal evidence"})
	}

	newPath := EntryPath(*e)
	oldPath := p.canonicalPath()
	pathChanged := oldPath != "" && oldPath != newPath
	parentChanged := PathDir(oldPath) != PathDir(newPath)
	attrsChanged := !equalInt64(p.Size, e.Size) || !equalTimePtr(p.Mtime, e.Mtime) ||
		derefStr(p.ContentHash) != derefStr(e.ContentHash) ||
		derefStr(p.ContentType) != derefStr(e.ContentType)

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
