package state

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrInvalidEnum is returned when a value is outside the frozen P2 enum set.
var ErrInvalidEnum = errors.New("invalid enum value")

// ErrInvalidSet is returned when a set input is malformed.
var ErrInvalidSet = errors.New("invalid set")

func validateWatchState(s WatchState) error {
	switch s {
	case WatchHot, WatchWarm, WatchCold, WatchDisabled:
		return nil
	default:
		return fmt.Errorf("%w: watch state %q", ErrInvalidEnum, s)
	}
}

func validateWorkState(s WorkState) error {
	switch s {
	case WorkPending, WorkInFlight, WorkVerified, WorkRetryWait, WorkBlocked, WorkSuspended:
		return nil
	default:
		return fmt.Errorf("%w: work state %q", ErrInvalidEnum, s)
	}
}

// ValidatePriority checks a priority value.
func ValidatePriority(p Priority) error {
	switch p {
	case PriorityUrgent, PriorityHigh, PriorityNormal, PriorityLow:
		return nil
	default:
		return fmt.Errorf("%w: priority %q", ErrInvalidEnum, p)
	}
}

// ValidateTriggerSource checks a dirty-signal source.
func ValidateTriggerSource(s TriggerSource) error {
	switch s {
	case SourcePollSchedule, SourceMutationHint, SourceManualOperator,
		SourceProviderEvent, SourceRecovery, SourceFullVerifyBackstop:
		return nil
	default:
		return fmt.Errorf("%w: trigger source %q", ErrInvalidEnum, s)
	}
}

// ValidateWatchSource checks a watch-policy provenance source.
func ValidateWatchSource(s WatchSource) error {
	switch s {
	case WatchSourceOperatorPolicy, WatchSourceAdaptivePolicy,
		WatchSourceMigrated, WatchSourceBackstopEnroll:
		return nil
	default:
		return fmt.Errorf("%w: watch source %q", ErrInvalidEnum, s)
	}
}

// ValidateTriggerReason checks a dirty-signal reason.
func ValidateTriggerReason(r TriggerReason) error {
	switch r {
	case ReasonPossibleChange, ReasonDeleteHint, ReasonMoveUncertain,
		ReasonMetadataUncertain, ReasonManualVerify, ReasonDriftVerify, ReasonRetry:
		return nil
	default:
		return fmt.Errorf("%w: trigger reason %q", ErrInvalidEnum, r)
	}
}

// ValidateErrorClass checks a failure class.
func ValidateErrorClass(e ErrorClass) error {
	switch e {
	case ErrorTransientProvider, ErrorThrottled, ErrorAuthOrPermission,
		ErrorScopeTooLarge, ErrorInvalidScope, ErrorRootInactive,
		ErrorConfigInvalid, ErrorInternal:
		return nil
	default:
		return fmt.Errorf("%w: error class %q", ErrInvalidEnum, e)
	}
}

// NormalizeWatchSources validates, de-duplicates and deterministically sorts a
// watch-policy source set.
func NormalizeWatchSources(in []WatchSource) ([]WatchSource, error) {
	if in == nil {
		return nil, nil
	}
	seen := map[WatchSource]bool{}
	out := make([]WatchSource, 0, len(in))
	for _, s := range in {
		if err := ValidateWatchSource(s); err != nil {
			return nil, err
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// NormalizeTriggerSources validates, de-duplicates and sorts a dirty source set.
func NormalizeTriggerSources(in []TriggerSource) ([]TriggerSource, error) {
	if in == nil {
		return nil, nil
	}
	seen := map[TriggerSource]bool{}
	out := make([]TriggerSource, 0, len(in))
	for _, s := range in {
		if err := ValidateTriggerSource(s); err != nil {
			return nil, err
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// NormalizeTriggerReasons validates, de-duplicates and sorts a reason set.
func NormalizeTriggerReasons(in []TriggerReason) ([]TriggerReason, error) {
	if in == nil {
		return nil, nil
	}
	seen := map[TriggerReason]bool{}
	out := make([]TriggerReason, 0, len(in))
	for _, r := range in {
		if err := ValidateTriggerReason(r); err != nil {
			return nil, err
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// UnionTriggerSources returns the union of two source sets, normalized.
func UnionTriggerSources(a, b []TriggerSource) ([]TriggerSource, error) {
	return NormalizeTriggerSources(append(append([]TriggerSource{}, a...), b...))
}

// UnionTriggerReasons returns the union of two reason sets, normalized.
func UnionTriggerReasons(a, b []TriggerReason) ([]TriggerReason, error) {
	return NormalizeTriggerReasons(append(append([]TriggerReason{}, a...), b...))
}

// ContainsTriggerSource reports whether the set contains s.
func ContainsTriggerSource(set []TriggerSource, s TriggerSource) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// MinTimePtr returns the earliest non-nil instant, or nil if all are nil.
// It is used for the P2 "min_nonnull eligibility" and "oldest first_seen" rules.
func MinTimePtr(ts ...*time.Time) *time.Time {
	var out *time.Time
	for _, t := range ts {
		if t == nil {
			continue
		}
		if out == nil || t.Before(*out) {
			v := *t
			out = &v
		}
	}
	return out
}

// MaxTimePtr returns the latest non-nil instant, or nil if all are nil. It keeps
// the later barrier when combining eligibility (never earlier).
func MaxTimePtr(ts ...*time.Time) *time.Time {
	var out *time.Time
	for _, t := range ts {
		if t == nil {
			continue
		}
		if out == nil || t.After(*out) {
			v := *t
			out = &v
		}
	}
	return out
}
