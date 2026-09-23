package domain

// ResourcePresence is the only consumer-visible presence signal (doc C V1/V2, M6).
type ResourcePresence string

const (
	ResourcePresent ResourcePresence = "PRESENT"
	ResourceRemoved ResourcePresence = "REMOVED"
)

// RemovalEvidenceState is Kernel-internal and MUST NOT be exposed to consumers (M6, Blocker F).
type RemovalEvidenceState string

const (
	RemovalEvidenceNone                       RemovalEvidenceState = "NONE"
	RemovalEvidenceMissingConfirmedByComplete RemovalEvidenceState = "MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT"
	RemovalEvidenceCandidate                  RemovalEvidenceState = "REMOVAL_CANDIDATE"
)

// RootLifecycleState (doc A T1, C-R2). DELETED is a root-level-only tombstone (C-R4).
type RootLifecycleState string

const (
	RootNew        RootLifecycleState = "NEW"
	RootActive     RootLifecycleState = "ACTIVE"
	RootDeprecated RootLifecycleState = "DEPRECATED"
	RootDeleted    RootLifecycleState = "DELETED"
)

func (s RootLifecycleState) CanTransitionTo(next RootLifecycleState) bool {
	switch s {
	case RootNew:
		return next == RootActive
	case RootActive:
		return next == RootDeprecated || next == RootDeleted
	case RootDeprecated:
		return next == RootDeleted
	default:
		return false
	}
}

// SnapshotLifecycleState (doc A T5, Gate 1B Sec 1.1).
type SnapshotLifecycleState string

const (
	SnapshotDraft      SnapshotLifecycleState = "DRAFT"
	SnapshotSubmitted  SnapshotLifecycleState = "SUBMITTED"
	SnapshotEvaluated  SnapshotLifecycleState = "EVALUATED"
	SnapshotReconciled SnapshotLifecycleState = "RECONCILED"
	SnapshotRejected   SnapshotLifecycleState = "REJECTED"
	SnapshotRetired    SnapshotLifecycleState = "RETIRED"
)

// AcceptanceState is the Kernel-owned completeness verdict and the sole authority
// for destructive reconcile eligibility (doc A T5, Gate 1B Sec 3.4, INV-004).
type AcceptanceState string

const (
	AcceptanceComplete   AcceptanceState = "COMPLETE"
	AcceptancePartial    AcceptanceState = "PARTIAL"
	AcceptanceFailed     AcceptanceState = "FAILED"
	AcceptanceStale      AcceptanceState = "STALE"
	AcceptanceSuspicious AcceptanceState = "SUSPICIOUS"
)

// TraversalStatus is a contract-level Collector signal, not a completeness proof (doc E Sec 2.2).
type TraversalStatus string

const (
	TraversalSuccess     TraversalStatus = "SUCCESS"
	TraversalPartial     TraversalStatus = "PARTIAL"
	TraversalFailed      TraversalStatus = "FAILED"
	TraversalInterrupted TraversalStatus = "INTERRUPTED"
)

// FreshnessEvidence (doc E Sec 2.3). Missing is normalized to FreshnessUnknown.
type FreshnessEvidence string

const (
	FreshDirect      FreshnessEvidence = "FRESH_DIRECT"
	FreshRefreshed   FreshnessEvidence = "FRESH_REFRESHED"
	CachedFresh      FreshnessEvidence = "CACHED_FRESH"
	FreshnessStale   FreshnessEvidence = "STALE"
	FreshnessUnknown FreshnessEvidence = "UNKNOWN"
)

// FailureVisibility is the Collector completeness assurance class (doc E Sec 2.3, Gate 1B C-9a).
type FailureVisibility string

const (
	StrongFailureVisibility  FailureVisibility = "STRONG_FAILURE_VISIBILITY"
	WeakFailureVisibility    FailureVisibility = "WEAK_FAILURE_VISIBILITY"
	UnknownFailureVisibility FailureVisibility = "UNKNOWN_FAILURE_VISIBILITY"
)

// SnapshotIdentityKind is one of the two frozen identity forms (doc A Sec 3.8).
type SnapshotIdentityKind string

const (
	IdentityRevisionToken       SnapshotIdentityKind = "REVISION_TOKEN"
	IdentityDeterministicDigest SnapshotIdentityKind = "DETERMINISTIC_DIGEST"
)

// EventType is the closed set of seven canonical journal event types (doc D U3, C-J4).
type EventType string

const (
	EventResourceAdded   EventType = "resource-added"
	EventResourceUpdated EventType = "resource-updated"
	EventResourceRenamed EventType = "resource-renamed"
	EventResourceMoved   EventType = "resource-moved"
	EventResourceRemoved EventType = "resource-removed"
	EventRootDeprecated  EventType = "root-deprecated"
	EventRootDeleted     EventType = "root-deleted"
)

// AdmissionStatus (doc A T7, doc B RC7). Everything except PENDING is terminal.
type AdmissionStatus string

const (
	AdmissionPending    AdmissionStatus = "PENDING"
	AdmissionApplied    AdmissionStatus = "APPLIED"
	AdmissionNoop       AdmissionStatus = "NOOP"
	AdmissionRejected   AdmissionStatus = "REJECTED"
	AdmissionStaleInput AdmissionStatus = "STALE_INPUT"
	AdmissionFailed     AdmissionStatus = "FAILED"
)

func (s AdmissionStatus) IsTerminal() bool { return s != AdmissionPending }

// ReconcileOutcome (doc A T11).
type ReconcileOutcome string

const (
	ReconcileReconciled ReconcileOutcome = "RECONCILED"
	ReconcileNoop       ReconcileOutcome = "NOOP"
	ReconcileRejected   ReconcileOutcome = "REJECTED"
	ReconcileStaleInput ReconcileOutcome = "STALE_INPUT"
	ReconcileFailed     ReconcileOutcome = "FAILED"
)

// ProviderIdentityAssurance (doc E Sec 2.3, Gate 1B Domain R1).
type ProviderIdentityAssurance string

const (
	IdentityStableWithinScope ProviderIdentityAssurance = "STABLE_WITHIN_SCOPE"
	IdentityUnverified        ProviderIdentityAssurance = "UNVERIFIED"
	IdentityUnstable          ProviderIdentityAssurance = "UNSTABLE"
	IdentityUnavailable       ProviderIdentityAssurance = "UNAVAILABLE"
)

// ScopeShrinkCorroboration is Kernel-derived during SUBMITTED -> EVALUATED and then
// immutable; it is never Collector-writable (doc A Sec 3.5, Gate 1B Sec 3.1.3).
type ScopeShrinkCorroboration string

const (
	ShrinkNone         ScopeShrinkCorroboration = "NONE"
	ShrinkCorroborated ScopeShrinkCorroboration = "CORROBORATED"
	ShrinkContradicted ScopeShrinkCorroboration = "CONTRADICTED"
)

// CompletenessFlag is a non-authoritative Collector hint/audit value only (doc A Sec 3.5 erratum).
type CompletenessFlag string

const (
	CompletenessFlagComplete CompletenessFlag = "COMPLETE"
	CompletenessFlagPartial  CompletenessFlag = "PARTIAL"
)
