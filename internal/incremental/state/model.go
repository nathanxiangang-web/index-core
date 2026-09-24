// Package state holds provider-neutral operational state for the incremental
// scope-watching prototype (P3, Issue #72).
//
// This is NOT Canonical Domain. It models the accepted P2 ScopeWatchState /
// DirtyScopeWork contract and never writes Canonical Inventory, generation,
// Journal, removal evidence, Snapshot, or admission.
package state

import "time"

// WatchState is the recurring scheduling class of a watched scope.
type WatchState string

const (
	WatchHot      WatchState = "HOT"
	WatchWarm     WatchState = "WARM"
	WatchCold     WatchState = "COLD"
	WatchDisabled WatchState = "DISABLED"
)

// Scheduled reports whether the state participates in periodic polling.
// Only HOT/WARM are periodically scheduled (P2: COLD is retained-state-only).
func (w WatchState) Scheduled() bool {
	return w == WatchHot || w == WatchWarm
}

// WorkState is the durable state of a DirtyScopeWork item.
type WorkState string

const (
	WorkPending   WorkState = "PENDING"
	WorkInFlight  WorkState = "IN_FLIGHT"
	WorkVerified  WorkState = "VERIFIED"
	WorkRetryWait WorkState = "RETRY_WAIT"
	WorkBlocked   WorkState = "BLOCKED"
	WorkSuspended WorkState = "SUSPENDED"
)

// Priority is a scheduling hint.
type Priority string

const (
	PriorityUrgent Priority = "URGENT"
	PriorityHigh   Priority = "HIGH"
	PriorityNormal Priority = "NORMAL"
	PriorityLow    Priority = "LOW"
)

// priorityRank orders priorities so a merge can take the maximum.
func priorityRank(p Priority) int {
	switch p {
	case PriorityUrgent:
		return 4
	case PriorityHigh:
		return 3
	case PriorityNormal:
		return 2
	case PriorityLow:
		return 1
	default:
		return 0
	}
}

// MaxPriority returns the higher of two priorities (a > b means a wins).
func MaxPriority(a, b Priority) Priority {
	if priorityRank(a) >= priorityRank(b) {
		return a
	}
	return b
}

// TriggerSource is where a dirty signal came from.
type TriggerSource string

const (
	SourcePollSchedule       TriggerSource = "POLL_SCHEDULE"
	SourceMutationHint       TriggerSource = "MUTATION_HINT"
	SourceManualOperator     TriggerSource = "MANUAL_OPERATOR"
	SourceProviderEvent      TriggerSource = "PROVIDER_EVENT"
	SourceRecovery           TriggerSource = "RECOVERY"
	SourceFullVerifyBackstop TriggerSource = "FULL_VERIFY_BACKSTOP"
)

// WatchSource is watch-policy provenance. It never contains POLL_SCHEDULE.
type WatchSource string

const (
	WatchSourceOperatorPolicy WatchSource = "OPERATOR_POLICY"
	WatchSourceAdaptivePolicy WatchSource = "ADAPTIVE_POLICY"
	WatchSourceMigrated       WatchSource = "MIGRATED"
	WatchSourceBackstopEnroll WatchSource = "BACKSTOP_ENROLL"
)

// TriggerReason is why verification is wanted.
type TriggerReason string

const (
	ReasonPossibleChange    TriggerReason = "POSSIBLE_CHANGE"
	ReasonDeleteHint        TriggerReason = "DELETE_HINT"
	ReasonMoveUncertain     TriggerReason = "MOVE_UNCERTAIN"
	ReasonMetadataUncertain TriggerReason = "METADATA_UNCERTAIN"
	ReasonManualVerify      TriggerReason = "MANUAL_VERIFY"
	ReasonDriftVerify       TriggerReason = "DRIFT_VERIFY"
	ReasonRetry             TriggerReason = "RETRY"
)

// ErrorClass is the provider-neutral failure classification.
type ErrorClass string

const (
	ErrorTransientProvider ErrorClass = "TRANSIENT_PROVIDER"
	ErrorThrottled         ErrorClass = "THROTTLED"
	ErrorAuthOrPermission  ErrorClass = "AUTH_OR_PERMISSION"
	ErrorScopeTooLarge     ErrorClass = "SCOPE_TOO_LARGE"
	ErrorInvalidScope      ErrorClass = "INVALID_SCOPE"
	ErrorRootInactive      ErrorClass = "ROOT_INACTIVE"
	ErrorConfigInvalid     ErrorClass = "CONFIG_INVALID"
	ErrorInternal          ErrorClass = "INTERNAL"
)

// IsProviderFailure reports whether the class counts as a provider failure
// (it increments failure counters). ROOT_INACTIVE is lifecycle, not a failure.
func (e ErrorClass) IsProviderFailure() bool {
	return e != ErrorRootInactive
}

// TargetWorkState maps a failure class to the frozen P2 work-state transition.
func (e ErrorClass) TargetWorkState() WorkState {
	switch e {
	case ErrorTransientProvider, ErrorThrottled, ErrorInternal:
		return WorkRetryWait
	case ErrorAuthOrPermission, ErrorScopeTooLarge, ErrorInvalidScope, ErrorConfigInvalid:
		return WorkBlocked
	case ErrorRootInactive:
		return WorkSuspended
	default:
		return WorkBlocked
	}
}

// ScopeWatchState is the durable recurring-watch model (P2 Sec 4).
type ScopeWatchState struct {
	RootID                   string
	ScopeKey                 string
	WatchState               WatchState
	CadenceClass             string
	EffectiveIntervalSeconds *int64
	SourceSet                []WatchSource
	Priority                 Priority

	LastDueAt             *time.Time
	LastAttemptStartedAt  *time.Time
	LastAttemptFinishedAt *time.Time
	LastSuccessAt         *time.Time
	NextDueAt             *time.Time
	ConsecutiveFailures   int64
	LastErrorClass        *ErrorClass
	DeferredUntil         *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
	Version   int64
}

// Due reports whether the watch is due at now. Only HOT/WARM can be due and a
// deferred watch is not due.
func (w ScopeWatchState) Due(now time.Time) bool {
	if !w.WatchState.Scheduled() || w.NextDueAt == nil {
		return false
	}
	if w.NextDueAt.After(now) {
		return false
	}
	if w.DeferredUntil != nil && w.DeferredUntil.After(now) {
		return false
	}
	return true
}

// DirtyScopeWork is the durable coalesced verification intent (P2 Sec 5).
type DirtyScopeWork struct {
	RootID    string
	ScopeKey  string
	WorkState WorkState
	SignalSeq int64

	ClaimedSignalSeq   *int64
	ClaimedSourceSet   []TriggerSource
	ClaimedReasonSet   []TriggerReason
	ClaimedPriority    *Priority
	ClaimedFirstSeenAt *time.Time

	PendingSourceSet   []TriggerSource
	PendingReasonSet   []TriggerReason
	PendingPriority    *Priority
	PendingFirstSeenAt *time.Time
	PendingNotBefore   *time.Time

	LastSeenAt            time.Time
	AttemptCount          int64
	ConsecutiveFailures   int64
	LastAttemptStartedAt  *time.Time
	LastAttemptFinishedAt *time.Time
	LastErrorClass        *ErrorClass
	LastVerifiedAt        *time.Time
	LastVerifiedSignalSeq *int64

	CreatedAt time.Time
	UpdatedAt time.Time
	Version   int64
}

// DirtySignal is one provider-neutral trigger to merge.
type DirtySignal struct {
	RootID    string
	ScopeKey  string
	Source    TriggerSource
	Reason    TriggerReason
	Priority  Priority
	NotBefore *time.Time
	SeenAt    time.Time
}
