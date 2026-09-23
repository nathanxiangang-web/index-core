package reconcile

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// Config carries per-root reconcile policy thresholds. These are Gate 1C
// runtime/CANDIDATE values, not new frozen architecture.
type Config struct {
	// RemovalGracePeriod is the interval after MISSING during which a resource
	// MUST NOT be confirmed removed (frozen invariant:
	// RemovalGracePeriod >= MoveRecognitionHorizon).
	RemovalGracePeriod time.Duration
	// MoveRecognitionHorizon is the interval after MISSING during which R3/R5 may
	// still recognize a rename/move. Zero disables move recognition.
	MoveRecognitionHorizon time.Duration
	// MinConsecutiveCompleteMissing is C3 (>=1).
	MinConsecutiveCompleteMissing int
	// MinIndependentConfirmations is V2c: the number of later, independently
	// admitted COMPLETE observations required beyond the first MISSING. Default 1.
	MinIndependentConfirmations int
	// NewID assigns a Kernel-owned resource_id for ADD. Injectable for determinism.
	NewID func() string
}

func (c Config) newID() string {
	if c.NewID != nil {
		return c.NewID()
	}
	return NewUUID()
}

func (c Config) minConsecutive() int {
	if c.MinConsecutiveCompleteMissing < 1 {
		return 1
	}
	return c.MinConsecutiveCompleteMissing
}

func (c Config) minIndependent() int {
	if c.MinIndependentConfirmations < 1 {
		return 1
	}
	return c.MinIndependentConfirmations
}

// NewUUID returns a random RFC-4122 v4 UUID string (Kernel-assigned identifier).
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// PriorResource is a canonical resource enriched with the latest identity
// evidence, which is what identity matching (Gate 1B Domain R0..R11) needs.
// It embeds domain.CanonicalResource, which includes the Store-internal
// MissingFirstSnapshotID used for independence (B2).
type PriorResource struct {
	domain.CanonicalResource
	ProviderObjectID      *string
	ProviderObjectIDScope *string
	ProviderIDAssurance   domain.ProviderIdentityAssurance
}

func (p *PriorResource) isRemoved() bool { return p.ResourcePresence == domain.ResourceRemoved }
func (p *PriorResource) isPresent() bool { return p.ResourcePresence == domain.ResourcePresent }

// hasMissingEvidence reports a live resource carrying MISSING/removal evidence
// (it is still PRESENT, not a tombstone).
func (p *PriorResource) hasMissingEvidence() bool {
	return p.isPresent() && p.RemovalEvidenceState != domain.RemovalEvidenceNone
}

func (p *PriorResource) canonicalPath() string { return derefStr(p.CanonicalPath) }

// Kind is a reconcile transition kind.
type Kind string

const (
	KindAdd                  Kind = "ADD"
	KindUpdate               Kind = "UPDATE"
	KindRename               Kind = "RENAME"
	KindMove                 Kind = "MOVE"
	KindUnchanged            Kind = "UNCHANGED"
	KindResetRemovalEvidence Kind = "RESET_REMOVAL_EVIDENCE"
	KindMissingEvidence      Kind = "MISSING_EVIDENCE"
	KindRemovalCandidate     Kind = "REMOVAL_CANDIDATE"
	KindConfirmRemoved       Kind = "CONFIRMED_REMOVED"
	KindConflict             Kind = "CONFLICT"
)

// Transition is one Kernel-decided canonical change (provider-neutral).
type Transition struct {
	Kind       Kind
	ResourceID string
	Entry      *domain.SnapshotEntry
	Prior      *PriorResource
	NewPath    *string
	NewName    *string

	MissingSince            *time.Time
	ConsecutiveMissing      int32
	MissingFirstSnapshotID  *string
	LastConfirmedGeneration int64

	Reason string
}

// Observation is a versioned IdentityEvidence observation the Store must append
// inside the same Stage-2 transaction (B3, doc A C-E2/C-E6).
type Observation struct {
	ResourceID string
	Entry      domain.SnapshotEntry
}

// Counts summarizes a reconcile result.
type Counts struct {
	Added           int `json:"added"`
	Updated         int `json:"updated"`
	Renamed         int `json:"renamed"`
	Moved           int `json:"moved"`
	Removed         int `json:"removed"`
	Unchanged       int `json:"unchanged"`
	MissingEvidence int `json:"missing_evidence"`
	Conflict        int `json:"conflict"`
}

// Conflict is a reconcile-result signal; it is never a journal event (J5).
type Conflict struct {
	EntryLocalID string
	ResourceID   string
	Reason       string
}

// Result is the pure Kernel decision handed to the Store for atomic application.
type Result struct {
	MutatesCanonical bool
	Transitions      []Transition
	Observations     []Observation
	Counts           Counts
	Conflicts        []Conflict
}
