package postgres

import (
	"errors"

	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

// ErrStateCASConflict is the operational-state CAS conflict for the P3
// incremental state tables. It is deliberately distinct from the Canonical
// generation ErrCASConflict so callers cannot confuse the two domains.
var ErrStateCASConflict = errors.New("operational state CAS conflict")

// ErrStaleClaim is returned when a completion/failure targets a claim that is no
// longer owned by the caller (stale claimed_signal_seq or no longer IN_FLIGHT).
var ErrStaleClaim = errors.New("operational state stale claim")

// watchColumns is the canonical column list for index_scope_watch_state reads.
const watchColumns = `root_id, scope_key, watch_state, cadence_class,
	effective_interval_seconds, source_set, priority_class,
	last_due_at, last_attempt_started_at, last_attempt_finished_at, last_success_at, next_due_at,
	consecutive_failures, last_error_class, deferred_until,
	created_at, updated_at, version`

const workColumns = `root_id, scope_key, work_state, signal_seq,
	claimed_signal_seq, claimed_source_set, claimed_reason_set, claimed_priority, claimed_first_seen_at,
	pending_source_set, pending_reason_set, pending_priority, pending_first_seen_at, pending_not_before,
	last_seen_at, attempt_count, consecutive_failures,
	last_attempt_started_at, last_attempt_finished_at, last_error_class,
	last_verified_at, last_verified_signal_seq,
	created_at, updated_at, version`

// scanWatch converts a raw row into a normalized ScopeWatchState.
func scanWatch(scan func(dest ...any) error) (state.ScopeWatchState, error) {
	var (
		w       state.ScopeWatchState
		eff     *int64
		srcSet  []string
		lastErr *string
	)
	if err := scan(&w.RootID, &w.ScopeKey, &w.WatchState, &w.CadenceClass, &eff, &srcSet, &w.Priority,
		&w.LastDueAt, &w.LastAttemptStartedAt, &w.LastAttemptFinishedAt, &w.LastSuccessAt, &w.NextDueAt,
		&w.ConsecutiveFailures, &lastErr, &w.DeferredUntil,
		&w.CreatedAt, &w.UpdatedAt, &w.Version); err != nil {
		return state.ScopeWatchState{}, err
	}
	w.EffectiveIntervalSeconds = eff
	if lastErr != nil {
		ec := state.ErrorClass(*lastErr)
		w.LastErrorClass = &ec
	}
	w.SourceSet = make([]state.WatchSource, 0, len(srcSet))
	for _, s := range srcSet {
		w.SourceSet = append(w.SourceSet, state.WatchSource(s))
	}
	return w, nil
}

// scanWork converts a raw row into a normalized DirtyScopeWork.
func scanWork(scan func(dest ...any) error) (state.DirtyScopeWork, error) {
	var (
		wk       state.DirtyScopeWork
		claimedS []string
		claimedR []string
		claimedP *string
		pendingS []string
		pendingR []string
		pendingP *string
		lastErr  *string
	)
	if err := scan(&wk.RootID, &wk.ScopeKey, &wk.WorkState, &wk.SignalSeq,
		&wk.ClaimedSignalSeq, &claimedS, &claimedR, &claimedP, &wk.ClaimedFirstSeenAt,
		&pendingS, &pendingR, &pendingP, &wk.PendingFirstSeenAt, &wk.PendingNotBefore,
		&wk.LastSeenAt, &wk.AttemptCount, &wk.ConsecutiveFailures,
		&wk.LastAttemptStartedAt, &wk.LastAttemptFinishedAt, &lastErr,
		&wk.LastVerifiedAt, &wk.LastVerifiedSignalSeq,
		&wk.CreatedAt, &wk.UpdatedAt, &wk.Version); err != nil {
		return state.DirtyScopeWork{}, err
	}
	wk.ClaimedSourceSet = toTriggerSources(claimedS)
	wk.ClaimedReasonSet = toTriggerReasons(claimedR)
	if claimedP != nil {
		p := state.Priority(*claimedP)
		wk.ClaimedPriority = &p
	}
	wk.PendingSourceSet = toTriggerSources(pendingS)
	wk.PendingReasonSet = toTriggerReasons(pendingR)
	if pendingP != nil {
		p := state.Priority(*pendingP)
		wk.PendingPriority = &p
	}
	if lastErr != nil {
		ec := state.ErrorClass(*lastErr)
		wk.LastErrorClass = &ec
	}
	return wk, nil
}

// toTriggerSources preserves NULL (nil) vs empty ({}).
func toTriggerSources(in []string) []state.TriggerSource {
	if in == nil {
		return nil
	}
	out := make([]state.TriggerSource, 0, len(in))
	for _, s := range in {
		out = append(out, state.TriggerSource(s))
	}
	return out
}

// toTriggerReasons preserves NULL (nil) vs empty ({}).
func toTriggerReasons(in []string) []state.TriggerReason {
	if in == nil {
		return nil
	}
	out := make([]state.TriggerReason, 0, len(in))
	for _, r := range in {
		out = append(out, state.TriggerReason(r))
	}
	return out
}

func sourceStrings(in []state.TriggerSource) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, string(s))
	}
	return out
}

func reasonStrings(in []state.TriggerReason) []string {
	out := make([]string, 0, len(in))
	for _, r := range in {
		out = append(out, string(r))
	}
	return out
}

// sourceStringsNullable maps a trigger-source set to text[]; nil stays NULL.
// Used for claimed_* (absent outside IN_FLIGHT).
func sourceStringsNullable(in []state.TriggerSource) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, string(s))
	}
	return out
}

// reasonStringsNullable maps a reason set to text[]; nil stays NULL.
func reasonStringsNullable(in []state.TriggerReason) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, r := range in {
		out = append(out, string(r))
	}
	return out
}

func watchSourceStrings(in []state.WatchSource) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, string(s))
	}
	return out
}

func priorityPtrString(p *state.Priority) *string {
	if p == nil {
		return nil
	}
	s := string(*p)
	return &s
}

func errorClassPtrString(e *state.ErrorClass) *string {
	if e == nil {
		return nil
	}
	s := string(*e)
	return &s
}
