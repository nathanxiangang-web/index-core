// Package query defines the read-only Query Contract surface (doc C). It exposes
// no canonical write path. RemovalEvidenceState and its companion counters are
// deliberately absent from ResourceView (M6, Blocker F).
package query

import (
	"errors"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// ErrStaleCursor is returned when a page cursor is bound to a generation that is
// no longer current (doc C P3). The consumer must restart paging.
var ErrStaleCursor = errors.New("STALE_CURSOR")

// RootView mirrors doc C Sec 2.1.
type RootView struct {
	RootID            string
	ScopeDescriptor   []byte
	LifecycleState    domain.RootLifecycleState
	CurrentGeneration int64
	CreatedAt         time.Time
}

// ResourceView mirrors doc C Sec 2.2. It MUST NOT expose removal_evidence_state,
// missing_since, or the consecutive-missing counter (Blocker F).
type ResourceView struct {
	ResourceID              string
	RootID                  string
	CanonicalPath           *string
	ParentResourceID        *string
	Name                    *string
	IsDir                   *bool
	Size                    *int64
	Mtime                   *time.Time
	ContentHash             *string
	ContentType             *string
	ResourcePresence        domain.ResourcePresence
	IntroducedAtGeneration  int64
	LastConfirmedGeneration int64
}

// JournalEventView mirrors doc C Sec 2.3.
type JournalEventView struct {
	EventSeq           int64
	EventID            *int64
	RootID             string
	GenerationNumber   int64
	IntraGenerationSeq int32
	EventType          domain.EventType
	ResourceID         *string
	Payload            []byte
	CommittedAt        time.Time
}

// RootStatus mirrors doc C Sec 2.4.
type RootStatus struct {
	RootID                  string
	LifecycleState          domain.RootLifecycleState
	CurrentGeneration       int64
	LastAppliedAdmissionSeq *int64
}

// Cursor is a generation-bound page cursor (doc C P2): it carries the root, the
// generation it was produced at, and the last-seen sort key.
type Cursor struct {
	RootID          string
	Generation      int64
	HasAfter        bool
	AfterPath       string
	AfterResourceID string
}

// ResourcePage is one page of resources plus an optional next cursor.
type ResourcePage struct {
	Items []ResourceView
	Next  *Cursor
}

// PathResolution is the explicit result of resolve_path (doc C Sec 2.5). It never
// fabricates a single winner: overlapping PRESENT rows are all returned with
// Ambiguous=true (doc A C-C3a).
type PathResolution struct {
	Matches   []ResourceView
	Ambiguous bool
}

// View converts a canonical resource to the consumer-visible ResourceView.
func View(r domain.CanonicalResource) ResourceView {
	return ResourceView{
		ResourceID:              r.ResourceID,
		RootID:                  r.RootID,
		CanonicalPath:           r.CanonicalPath,
		ParentResourceID:        r.ParentResourceID,
		Name:                    r.Name,
		IsDir:                   r.IsDir,
		Size:                    r.Size,
		Mtime:                   r.Mtime,
		ContentHash:             r.ContentHash,
		ContentType:             r.ContentType,
		ResourcePresence:        r.ResourcePresence,
		IntroducedAtGeneration:  r.IntroducedAtGeneration,
		LastConfirmedGeneration: r.LastConfirmedGeneration,
	}
}
