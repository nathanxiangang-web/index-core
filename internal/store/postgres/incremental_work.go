package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// errRetryMerge is internal signal for a bounded merge retry after an insert race.
var errRetryMerge = errors.New("retry dirty signal merge")

func rootLifecycle(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, rootID string) (string, error) {
	var lifecycle string
	if err := q.QueryRow(ctx, `SELECT lifecycle_state FROM index_root WHERE root_id=$1`, rootID).Scan(&lifecycle); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return lifecycle, nil
}

func rootIsActive(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, rootID string) (bool, error) {
	lifecycle, err := rootLifecycle(ctx, q, rootID)
	if err != nil {
		return false, err
	}
	return lifecycle == "ACTIVE", nil
}

func loadWorkForUpdate(ctx context.Context, tx pgx.Tx, rootID, scopeKey string) (state.DirtyScopeWork, bool, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+workColumns+` FROM index_dirty_scope_work WHERE root_id=$1 AND scope_key=$2 FOR UPDATE`,
		rootID, scopeKey)
	wk, err := scanWork(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.DirtyScopeWork{}, false, nil
	}
	if err != nil {
		return state.DirtyScopeWork{}, false, err
	}
	return wk, true, nil
}

func insertWork(ctx context.Context, tx pgx.Tx, wk state.DirtyScopeWork) (state.DirtyScopeWork, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO index_dirty_scope_work (
			root_id, scope_key, work_state, signal_seq,
			claimed_signal_seq, claimed_source_set, claimed_reason_set, claimed_priority, claimed_first_seen_at,
			pending_source_set, pending_reason_set, pending_priority, pending_first_seen_at, pending_not_before,
			last_seen_at, attempt_count, consecutive_failures,
			last_attempt_started_at, last_attempt_finished_at, last_error_class,
			last_verified_at, last_verified_signal_seq,
			created_at, updated_at, version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$23,1)
		ON CONFLICT (root_id, scope_key) DO NOTHING
		RETURNING `+workColumns,
		wk.RootID, wk.ScopeKey, string(wk.WorkState), wk.SignalSeq,
		wk.ClaimedSignalSeq, sourceStringsNullable(wk.ClaimedSourceSet), reasonStringsNullable(wk.ClaimedReasonSet),
		priorityPtrString(wk.ClaimedPriority), wk.ClaimedFirstSeenAt,
		sourceStrings(wk.PendingSourceSet), reasonStrings(wk.PendingReasonSet),
		priorityPtrString(wk.PendingPriority), wk.PendingFirstSeenAt, wk.PendingNotBefore,
		wk.LastSeenAt, wk.AttemptCount, wk.ConsecutiveFailures,
		wk.LastAttemptStartedAt, wk.LastAttemptFinishedAt, errorClassPtrString(wk.LastErrorClass),
		wk.LastVerifiedAt, wk.LastVerifiedSignalSeq,
		time.Now().UTC())
	out, err := scanWork(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.DirtyScopeWork{}, errRetryMerge
	}
	return out, err
}

func writeWork(ctx context.Context, tx pgx.Tx, wk state.DirtyScopeWork) (state.DirtyScopeWork, error) {
	row := tx.QueryRow(ctx, `
		UPDATE index_dirty_scope_work SET
			work_state=$3, signal_seq=$4,
			claimed_signal_seq=$5, claimed_source_set=$6, claimed_reason_set=$7, claimed_priority=$8, claimed_first_seen_at=$9,
			pending_source_set=$10, pending_reason_set=$11, pending_priority=$12, pending_first_seen_at=$13, pending_not_before=$14,
			last_seen_at=$15, attempt_count=$16, consecutive_failures=$17,
			last_attempt_started_at=$18, last_attempt_finished_at=$19, last_error_class=$20,
			last_verified_at=$21, last_verified_signal_seq=$22,
			updated_at=$23, version=version+1
		WHERE root_id=$1 AND scope_key=$2
		RETURNING `+workColumns,
		wk.RootID, wk.ScopeKey, string(wk.WorkState), wk.SignalSeq,
		wk.ClaimedSignalSeq, sourceStringsNullable(wk.ClaimedSourceSet), reasonStringsNullable(wk.ClaimedReasonSet),
		priorityPtrString(wk.ClaimedPriority), wk.ClaimedFirstSeenAt,
		sourceStrings(wk.PendingSourceSet), reasonStrings(wk.PendingReasonSet),
		priorityPtrString(wk.PendingPriority), wk.PendingFirstSeenAt, wk.PendingNotBefore,
		wk.LastSeenAt, wk.AttemptCount, wk.ConsecutiveFailures,
		wk.LastAttemptStartedAt, wk.LastAttemptFinishedAt, errorClassPtrString(wk.LastErrorClass),
		wk.LastVerifiedAt, wk.LastVerifiedSignalSeq,
		time.Now().UTC())
	return scanWork(row.Scan)
}

func newWorkFromSignal(sig state.DirtySignal, active bool) state.DirtyScopeWork {
	wkState := state.WorkPending
	if !active {
		wkState = state.WorkSuspended
	}
	p := sig.Priority
	seen := sig.SeenAt
	return state.DirtyScopeWork{
		RootID:             sig.RootID,
		ScopeKey:           sig.ScopeKey,
		WorkState:          wkState,
		SignalSeq:          1,
		PendingSourceSet:   []state.TriggerSource{sig.Source},
		PendingReasonSet:   []state.TriggerReason{sig.Reason},
		PendingPriority:    &p,
		PendingFirstSeenAt: &seen,
		PendingNotBefore:   sig.NotBefore,
		LastSeenAt:         seen,
	}
}

// applyMerge implements the frozen P2 merge rules on a loaded (locked) row.
func applyMerge(wk state.DirtyScopeWork, sig state.DirtySignal, active bool) (state.DirtyScopeWork, error) {
	switch wk.WorkState {
	case state.WorkVerified:
		// New epoch: rebuild the pending bucket (no inheritance).
		p := sig.Priority
		seen := sig.SeenAt
		wk.PendingSourceSet = []state.TriggerSource{sig.Source}
		wk.PendingReasonSet = []state.TriggerReason{sig.Reason}
		wk.PendingPriority = &p
		wk.PendingFirstSeenAt = &seen
		wk.PendingNotBefore = sig.NotBefore
		if active {
			wk.WorkState = state.WorkPending
		} else {
			wk.WorkState = state.WorkSuspended
		}

	case state.WorkPending, state.WorkInFlight:
		srcs, err := state.UnionTriggerSources(wk.PendingSourceSet, []state.TriggerSource{sig.Source})
		if err != nil {
			return wk, err
		}
		reasons, err := state.UnionTriggerReasons(wk.PendingReasonSet, []state.TriggerReason{sig.Reason})
		if err != nil {
			return wk, err
		}
		wk.PendingSourceSet = srcs
		wk.PendingReasonSet = reasons
		wk.PendingPriority = mergedPriority(wk.PendingPriority, sig.Priority)
		wk.PendingFirstSeenAt = state.MinTimePtr(wk.PendingFirstSeenAt, &sig.SeenAt)
		if wk.WorkState == state.WorkPending {
			// PENDING may become eligible sooner (min_nonnull).
			wk.PendingNotBefore = state.MinTimePtr(wk.PendingNotBefore, sig.NotBefore)
		} else {
			// IN_FLIGHT: first post-claim signal initializes; later ones min-merge.
			wk.PendingNotBefore = state.MinTimePtr(wk.PendingNotBefore, sig.NotBefore)
		}

	case state.WorkRetryWait, state.WorkBlocked, state.WorkSuspended:
		// Merge provenance; eligibility and state unchanged.
		srcs, err := state.UnionTriggerSources(wk.PendingSourceSet, []state.TriggerSource{sig.Source})
		if err != nil {
			return wk, err
		}
		reasons, err := state.UnionTriggerReasons(wk.PendingReasonSet, []state.TriggerReason{sig.Reason})
		if err != nil {
			return wk, err
		}
		wk.PendingSourceSet = srcs
		wk.PendingReasonSet = reasons
		wk.PendingPriority = mergedPriority(wk.PendingPriority, sig.Priority)
		wk.PendingFirstSeenAt = state.MinTimePtr(wk.PendingFirstSeenAt, &sig.SeenAt)
		// pending_not_before deliberately unchanged.

	default:
		return wk, fmt.Errorf("merge: unsupported work state %q", wk.WorkState)
	}
	wk.SignalSeq++
	wk.LastSeenAt = sig.SeenAt
	return wk, nil
}

func mergedPriority(cur *state.Priority, incoming state.Priority) *state.Priority {
	if cur == nil {
		v := incoming
		return &v
	}
	v := state.MaxPriority(*cur, incoming)
	return &v
}

func mergeSignalTx(ctx context.Context, tx pgx.Tx, sig state.DirtySignal, active bool) (state.DirtyScopeWork, error) {
	wk, found, err := loadWorkForUpdate(ctx, tx, sig.RootID, sig.ScopeKey)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if !found {
		return insertWork(ctx, tx, newWorkFromSignal(sig, active))
	}
	merged, err := applyMerge(wk, sig, active)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	return writeWork(ctx, tx, merged)
}

// MergeSignal merges exactly one trigger into the DirtyScopeWork row, applying
// the frozen P2 rules. Concurrent merges are serialized by the row lock.
func (s *Store) MergeSignal(ctx context.Context, sig state.DirtySignal) (state.DirtyScopeWork, error) {
	if err := state.ValidateScopeKey(sig.ScopeKey); err != nil {
		return state.DirtyScopeWork{}, err
	}
	if err := state.ValidateTriggerSource(sig.Source); err != nil {
		return state.DirtyScopeWork{}, err
	}
	if err := state.ValidateTriggerReason(sig.Reason); err != nil {
		return state.DirtyScopeWork{}, err
	}
	if err := state.ValidatePriority(sig.Priority); err != nil {
		return state.DirtyScopeWork{}, err
	}
	active, err := rootIsActive(ctx, s.pool, sig.RootID)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}

	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return state.DirtyScopeWork{}, err
		}
		out, err := mergeSignalTx(ctx, tx, sig, active)
		if err != nil {
			_ = tx.Rollback(ctx)
			if errors.Is(err, errRetryMerge) {
				lastErr = err
				continue
			}
			return state.DirtyScopeWork{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return state.DirtyScopeWork{}, err
		}
		return out, nil
	}
	return state.DirtyScopeWork{}, fmt.Errorf("merge signal contention: %w", lastErr)
}

// EmitDuePoll atomically re-checks a due watch, merges exactly one
// POLL_SCHEDULE/POSSIBLE_CHANGE signal, and advances the watch schedule.
func (s *Store) EmitDuePoll(ctx context.Context, rootID, scopeKey string, expectedWatchVersion int64, now time.Time) (state.DirtyScopeWork, error) {
	if err := state.ValidateScopeKey(scopeKey); err != nil {
		return state.DirtyScopeWork{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	defer tx.Rollback(ctx)

	w, err := getWatchForUpdate(ctx, tx, rootID, scopeKey, expectedWatchVersion)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	active, err := rootIsActive(ctx, tx, rootID)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if !active {
		return state.DirtyScopeWork{}, fmt.Errorf("emit due poll: root %s is not ACTIVE", rootID)
	}
	if !w.WatchState.Scheduled() {
		return state.DirtyScopeWork{}, fmt.Errorf("emit due poll: watch %s/%s is %s (not scheduled)", rootID, scopeKey, w.WatchState)
	}
	if w.NextDueAt == nil || w.NextDueAt.After(now) {
		return state.DirtyScopeWork{}, fmt.Errorf("emit due poll: watch %s/%s is not due", rootID, scopeKey)
	}
	if w.DeferredUntil != nil && w.DeferredUntil.After(now) {
		return state.DirtyScopeWork{}, fmt.Errorf("emit due poll: watch %s/%s is deferred", rootID, scopeKey)
	}

	sig := state.DirtySignal{
		RootID: rootID, ScopeKey: scopeKey,
		Source: state.SourcePollSchedule, Reason: state.ReasonPossibleChange,
		Priority: w.Priority, SeenAt: now,
	}
	wk, err := mergeSignalTx(ctx, tx, sig, active)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}

	interval := time.Duration(*w.EffectiveIntervalSeconds) * time.Second
	if _, err := tx.Exec(ctx, `
		UPDATE index_scope_watch_state
		SET last_due_at=$3, next_due_at=$4, updated_at=$5, version=version+1
		WHERE root_id=$1 AND scope_key=$2 AND version=$6`,
		rootID, scopeKey, now, now.Add(interval), now, expectedWatchVersion); err != nil {
		return state.DirtyScopeWork{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return state.DirtyScopeWork{}, err
	}
	return wk, nil
}

func getWatchForUpdate(ctx context.Context, tx pgx.Tx, rootID, scopeKey string, expectedVersion int64) (state.ScopeWatchState, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+watchColumns+` FROM index_scope_watch_state
		 WHERE root_id=$1 AND scope_key=$2 AND version=$3 FOR UPDATE`,
		rootID, scopeKey, expectedVersion)
	w, err := scanWatch(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.ScopeWatchState{}, ErrStateCASConflict
	}
	return w, err
}

func watchBumpAttemptStart(ctx context.Context, tx pgx.Tx, rootID, scopeKey string, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE index_scope_watch_state
		SET last_attempt_started_at=$3, updated_at=$4, version=version+1
		WHERE root_id=$1 AND scope_key=$2`, rootID, scopeKey, now, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStateCASConflict
	}
	return nil
}

func watchRecordSuccess(ctx context.Context, tx pgx.Tx, rootID, scopeKey string, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE index_scope_watch_state
		SET last_attempt_finished_at=$3, last_success_at=$3, consecutive_failures=0,
		    last_error_class=NULL, updated_at=$4, version=version+1
		WHERE root_id=$1 AND scope_key=$2`, rootID, scopeKey, now, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStateCASConflict
	}
	return nil
}

func watchRecordFailure(ctx context.Context, tx pgx.Tx, rootID, scopeKey string, class state.ErrorClass, providerFailure bool, now time.Time) error {
	var tag pgconn.CommandTag
	var err error
	if providerFailure {
		tag, err = tx.Exec(ctx, `
			UPDATE index_scope_watch_state
			SET last_attempt_finished_at=$3, consecutive_failures=consecutive_failures+1,
			    last_error_class=$4, updated_at=$5, version=version+1
			WHERE root_id=$1 AND scope_key=$2`, rootID, scopeKey, now, string(class), now)
	} else {
		tag, err = tx.Exec(ctx, `
			UPDATE index_scope_watch_state
			SET last_attempt_finished_at=$3, last_error_class=$4, updated_at=$5, version=version+1
			WHERE root_id=$1 AND scope_key=$2`, rootID, scopeKey, now, string(class), now)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStateCASConflict
	}
	return nil
}

// ClaimWork snapshots the pending bucket into claimed_* atomically.
func (s *Store) ClaimWork(ctx context.Context, rootID, scopeKey string, now time.Time) (state.DirtyScopeWork, error) {
	if err := state.ValidateScopeKey(scopeKey); err != nil {
		return state.DirtyScopeWork{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	defer tx.Rollback(ctx)

	wk, found, err := loadWorkForUpdate(ctx, tx, rootID, scopeKey)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if !found {
		return state.DirtyScopeWork{}, ErrNotFound
	}
	active, err := rootIsActive(ctx, tx, rootID)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}

	if !active {
		if wk.WorkState == state.WorkPending {
			// Preserve the work by suspending it; fail closed.
			wk.WorkState = state.WorkSuspended
			wk.PendingNotBefore = nil
			out, werr := writeWork(ctx, tx, wk)
			if werr != nil {
				return state.DirtyScopeWork{}, werr
			}
			if cerr := tx.Commit(ctx); cerr != nil {
				return state.DirtyScopeWork{}, cerr
			}
			return out, fmt.Errorf("claim: root %s is not ACTIVE; work suspended", rootID)
		}
		return state.DirtyScopeWork{}, fmt.Errorf("claim: root %s is not ACTIVE", rootID)
	}
	if wk.WorkState != state.WorkPending {
		return state.DirtyScopeWork{}, fmt.Errorf("claim: work %s/%s is %s, not PENDING", rootID, scopeKey, wk.WorkState)
	}
	if wk.PendingNotBefore != nil && wk.PendingNotBefore.After(now) {
		return state.DirtyScopeWork{}, fmt.Errorf("claim: work %s/%s is not eligible until %s", rootID, scopeKey, wk.PendingNotBefore)
	}
	if len(wk.PendingSourceSet) == 0 {
		return state.DirtyScopeWork{}, fmt.Errorf("claim: work %s/%s has an empty pending bucket", rootID, scopeKey)
	}

	// Snapshot pending -> claimed.
	seq := wk.SignalSeq
	wk.ClaimedSignalSeq = &seq
	wk.ClaimedSourceSet = wk.PendingSourceSet
	wk.ClaimedReasonSet = wk.PendingReasonSet
	wk.ClaimedPriority = wk.PendingPriority
	wk.ClaimedFirstSeenAt = wk.PendingFirstSeenAt
	// Clear pending.
	wk.PendingSourceSet = nil
	wk.PendingReasonSet = nil
	wk.PendingPriority = nil
	wk.PendingFirstSeenAt = nil
	wk.PendingNotBefore = nil
	wk.WorkState = state.WorkInFlight
	wk.AttemptCount++
	wk.LastAttemptStartedAt = &now

	if state.ContainsTriggerSource(wk.ClaimedSourceSet, state.SourcePollSchedule) {
		if err := watchBumpAttemptStart(ctx, tx, rootID, scopeKey, now); err != nil {
			return state.DirtyScopeWork{}, err
		}
	}

	out, err := writeWork(ctx, tx, wk)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return state.DirtyScopeWork{}, err
	}
	return out, nil
}

// CompleteSuccess records a successful claimed prefix. A stale claim writes nothing.
func (s *Store) CompleteSuccess(ctx context.Context, rootID, scopeKey string, claimedSignalSeq int64, now time.Time) (state.DirtyScopeWork, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	defer tx.Rollback(ctx)

	wk, found, err := loadWorkForUpdate(ctx, tx, rootID, scopeKey)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if !found {
		return state.DirtyScopeWork{}, ErrNotFound
	}
	if wk.WorkState != state.WorkInFlight || wk.ClaimedSignalSeq == nil || *wk.ClaimedSignalSeq != claimedSignalSeq {
		return state.DirtyScopeWork{}, ErrStaleClaim
	}
	claimedSources := wk.ClaimedSourceSet

	// Success bookkeeping (identical in both branches).
	wk.LastAttemptFinishedAt = &now
	wk.LastVerifiedAt = &now
	v := claimedSignalSeq
	wk.LastVerifiedSignalSeq = &v
	wk.ConsecutiveFailures = 0
	wk.LastErrorClass = nil

	if len(wk.PendingSourceSet) > 0 {
		wk.WorkState = state.WorkPending // keep only post-claim pending
	} else {
		wk.WorkState = state.WorkVerified
	}
	releaseClaim(&wk)

	if state.ContainsTriggerSource(claimedSources, state.SourcePollSchedule) {
		if err := watchRecordSuccess(ctx, tx, rootID, scopeKey, now); err != nil {
			return state.DirtyScopeWork{}, err
		}
	}

	out, err := writeWork(ctx, tx, wk)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return state.DirtyScopeWork{}, err
	}
	return out, nil
}

// CompleteFailure applies the frozen failure mapping, re-coalescing provenance.
func (s *Store) CompleteFailure(ctx context.Context, rootID, scopeKey string, claimedSignalSeq int64, class state.ErrorClass, retryNotBefore *time.Time, now time.Time) (state.DirtyScopeWork, error) {
	if err := state.ValidateErrorClass(class); err != nil {
		return state.DirtyScopeWork{}, err
	}
	target := class.TargetWorkState()
	if target == state.WorkRetryWait && retryNotBefore == nil {
		return state.DirtyScopeWork{}, fmt.Errorf("complete failure: RETRY_WAIT requires a retry eligibility")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	defer tx.Rollback(ctx)

	wk, found, err := loadWorkForUpdate(ctx, tx, rootID, scopeKey)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if !found {
		return state.DirtyScopeWork{}, ErrNotFound
	}
	if wk.WorkState != state.WorkInFlight || wk.ClaimedSignalSeq == nil || *wk.ClaimedSignalSeq != claimedSignalSeq {
		return state.DirtyScopeWork{}, ErrStaleClaim
	}
	claimedSources := wk.ClaimedSourceSet

	// Re-coalesce claimed_* + post-claim pending_*, preserving the oldest age.
	srcs, err := state.UnionTriggerSources(wk.ClaimedSourceSet, wk.PendingSourceSet)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	reasons, err := state.UnionTriggerReasons(wk.ClaimedReasonSet, wk.PendingReasonSet)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	wk.PendingSourceSet = srcs
	wk.PendingReasonSet = reasons
	wk.PendingPriority = mergedPriority(wk.PendingPriority, derefPriority(wk.ClaimedPriority))
	wk.PendingFirstSeenAt = state.MinTimePtr(wk.PendingFirstSeenAt, wk.ClaimedFirstSeenAt, wk.LastAttemptStartedAt)

	switch target {
	case state.WorkRetryWait:
		wk.PendingNotBefore = retryNotBefore
	case state.WorkBlocked, state.WorkSuspended:
		wk.PendingNotBefore = nil
	}
	wk.WorkState = target
	releaseClaim(&wk)
	wk.LastAttemptFinishedAt = &now
	ec := class
	wk.LastErrorClass = &ec
	providerFailure := class.IsProviderFailure()
	if providerFailure {
		wk.ConsecutiveFailures++
	}

	if state.ContainsTriggerSource(claimedSources, state.SourcePollSchedule) {
		if err := watchRecordFailure(ctx, tx, rootID, scopeKey, class, providerFailure, now); err != nil {
			return state.DirtyScopeWork{}, err
		}
	}

	out, err := writeWork(ctx, tx, wk)
	if err != nil {
		return state.DirtyScopeWork{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return state.DirtyScopeWork{}, err
	}
	return out, nil
}

func derefPriority(p *state.Priority) state.Priority {
	if p == nil {
		return state.PriorityLow
	}
	return *p
}

func releaseClaim(wk *state.DirtyScopeWork) {
	wk.ClaimedSignalSeq = nil
	wk.ClaimedSourceSet = nil
	wk.ClaimedReasonSet = nil
	wk.ClaimedPriority = nil
	wk.ClaimedFirstSeenAt = nil
}
