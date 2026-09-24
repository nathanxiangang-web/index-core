package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

func validateWatchForWrite(w state.ScopeWatchState) error {
	switch w.WatchState {
	case state.WatchHot, state.WatchWarm:
		if w.EffectiveIntervalSeconds == nil || *w.EffectiveIntervalSeconds <= 0 {
			return fmt.Errorf("watch %s/%s: %v requires a positive interval", w.RootID, w.ScopeKey, w.WatchState)
		}
		if w.NextDueAt == nil {
			return fmt.Errorf("watch %s/%s: %v requires next_due_at", w.RootID, w.ScopeKey, w.WatchState)
		}
	case state.WatchCold, state.WatchDisabled:
		if w.EffectiveIntervalSeconds != nil || w.NextDueAt != nil {
			return fmt.Errorf("watch %s/%s: %v must not carry interval/next_due_at", w.RootID, w.ScopeKey, w.WatchState)
		}
	default:
		return fmt.Errorf("watch %s/%s: invalid watch state %q", w.RootID, w.ScopeKey, w.WatchState)
	}
	if err := state.ValidatePriority(w.Priority); err != nil {
		return err
	}
	if w.LastErrorClass != nil {
		if err := state.ValidateErrorClass(*w.LastErrorClass); err != nil {
			return err
		}
	}
	if w.ConsecutiveFailures < 0 {
		return fmt.Errorf("watch %s/%s: negative consecutive_failures", w.RootID, w.ScopeKey)
	}
	return nil
}

// CreateWatch inserts a new watch row. version starts at 1 (DB default).
func (s *Store) CreateWatch(ctx context.Context, w state.ScopeWatchState) (state.ScopeWatchState, error) {
	if err := state.ValidateScopeKey(w.ScopeKey); err != nil {
		return state.ScopeWatchState{}, err
	}
	if err := validateWatchForWrite(w); err != nil {
		return state.ScopeWatchState{}, err
	}
	srcs, err := state.NormalizeWatchSources(w.SourceSet)
	if err != nil {
		return state.ScopeWatchState{}, err
	}
	now := time.Now().UTC()
	row := s.pool.QueryRow(ctx, `
		INSERT INTO index_scope_watch_state (
			root_id, scope_key, watch_state, cadence_class, effective_interval_seconds,
			source_set, priority_class,
			last_due_at, last_attempt_started_at, last_attempt_finished_at, last_success_at, next_due_at,
			consecutive_failures, last_error_class, deferred_until,
			created_at, updated_at, version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16,1)
		RETURNING `+watchColumns,
		w.RootID, w.ScopeKey, string(w.WatchState), w.CadenceClass, w.EffectiveIntervalSeconds,
		watchSourceStrings(srcs), string(w.Priority),
		w.LastDueAt, w.LastAttemptStartedAt, w.LastAttemptFinishedAt, w.LastSuccessAt, w.NextDueAt,
		w.ConsecutiveFailures, errorClassPtrString(w.LastErrorClass), w.DeferredUntil,
		now)
	return scanWatch(row.Scan)
}

// GetWatch reads one watch row. Missing rows return ErrNotFound.
func (s *Store) GetWatch(ctx context.Context, rootID, scopeKey string) (state.ScopeWatchState, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+watchColumns+` FROM index_scope_watch_state WHERE root_id=$1 AND scope_key=$2`,
		rootID, scopeKey)
	w, err := scanWatch(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.ScopeWatchState{}, ErrNotFound
	}
	return w, err
}

// CASUpdateWatch replaces a watch row's mutable fields under a version CAS.
// A stale expected version returns ErrStateCASConflict with no partial write.
func (s *Store) CASUpdateWatch(ctx context.Context, w state.ScopeWatchState, expectedVersion int64) (state.ScopeWatchState, error) {
	if err := state.ValidateScopeKey(w.ScopeKey); err != nil {
		return state.ScopeWatchState{}, err
	}
	if err := validateWatchForWrite(w); err != nil {
		return state.ScopeWatchState{}, err
	}
	srcs, err := state.NormalizeWatchSources(w.SourceSet)
	if err != nil {
		return state.ScopeWatchState{}, err
	}
	now := time.Now().UTC()
	row := s.pool.QueryRow(ctx, `
		UPDATE index_scope_watch_state SET
			watch_state=$3, cadence_class=$4, effective_interval_seconds=$5,
			source_set=$6, priority_class=$7,
			last_due_at=$8, last_attempt_started_at=$9, last_attempt_finished_at=$10,
			last_success_at=$11, next_due_at=$12,
			consecutive_failures=$13, last_error_class=$14, deferred_until=$15,
			updated_at=$16, version=version+1
		WHERE root_id=$1 AND scope_key=$2 AND version=$17
		RETURNING `+watchColumns,
		w.RootID, w.ScopeKey, string(w.WatchState), w.CadenceClass, w.EffectiveIntervalSeconds,
		watchSourceStrings(srcs), string(w.Priority),
		w.LastDueAt, w.LastAttemptStartedAt, w.LastAttemptFinishedAt, w.LastSuccessAt, w.NextDueAt,
		w.ConsecutiveFailures, errorClassPtrString(w.LastErrorClass), w.DeferredUntil,
		now, expectedVersion)
	out, err := scanWatch(row.Scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return state.ScopeWatchState{}, ErrStateCASConflict
	}
	return out, err
}

// ListDueWatches returns ACTIVE-root HOT/WARM watches that are due at now and not
// deferred, in a deterministic order.
func (s *Store) ListDueWatches(ctx context.Context, now time.Time, limit int) ([]state.ScopeWatchState, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT w.*
		FROM index_scope_watch_state w
		JOIN index_root r ON r.root_id = w.root_id
		WHERE r.lifecycle_state = 'ACTIVE'
		  AND w.watch_state IN ('HOT','WARM')
		  AND w.next_due_at IS NOT NULL
		  AND w.next_due_at <= $1
		  AND (w.deferred_until IS NULL OR w.deferred_until <= $1)
		ORDER BY w.next_due_at ASC, w.root_id ASC, w.scope_key ASC
		LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []state.ScopeWatchState
	for rows.Next() {
		w, err := scanWatch(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
