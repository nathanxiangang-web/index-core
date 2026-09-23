package domain

import "time"

// ResourceRoot is T1 index_root (doc A Sec 3.1). root_id is immutable and never reused.
type ResourceRoot struct {
	RootID             string
	ScopeDescriptor    []byte
	OwningCollectorRef *string
	LifecycleState     RootLifecycleState
	CurrentGeneration  int64
	LatestAdmissionSeq int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Generation is T2 index_generation (doc A Sec 3.2).
type Generation struct {
	RootID                 string
	GenerationNumber       int64
	ProducedBySnapshotID   *string
	ProducedByAdmissionSeq *int64
	ProducedAt             time.Time
	Summary                []byte
}

// CanonicalResource is T3 index_canonical_resource (doc A Sec 3.3).
// RemovalEvidenceState is Kernel-internal and MUST NOT be exposed to consumers (M6).
type CanonicalResource struct {
	ResourceID                 string
	RootID                     string
	IntroducedAtGeneration     int64
	LastConfirmedGeneration    int64
	ResourcePresence           ResourcePresence
	RemovalEvidenceState       RemovalEvidenceState
	MissingSince               *time.Time
	ConsecutiveCompleteMissing int32
	// MissingFirstSnapshotID records the accepted Snapshot that first produced
	// MISSING evidence. It is a Store-internal realization used to prove that a
	// later, independently admitted Snapshot confirmed removal (V2c); it is not
	// part of the external Domain semantics.
	MissingFirstSnapshotID *string
	CanonicalPath          *string
	ParentResourceID       *string
	Name                   *string
	IsDir                  *bool
	Size                   *int64
	Mtime                  *time.Time
	ContentHash            *string
	HashAlgorithm          *string
	ContentType            *string
	CurrentAttributes      []byte
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// IdentityEvidenceObservation is T4 index_identity_evidence_observation (append-only, authoritative).
type IdentityEvidenceObservation struct {
	ObservationID             int64
	ResourceID                string
	SnapshotID                string
	SourceRef                 *string
	ObservedAt                time.Time
	ProviderObjectID          *string
	ProviderObjectIDScope     *string
	ProviderIdentityAssurance ProviderIdentityAssurance
	ContentHash               *string
	HashAlgorithm             *string
	ObservedPath              *string
	ObservedParentRef         *string
	Size                      *int64
	Mtime                     *time.Time
	IsDir                     bool
	ExtraEvidence             []byte
}

// Snapshot is T5 index_snapshot (doc A Sec 3.5). Collector evidence is immutable after
// submission; scope_shrink_corroboration and acceptance_state advance with lifecycle only.
type Snapshot struct {
	SnapshotID                     string
	RootID                         string
	Provenance                     []byte
	ObservedAt                     time.Time
	StartedAt                      *time.Time
	FinishedAt                     *time.Time
	TraversalStatus                TraversalStatus
	ErrorSummary                   []byte
	SkippedScopes                  []byte
	SkippedScopesKnownEmpty        bool
	FreshnessEvidence              *FreshnessEvidence
	CollectorCompletenessAssurance *FailureVisibility
	ScopeShrinkCorroboration       *ScopeShrinkCorroboration
	CompletenessFlag               CompletenessFlag
	AcceptanceState                *AcceptanceState
	LifecycleState                 SnapshotLifecycleState
	EntryCount                     *int64
	ByteCount                      *int64
	GenerationHint                 *string
	CreatedAt                      time.Time
}

// SnapshotEntry is T6 index_snapshot_entry (audit/replay; CANDIDATE persistence).
type SnapshotEntry struct {
	SnapshotID            string
	EntryLocalID          string
	Name                  string
	ParentRef             string
	IsDir                 bool
	Size                  *int64
	Mtime                 *time.Time
	ContentHash           *string
	HashAlgorithm         *string
	ProviderObjectID      *string
	ProviderObjectIDScope *string
	ContentType           *string
	// ProviderIdentityAssurance is an optional adapter-supplied capability hint
	// for this entry's provider_object_id. Absent means UNVERIFIED. PoC CANDIDATE.
	ProviderIdentityAssurance *ProviderIdentityAssurance
	ExtraEvidence             []byte
}

// Admission is T7 index_admission (doc A Sec 3.7, doc B Sec 1.1/1.2).
type Admission struct {
	RootID            string
	AdmissionSeq      int64
	SnapshotID        string
	AdmittedAt        time.Time
	Status            AdmissionStatus
	AppliedGeneration *int64
	ClaimedBy         *string
	ClaimedAt         *time.Time
	LeaseExpiresAt    *time.Time
}

// AppliedSnapshot is one row of T8 index_applied_snapshot (append-only application history).
type AppliedSnapshot struct {
	RootID                    string
	SnapshotIdentityKind      SnapshotIdentityKind
	SnapshotIdentityNamespace string
	SnapshotIdentityVersion   string
	SnapshotIdentityValue     string
	SnapshotID                string
	AppliedGeneration         int64
	AppliedAdmissionSeq       int64
	AppliedAt                 time.Time
}

// JournalEvent is T9 index_journal_event (append-only).
type JournalEvent struct {
	RootID             string
	EventSeq           int64
	EventID            *int64
	GenerationNumber   int64
	IntraGenerationSeq int32
	EventType          EventType
	ResourceID         *string
	Payload            []byte
	CommittedAt        time.Time
}

// ReconcileResult is T11 index_reconcile_result (conflict/outcome records; never journal events).
type ReconcileResult struct {
	ReconcileID      string
	RootID           string
	AdmissionSeq     int64
	GenerationNumber *int64
	Outcome          ReconcileOutcome
	Counts           []byte
	Conflicts        []byte
	RejectionReason  *string
	CreatedAt        time.Time
}
