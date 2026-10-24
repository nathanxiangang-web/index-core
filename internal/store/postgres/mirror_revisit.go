package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

type mirrorProof struct {
	valid     bool
	changedAt time.Time
}
type mirrorCompletion struct {
	interval time.Duration
	proofs   map[string]mirrorProof
	stats    state.MirrorDiscoveryStats
}

// Same atomic boundary as accepted discovery. Configurable only by a trusted
// runtime; proof reads grant no canonical write/absence authority.
func (s *Store) CompleteSuccessWithMirrorChildren(ctx context.Context, root, scope string, seq int64, children []string, priorityScope string, now time.Time, interval time.Duration) (state.DirtyScopeWork, state.MirrorDiscoveryStats, error) {
	if err := state.ValidateMirrorRevisit(interval); err != nil || interval == 0 {
		return state.DirtyScopeWork{}, state.MirrorDiscoveryStats{}, errors.New("mirror completion requires a valid positive revisit interval")
	}
	p := &mirrorCompletion{interval: interval}
	w, err := s.completeDiscovery(ctx, root, scope, seq, children, priorityScope, now, p)
	if err != nil {
		return w, state.MirrorDiscoveryStats{}, err
	} // no committed counters on rollback
	return w, p.stats, nil
}

func mirrorProofs(ctx context.Context, tx pgx.Tx, root, parent string, children []string) (map[string]mirrorProof, error) {
	proofs := map[string]mirrorProof{}
	var kind string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT collector_kind,config FROM index_root_adapter_config WHERE root_id=$1`, root).Scan(&kind, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return proofs, nil
	}
	if err != nil {
		return nil, err
	}
	var adapter struct {
		Path   string `json:"path"`
		Mirror bool   `json:"mirror_presence"`
	}
	if json.Unmarshal(raw, &adapter) != nil || !adapter.Mirror || (kind != "alist" && kind != "openlist") || state.ValidateScopeKey(adapter.Path) != nil {
		return proofs, nil
	}
	prefix := strings.TrimSuffix(adapter.Path, "/")
	paths := []string{prefix + parent}
	for _, c := range children {
		paths = append(paths, prefix+c)
	}
	rows, err := tx.Query(ctx, `SELECT canonical_path,count(*),bool_and(coalesce(is_dir,false) AND removal_evidence_state='NONE'),max(updated_at) FROM index_canonical_resource WHERE root_id=$1 AND resource_presence='PRESENT' AND canonical_path=ANY($2::text[]) GROUP BY canonical_path`, root, paths)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var count int
		var valid bool
		var changed time.Time
		if err := rows.Scan(&path, &count, &valid, &changed); err != nil {
			return nil, err
		}
		proofs[strings.TrimPrefix(path, prefix)] = mirrorProof{valid: valid && count == 1, changedAt: changed}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if parent != "/" && !proofs[parent].valid {
		return map[string]mirrorProof{}, nil
	}
	return proofs, nil
}

func (p *mirrorCompletion) child(ctx context.Context, tx pgx.Tx, root, child string, priority state.Priority, watch state.ScopeWatchState, hasWatch bool, now time.Time) (bool, error) {
	merge := func() (bool, error) { p.stats.SignalsMerged++; return false, nil }
	if priority != state.PriorityNormal {
		p.stats.ExplicitChildren++
		return merge()
	}
	proof := p.proofs[child]
	if !proof.valid || proof.changedAt.After(now) || len(strings.Split(strings.Trim(child, "/"), "/")) > 8 {
		p.stats.UncertainChildren++
		return merge()
	}
	// The watch is created BEFORE the child Work is locked/inserted. Existing
	// watches, including explicit DISABLED/COLD or deferred policy, are untouched.
	if !hasWatch {
		seconds := int64(p.interval / time.Second)
		row := tx.QueryRow(ctx, `INSERT INTO index_scope_watch_state(root_id,scope_key,watch_state,cadence_class,effective_interval_seconds,source_set,priority_class,next_due_at) VALUES($1,$2,'WARM','MIRROR_REVISIT',$3,ARRAY['BACKSTOP_ENROLL'],'NORMAL',$4) ON CONFLICT DO NOTHING RETURNING `+watchColumns, root, child, seconds, now.Add(p.interval))
		var err error
		watch, err = scanWatch(row.Scan)
		if errors.Is(err, pgx.ErrNoRows) {
			watch, hasWatch, err = lockWatchForUpdate(ctx, tx, root, child)
		} else if err == nil {
			hasWatch = true
			p.stats.WatchEnrolled++
		}
		if err != nil {
			return false, err
		}
	}
	wk, found, err := loadWorkForUpdate(ctx, tx, root, child)
	if err != nil {
		return false, err
	}
	covered := hasWatch && mirrorWatchCovers(watch, now, p.interval)
	if !found {
		p.stats.NewChildren++
		return merge()
	}
	if !covered {
		p.stats.UncertainChildren++
		return merge()
	}
	if wk.LastVerifiedAt != nil && wk.LastVerifiedAt.After(now) {
		p.stats.UncertainChildren++
		return merge()
	}
	if wk.LastVerifiedAt != nil && proof.changedAt.After(*wk.LastVerifiedAt) {
		p.stats.ChangedChildren++
		return merge()
	}
	switch wk.WorkState {
	case state.WorkVerified:
		if wk.LastVerifiedAt == nil || wk.LastVerifiedSignalSeq == nil || *wk.LastVerifiedSignalSeq <= 0 || *wk.LastVerifiedSignalSeq > wk.SignalSeq || wk.AttemptCount <= 0 || wk.LastErrorClass != nil {
			p.stats.UncertainChildren++
			return merge()
		}
		p.stats.VerifiedReused++
		return true, nil
	case state.WorkPending:
		if wk.LastErrorClass == nil && wk.PendingPriority != nil && state.MaxPriority(*wk.PendingPriority, priority) == *wk.PendingPriority && (wk.PendingNotBefore == nil || !wk.PendingNotBefore.After(now)) {
			p.stats.AlreadyQueued++
			return true, nil
		}
		if wk.LastVerifiedAt == nil {
			p.stats.UnverifiedChildren++
		} else {
			p.stats.ProtectedChildren++
		}
		return merge()
	default:
		p.stats.ProtectedChildren++
		return merge()
	}
}

func mirrorWatchCovers(w state.ScopeWatchState, now time.Time, interval time.Duration) bool {
	if w.WatchState != state.WatchWarm || w.CadenceClass != "MIRROR_REVISIT" || w.Priority != state.PriorityNormal || w.LastErrorClass != nil || w.DeferredUntil != nil {
		return false
	}
	if len(w.SourceSet) != 1 || w.SourceSet[0] != state.WatchSourceBackstopEnroll || w.EffectiveIntervalSeconds == nil || w.NextDueAt == nil {
		return false
	}
	seconds := *w.EffectiveIntervalSeconds
	// Validate before conversion: an arbitrary existing watch can contain an
	// interval large enough to overflow time.Duration. Never borrow its proof.
	return seconds >= 30 && seconds <= 3600 && time.Duration(seconds)*time.Second <= interval && !w.NextDueAt.After(now.Add(interval))
}
