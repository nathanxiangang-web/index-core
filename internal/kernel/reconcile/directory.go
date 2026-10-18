package reconcile

import (
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"path"
	"strings"
	"time"
)

// ReconcileDirectory mirrors validated fresh, complete direct-child coverage.
// The coordinator must validate coverage/config/generation first. Missing
// directories carry absence to their old subtree, never to path-prefix siblings
// or identities positively observed at a new path. No provider data is deleted.
func ReconcileDirectory(prior []PriorResource, entries []domain.SnapshotEntry, scope string, cfg Config, now time.Time, snapshotID string) Result {
	res := Reconcile(prior, entries, domain.AcceptancePartial, cfg, now, snapshotID)
	// An identity conflict at an observed path does not invalidate complete
	// coverage of OTHER absent paths. The observed-path set below protects
	// conflicted entries; do not let unrelated mtime/identity drift freeze absence.
	paths, observed := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		paths[EntryPath(e)] = true
	}
	for _, o := range res.Observations {
		observed[o.ResourceID] = true
	}
	absentDirs := []string{}
	for _, p := range prior {
		if p.isPresent() && p.CanonicalPath != nil && path.Dir(*p.CanonicalPath) == scope && !paths[*p.CanonicalPath] && p.IsDir != nil && *p.IsDir && !observed[p.ResourceID] {
			absentDirs = append(absentDirs, *p.CanonicalPath)
		}
	}
	for i := range prior {
		p := &prior[i]
		if !p.isPresent() || p.CanonicalPath == nil || observed[p.ResourceID] {
			continue
		}
		missing := path.Dir(*p.CanonicalPath) == scope && !paths[*p.CanonicalPath]
		for _, dir := range absentDirs {
			missing = missing || strings.HasPrefix(*p.CanonicalPath, dir+"/")
		}
		if !missing {
			continue
		}
		at, id := now, snapshotID
		res.Transitions = append(res.Transitions, Transition{Kind: KindConfirmRemoved, ResourceID: p.ResourceID, Prior: p,
			MissingSince: &at, ConsecutiveMissing: 1, MissingFirstSnapshotID: &id, MissingLastSnapshotID: &id,
			LastConfirmedGeneration: p.LastConfirmedGeneration, Reason: "source mirror: absent from complete fresh direct-child coverage"})
		res.Counts.Removed++
		res.MutatesCanonical = true
	}
	return res
}
