package postgres

import (
	"context"
	"errors"
	"path"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

var ErrMirrorDeferred = errors.New("mirror rolling admission deferred")

// PrepareMirrorRolling adopts only Root scheduling metadata, once per actor.
// Binding adoption remains lazy and requires a positive unique directory proof.
func (s *Store) PrepareMirrorRolling(ctx context.Context) error {
	if s.rolling == nil {
		return errors.New("mirror rolling disabled")
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO index_mirror_root_budget(root_id) SELECT DISTINCT root_id FROM index_scope_watch_state WHERE `+managedMirrorSQL+` ON CONFLICT DO NOTHING`)
	return err
}

func (s *Store) ListDueWatchesRolling(ctx context.Context, now time.Time, limit int) ([]state.ScopeWatchState, error) {
	if s.rolling == nil || limit < 3 || limit > 6 {
		return nil, errors.New("rolling due selection requires policy and3..6 candidates")
	}
	rows, err := s.pool.Query(ctx, `/* indexcore:mirror_ordinary */ WITH due AS (
 SELECT w.* FROM index_scope_watch_state w JOIN index_root r USING(root_id)
 WHERE r.lifecycle_state='ACTIVE' AND w.watch_state IN ('HOT','WARM') AND w.next_due_at<=$1
 AND (w.deferred_until IS NULL OR w.deferred_until<=$1) AND NOT (`+managedMirrorSQL+`)
 ORDER BY w.next_due_at,w.root_id,w.scope_key LIMIT $2) SELECT `+watchColumns+` FROM due`, now, limit)
	if err != nil {
		return nil, err
	}
	var ordinary []state.ScopeWatchState
	for rows.Next() {
		w, e := scanWatch(rows.Scan)
		if e != nil {
			rows.Close()
			return nil, e
		}
		ordinary = append(ordinary, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Two indexed scope ranges form a durable round; a large Root cannot hide
	// another Root. Cursors never change the requested freshness interval.
	row := s.pool.QueryRow(ctx, `/* indexcore:mirror_round */ WITH candidate AS (
 SELECT w.*,CASE WHEN rb.root_id::text>g.root_cursor THEN 0 ELSE 1 END root_band
 FROM index_mirror_root_budget rb JOIN index_root r USING(root_id) CROSS JOIN index_mirror_budget g
 CROSS JOIN LATERAL (
  SELECT * FROM (
   (SELECT w.*,0 band FROM index_scope_watch_state w WHERE w.root_id=rb.root_id AND w.scope_key>rb.scope_cursor AND `+managedMirrorSQL+` AND w.next_due_at<=$1 AND (w.deferred_until IS NULL OR w.deferred_until<=$1) ORDER BY w.scope_key LIMIT 1)
   UNION ALL
   (SELECT w.*,1 band FROM index_scope_watch_state w WHERE w.root_id=rb.root_id AND w.scope_key<=rb.scope_cursor AND `+managedMirrorSQL+` AND w.next_due_at<=$1 AND (w.deferred_until IS NULL OR w.deferred_until<=$1) ORDER BY w.scope_key LIMIT 1)
  ) slots ORDER BY band LIMIT 1
 ) w
 WHERE r.lifecycle_state='ACTIVE' AND rb.outstanding_scope IS NULL AND g.next_admit_at<=$1
 ORDER BY root_band,rb.root_id LIMIT 1)
SELECT `+watchColumns+` FROM candidate`, now)
	managed, err := scanWatch(row.Scan)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ordinary, nil
	}
	out := make([]state.ScopeWatchState, 0, limit)
	if len(ordinary) > 0 {
		out = append(out, ordinary[0])
		ordinary = ordinary[1:]
	}
	out = append(out, managed)
	for _, w := range ordinary {
		if len(out) == limit {
			break
		}
		out = append(out, w)
	}
	return out, nil
}

// EmitRollingPoll retains P3 atomicity and lock order. Denied/protected scopes
// rotate without changing Work, next_due_at, verification or freshness debt.
func (s *Store) EmitRollingPoll(ctx context.Context, root, scope string, version int64, now time.Time) (state.DirtyScopeWork, error) {
	var empty state.DirtyScopeWork
	if s.rolling == nil || state.ValidateScopeKey(scope) != nil {
		return empty, errors.New("invalid rolling poll")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	life, err := lockRootForUpdate(ctx, tx, root)
	if err != nil {
		return empty, err
	}
	if !activeFromLifecycle(life) {
		return empty, ErrStateCASConflict
	}
	_, err = lockMirrorAdapter(ctx, tx, root)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	w, err := getWatchForUpdate(ctx, tx, root, scope, version)
	if err != nil {
		return empty, err
	}
	if !managedMirror(w) || !w.Due(now) {
		return empty, ErrStateCASConflict
	}
	b, hasBinding, err := loadMirrorBinding(ctx, tx, root, scope)
	if err != nil {
		return empty, err
	}
	wk, hasWork, err := loadWorkForUpdate(ctx, tx, root, scope)
	if err != nil {
		return empty, err
	}
	var outstanding *string
	if err = tx.QueryRow(ctx, `SELECT outstanding_scope FROM index_mirror_root_budget WHERE root_id=$1`, root).Scan(&outstanding); err != nil {
		return empty, err
	}
	deferPoll := func() (state.DirtyScopeWork, error) {
		if _, e := tx.Exec(ctx, `UPDATE index_mirror_root_budget SET scope_cursor=$2 WHERE root_id=$1`, root, scope); e != nil {
			return empty, e
		}
		if _, e := tx.Exec(ctx, `UPDATE index_mirror_budget SET root_cursor=$1,deferred=deferred+1 WHERE singleton`, root); e != nil {
			return empty, e
		}
		if e := tx.Commit(ctx); e != nil {
			return empty, e
		}
		return empty, ErrMirrorDeferred
	}
	if outstanding != nil || (hasWork && (wk.WorkState != state.WorkVerified || len(wk.PendingSourceSet) > 0 || wk.ClaimedSignalSeq != nil || wk.LastErrorClass != nil)) {
		return deferPoll()
	}
	proofs, err := mirrorProofs(ctx, tx, root, path.Dir(scope), []string{scope})
	if err != nil {
		return empty, err
	}
	proof := proofs[scope]
	if !proof.valid || proof.changedAt.After(now) || (hasBinding && (!bindingMatches(b, proof) || b.lifecycle == "RETIRED")) {
		return deferPoll()
	}
	if !hasBinding {
		if _, _, err = s.bindMirrorChild(ctx, tx, root, scope, w, proof, now, time.Duration(*w.EffectiveIntervalSeconds)*time.Second); err != nil {
			return empty, err
		}
	}
	// Global ledger is always locked LAST. Never hold it while acquiring Root.
	var ready bool
	if err = tx.QueryRow(ctx, `SELECT next_admit_at<=$1 FROM index_mirror_budget WHERE singleton FOR UPDATE`, now).Scan(&ready); err != nil {
		return empty, err
	}
	if !ready {
		return empty, ErrMirrorDeferred
	}
	wk, err = mergeSignalTx(ctx, tx, state.DirtySignal{RootID: root, ScopeKey: scope, Source: state.SourcePollSchedule, Reason: state.ReasonPossibleChange, Priority: state.PriorityNormal, SeenAt: now}, true)
	if err != nil {
		return empty, err
	}
	if emitDuePollAfterMergeHook != nil {
		if err = emitDuePollAfterMergeHook(); err != nil {
			return empty, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE index_scope_watch_state SET last_due_at=$3,next_due_at=$4,updated_at=$3,version=version+1 WHERE root_id=$1 AND scope_key=$2 AND version=$5`, root, scope, now, now.Add(time.Duration(*w.EffectiveIntervalSeconds)*time.Second), version); err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, `UPDATE index_mirror_root_budget SET scope_cursor=$2,outstanding_scope=$2,outstanding_seq=$3 WHERE root_id=$1`, root, scope, wk.SignalSeq); err != nil {
		return empty, err
	}
	if _, err = tx.Exec(ctx, `UPDATE index_mirror_budget SET root_cursor=$1,next_admit_at=$2,emitted=emitted+1 WHERE singleton`, root, now.Add(s.rolling.AdmissionSpacing())); err != nil {
		return empty, err
	}
	if err = tx.Commit(ctx); err != nil {
		return empty, err
	}
	return wk, nil
}

// IsMirrorBackground classifies only the exact unresolved epoch. A Hint or any
// other foreground source merged into the claim keeps foreground HTTP policy.
func (s *Store) IsMirrorBackground(ctx context.Context, w state.DirtyScopeWork) (bool, error) {
	if s.rolling == nil || w.ClaimedSignalSeq == nil || len(w.ClaimedSourceSet) != 1 || w.ClaimedSourceSet[0] != state.SourcePollSchedule {
		return false, nil
	}
	var yes bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM index_mirror_root_budget WHERE root_id=$1 AND outstanding_scope=$2 AND outstanding_seq<=$3)`, w.RootID, w.ScopeKey, *w.ClaimedSignalSeq).Scan(&yes)
	return yes, err
}

// MaintainMirrorBindings performs finite, restart-safe cursor maintenance.
func (s *Store) MaintainMirrorBindings(ctx context.Context, now time.Time, limit int) error {
	if s.rolling == nil || limit < 1 || limit > 5 {
		return errors.New("mirror maintenance requires policy and1..5 items")
	}
	rows, err := s.pool.Query(ctx, `/* indexcore:mirror_maintenance */ WITH cursor AS (SELECT coalesce(nullif(maintenance_root,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) root,maintenance_scope scope FROM index_mirror_budget), candidates AS (
 (SELECT b.root_id,b.scope_key,b.epoch,0 band FROM index_mirror_watch_binding b,cursor c WHERE b.lifecycle<>'RETIRED' AND (b.root_id,b.scope_key)>(c.root,c.scope) ORDER BY b.root_id,b.scope_key LIMIT $1)
 UNION ALL
 (SELECT b.root_id,b.scope_key,b.epoch,1 band FROM index_mirror_watch_binding b,cursor c WHERE b.lifecycle<>'RETIRED' AND (b.root_id,b.scope_key)<=(c.root,c.scope) ORDER BY b.root_id,b.scope_key LIMIT $1))
SELECT c.root_id::text,c.scope_key,c.epoch,w.version FROM candidates c JOIN index_scope_watch_state w USING(root_id,scope_key) ORDER BY c.band,c.root_id,c.scope_key LIMIT $1`, limit)
	if err != nil {
		return err
	}
	type item struct {
		root, scope    string
		epoch, version int64
	}
	var items []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.root, &i.scope, &i.epoch, &i.version); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		if _, err = s.MaintainMirrorWatch(ctx, i.root, i.scope, i.version, i.epoch, now); err != nil && !errors.Is(err, ErrStateCASConflict) {
			return err
		}
	}
	if len(items) > 0 {
		last := items[len(items)-1]
		_, err = s.pool.Exec(ctx, `UPDATE index_mirror_budget SET maintenance_root=$1,maintenance_scope=$2 WHERE singleton`, last.root, last.scope)
	}
	return err
}

type MirrorRollingStats struct {
	Watches, Scheduled, Bindings, Expired, Retired, Rebindings, Renewals, ExpirationEvents, RetirementEvents, OutstandingRoots, Emitted, Deferred int64
	FreshnessDebtSeconds, OldestFreshnessDebtSeconds                                                                                              float64
}

// Explicit observation only: the20K aggregate never runs on every Runtime wake.
func (s *Store) MirrorRollingStats(ctx context.Context, now time.Time) (MirrorRollingStats, error) {
	var out MirrorRollingStats
	err := s.pool.QueryRow(ctx, `/* indexcore:mirror_stats */ WITH watches AS (
 SELECT count(*) total,count(*) FILTER(WHERE w.watch_state='WARM') scheduled,
 coalesce(sum(greatest(0,extract(epoch FROM ($1-coalesce(b.freshness_at,k.last_verified_at,w.last_success_at,w.created_at)))-w.effective_interval_seconds)) FILTER(WHERE w.watch_state='WARM'),0)::float8 debt,
 coalesce(max(greatest(0,extract(epoch FROM ($1-coalesce(b.freshness_at,k.last_verified_at,w.last_success_at,w.created_at)))-w.effective_interval_seconds)) FILTER(WHERE w.watch_state='WARM'),0)::float8 oldest
 FROM index_scope_watch_state w LEFT JOIN index_mirror_watch_binding b USING(root_id,scope_key) LEFT JOIN index_dirty_scope_work k USING(root_id,scope_key)
 WHERE w.cadence_class='MIRROR_REVISIT' AND w.source_set=ARRAY['BACKSTOP_ENROLL']::text[] AND w.priority_class='NORMAL'), bindings AS (
 SELECT count(*) total,count(*) FILTER(WHERE lifecycle='EXPIRED') expired,count(*) FILTER(WHERE lifecycle='RETIRED') retired,coalesce(sum(binds-1),0)::bigint rebound,coalesce(sum(renewals),0)::bigint renewed,coalesce(sum(expirations),0)::bigint expired_events,coalesce(sum(retirements),0)::bigint retired_events FROM index_mirror_watch_binding)
 SELECT w.total,w.scheduled,b.total,b.expired,b.retired,b.rebound,b.renewed,b.expired_events,b.retired_events,(SELECT count(*) FROM index_mirror_root_budget WHERE outstanding_scope IS NOT NULL),g.emitted,g.deferred,w.debt,w.oldest FROM watches w,bindings b,index_mirror_budget g`, now).Scan(&out.Watches, &out.Scheduled, &out.Bindings, &out.Expired, &out.Retired, &out.Rebindings, &out.Renewals, &out.ExpirationEvents, &out.RetirementEvents, &out.OutstandingRoots, &out.Emitted, &out.Deferred, &out.FreshnessDebtSeconds, &out.OldestFreshnessDebtSeconds)
	return out, err
}
