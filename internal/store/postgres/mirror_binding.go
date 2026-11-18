package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

const managedMirrorSQL = `watch_state='WARM' AND cadence_class='MIRROR_REVISIT' AND source_set=ARRAY['BACKSTOP_ENROLL']::text[] AND priority_class='NORMAL'`

func mirrorOwned(w state.ScopeWatchState) bool {
	return w.CadenceClass == "MIRROR_REVISIT" && w.Priority == state.PriorityNormal && len(w.SourceSet) == 1 && w.SourceSet[0] == state.WatchSourceBackstopEnroll
}

func managedMirror(w state.ScopeWatchState) bool {
	return mirrorOwned(w) && w.WatchState == state.WatchWarm && w.EffectiveIntervalSeconds != nil && *w.EffectiveIntervalSeconds >= 30 && *w.EffectiveIntervalSeconds <= 3600
}

type mirrorBinding struct {
	id, path, digest, revision, lifecycle string
	epoch                                 int64
	expires                               time.Time
	retiredVersion                        *int64
}

func loadMirrorBinding(ctx context.Context, tx pgx.Tx, root, scope string) (mirrorBinding, bool, error) {
	var b mirrorBinding
	err := tx.QueryRow(ctx, `SELECT resource_id::text,canonical_path,adapter_digest,adapter_revision,epoch,lifecycle,lease_expires_at,retired_watch_version FROM index_mirror_watch_binding WHERE root_id=$1 AND scope_key=$2 FOR UPDATE`, root, scope).Scan(&b.id, &b.path, &b.digest, &b.revision, &b.epoch, &b.lifecycle, &b.expires, &b.retiredVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, false, nil
	}
	return b, err == nil, err
}

func bindingMatches(b mirrorBinding, p mirrorProof) bool {
	return p.valid && b.id == p.resourceID && b.path == p.canonicalPath && b.digest == p.digest && b.revision == p.revision
}

// Caller owns Root -> adapter -> Watch. Only positive canonical discovery can
// rebind an existing epoch; expiry never authorizes a new identity or deletion.
func (s *Store) bindMirrorChild(ctx context.Context, tx pgx.Tx, root, scope string, w state.ScopeWatchState, proof mirrorProof, now time.Time, interval time.Duration) (state.ScopeWatchState, bool, error) {
	if !proof.valid || !mirrorOwned(w) {
		return w, false, nil
	}
	b, found, err := loadMirrorBinding(ctx, tx, root, scope)
	if err != nil {
		return w, false, err
	}
	reactivated := false
	if w.WatchState == state.WatchCold {
		if !found || b.lifecycle != "RETIRED" || b.retiredVersion == nil || *b.retiredVersion != w.Version {
			return w, false, nil
		}
		w, err = scanWatch(tx.QueryRow(ctx, `UPDATE index_scope_watch_state SET watch_state='WARM',effective_interval_seconds=$3,next_due_at=$4,updated_at=$5,version=version+1 WHERE root_id=$1 AND scope_key=$2 AND version=$6 RETURNING `+watchColumns, root, scope, int64(interval/time.Second), now.Add(interval), now, w.Version).Scan)
		if err != nil {
			return w, false, err
		}
		reactivated = true
	}
	if !managedMirror(w) {
		return w, false, nil
	}
	if !found {
		_, err = tx.Exec(ctx, `INSERT INTO index_mirror_watch_binding(root_id,scope_key,resource_id,canonical_path,adapter_digest,adapter_revision,lifecycle,lease_expires_at,freshness_at,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,'ACTIVE',$7,least($8,coalesce((SELECT last_verified_at FROM index_dirty_scope_work WHERE root_id=$1 AND scope_key=$2),(SELECT coalesce(last_success_at,created_at) FROM index_scope_watch_state WHERE root_id=$1 AND scope_key=$2),$8)),$8,$8)`, root, scope, proof.resourceID, proof.canonicalPath, proof.digest, proof.revision, now.Add(s.rolling.Lease), now)
		return w, false, err
	}
	if bindingMatches(b, proof) && b.lifecycle != "RETIRED" {
		return w, false, nil
	}
	if !reactivated {
		w, err = scanWatch(tx.QueryRow(ctx, `UPDATE index_scope_watch_state SET updated_at=$3,version=version+1 WHERE root_id=$1 AND scope_key=$2 AND version=$4 RETURNING `+watchColumns, root, scope, now, w.Version).Scan)
		if err != nil {
			return w, false, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE index_mirror_watch_binding SET resource_id=$3,canonical_path=$4,adapter_digest=$5,adapter_revision=$6,epoch=epoch+1,lifecycle='ACTIVE',lease_expires_at=$7,freshness_at=least(freshness_at,$8),updated_at=$8,last_observed_at=NULL,retired_watch_version=NULL,retired_event_seq=NULL,retired_reason=NULL,binds=binds+1 WHERE root_id=$1 AND scope_key=$2`, root, scope, proof.resourceID, proof.canonicalPath, proof.digest, proof.revision, now.Add(s.rolling.Lease), now)
	return w, true, err
}

// Lock the adapter before any Watch/Work. xmin plus digest rejects config ABA.
func lockMirrorAdapter(ctx context.Context, tx pgx.Tx, root string) (AdapterConfig, error) {
	var a AdapterConfig
	err := tx.QueryRow(ctx, `SELECT collector_kind,config,xmin::text FROM index_root_adapter_config WHERE root_id=$1 FOR SHARE`, root).Scan(&a.CollectorKind, &a.Config, &a.Revision)
	return a, err
}

func (s *Store) completeMirrorReservation(ctx context.Context, tx pgx.Tx, root, scope string, verified int64, now time.Time, proof mirrorProof) error {
	// Flag-off stops admission/lifecycle, not acknowledgement of a previously
	// reserved successful prefix. Otherwise an off/drain/on transition strands
	// the Root forever. Failures/cancellation never reach this success path.
	if s.rolling != nil && proof.valid {
		if _, err := tx.Exec(ctx, `UPDATE index_mirror_watch_binding SET lifecycle='ACTIVE',lease_expires_at=$3,freshness_at=$4,last_observed_at=$4,updated_at=$4,renewals=renewals+1
WHERE root_id=$1 AND scope_key=$2 AND resource_id=$5 AND canonical_path=$6 AND adapter_digest=$7 AND adapter_revision=$8 AND lifecycle<>'RETIRED'`, root, scope, now.Add(s.rolling.Lease), now, proof.resourceID, proof.canonicalPath, proof.digest, proof.revision); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE index_mirror_root_budget SET outstanding_scope=NULL,outstanding_seq=NULL WHERE root_id=$1 AND outstanding_scope=$2 AND outstanding_seq<=$3`, root, scope, verified)
	return err
}

// MaintainMirrorWatch changes operational schedule only. A stale version/epoch
// is a CAS conflict; missing/failed observations are never retirement evidence.
func (s *Store) MaintainMirrorWatch(ctx context.Context, root, scope string, version, epoch int64, now time.Time) (string, error) {
	if s.rolling == nil {
		return "", errors.New("mirror rolling disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	lifecycle, err := lockRootForUpdate(ctx, tx, root)
	if err != nil {
		return "", err
	}
	if !activeFromLifecycle(lifecycle) {
		return "INACTIVE", nil
	}
	a, err := lockMirrorAdapter(ctx, tx, root)
	if errors.Is(err, pgx.ErrNoRows) {
		return "UNPROVEN", nil
	}
	if err != nil {
		return "", err
	}
	w, err := getWatchForUpdate(ctx, tx, root, scope, version)
	if err != nil {
		return "", err
	}
	b, found, err := loadMirrorBinding(ctx, tx, root, scope)
	if err != nil {
		return "", err
	}
	if !found || b.epoch != epoch {
		return "", ErrStateCASConflict
	}
	if !managedMirror(w) || b.lifecycle == "RETIRED" {
		return "PROTECTED", nil
	}
	result := "ACTIVE"
	if !b.expires.After(now) && b.lifecycle == "ACTIVE" {
		if _, err = tx.Exec(ctx, `UPDATE index_mirror_watch_binding SET lifecycle='EXPIRED',updated_at=$3,expirations=expirations+1 WHERE root_id=$1 AND scope_key=$2`, root, scope, now); err != nil {
			return "", err
		}
		result = "EXPIRED"
	}
	// Expiry remains recorded even if identity/configuration cannot be proved.
	if b.digest != AdapterConfigDigest(a) || b.revision != a.Revision {
		err = tx.Commit(ctx)
		return result, err
	}
	wk, hasWork, err := loadWorkForUpdate(ctx, tx, root, scope)
	if err != nil {
		return "", err
	}
	if hasWork && (wk.WorkState != state.WorkVerified || len(wk.PendingSourceSet) > 0 || wk.ClaimedSignalSeq != nil || wk.LastErrorClass != nil) {
		err = tx.Commit(ctx)
		return result, err
	}
	// Canonical and Journal are READ ONLY. Even a REMOVED row is insufficient
	// without its accepted producing Snapshot and committed resource event.
	var event int64
	var reason string
	err = tx.QueryRow(ctx, `/* indexcore:mirror_retirement */
SELECT j.event_seq,CASE WHEN c.resource_presence='REMOVED' THEN 'KERNEL_REMOVED' ELSE 'KERNEL_RELOCATED' END
FROM index_canonical_resource c
JOIN index_journal_event j ON j.root_id=c.root_id AND j.resource_id=c.resource_id
JOIN index_generation g ON g.root_id=j.root_id AND g.generation_number=j.generation_number
JOIN index_snapshot s ON s.snapshot_id=g.produced_by_snapshot_id AND s.root_id=g.root_id
WHERE c.root_id=$1 AND c.resource_id=$2 AND c.is_dir
AND ((c.resource_presence='REMOVED' AND j.event_type='resource-removed') OR
 (c.resource_presence='PRESENT' AND c.canonical_path<>$3 AND j.event_type IN ('resource-renamed','resource-moved')))
AND s.acceptance_state IN ('COMPLETE','PARTIAL') AND s.lifecycle_state='RECONCILED'
AND NOT EXISTS(SELECT 1 FROM index_canonical_resource other WHERE other.root_id=$1 AND other.canonical_path=$3 AND other.resource_presence='PRESENT')
ORDER BY j.event_seq DESC LIMIT 1`, root, b.id, b.path).Scan(&event, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.Commit(ctx)
		return result, err
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE index_scope_watch_state SET watch_state='COLD',effective_interval_seconds=NULL,next_due_at=NULL,updated_at=$3,version=version+1 WHERE root_id=$1 AND scope_key=$2 AND version=$4`, root, scope, now, version); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE index_mirror_watch_binding SET lifecycle='RETIRED',retired_watch_version=$3,retired_event_seq=$4,retired_reason=$5,updated_at=$6,retirements=retirements+1 WHERE root_id=$1 AND scope_key=$2`, root, scope, version+1, event, reason, now); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return "RETIRED", nil
}
