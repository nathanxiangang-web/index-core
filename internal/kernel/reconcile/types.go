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
	RemovalGracePeriod            time.Duration
	MoveRecognitionHorizon        time.Duration
	MinConsecutiveCompleteMissing int
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
type PriorResource struct {
	domain.CanonicalResource
	ProviderObjectID      *string
	ProviderObjectIDScope *string
	ProviderIDAssurance   domain.ProviderIdentityAssurance
}

// Kind is a reconcile transition kind.
type Kind string

const (
	KindAdd              Kind = "ADD"
	KindUpdate           Kind = "UPDATE"
	KindRename           Kind = "RENAME"
	KindMove             Kind = "MOVE"
	KindUnchanged        Kind = "UNCHANGED"
	KindMissingEvidence  Kind = "MISSING_EVIDENCE"
	KindRemovalCandidate Kind = "REMOVAL_CANDIDATE"
	KindConfirmRemoved   Kind = "CONFIRMED_REMOVED"
	KindConflict         Kind = "CONFLICT"
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
	LastConfirmedGeneration int64

	Reason string
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
	Counts           Counts
	Conflicts        []Conflict
}
