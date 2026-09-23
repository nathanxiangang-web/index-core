package httpapi

import (
	"time"

	"github.com/nathanxiangang-web/index-core/internal/query"
)

// Transport DTOs are intentionally separate from Domain/Query types.

type rootDTO struct {
	RootID            string    `json:"root_id"`
	LifecycleState    string    `json:"lifecycle_state"`
	CurrentGeneration int64     `json:"current_generation"`
	CreatedAt         time.Time `json:"created_at"`
}

type rootStatusDTO struct {
	RootID                  string `json:"root_id"`
	LifecycleState          string `json:"lifecycle_state"`
	CurrentGeneration       int64  `json:"current_generation"`
	LastAppliedAdmissionSeq *int64 `json:"last_applied_admission_seq,omitempty"`
}

type resourceDTO struct {
	ResourceID              string     `json:"resource_id"`
	RootID                  string     `json:"root_id"`
	CanonicalPath           *string    `json:"canonical_path,omitempty"`
	ParentResourceID        *string    `json:"parent_resource_id,omitempty"`
	Name                    *string    `json:"name,omitempty"`
	IsDir                   *bool      `json:"is_dir,omitempty"`
	Size                    *int64     `json:"size,omitempty"`
	Mtime                   *time.Time `json:"mtime,omitempty"`
	ContentHash             *string    `json:"content_hash,omitempty"`
	ContentType             *string    `json:"content_type,omitempty"`
	ResourcePresence        string     `json:"resource_presence"`
	IntroducedAtGeneration  int64      `json:"introduced_at_generation"`
	LastConfirmedGeneration int64      `json:"last_confirmed_generation"`
}

type journalDTO struct {
	EventSeq           int64     `json:"event_seq"`
	GenerationNumber   int64     `json:"generation_number"`
	IntraGenerationSeq int32     `json:"intra_generation_seq"`
	EventType          string    `json:"event_type"`
	ResourceID         *string   `json:"resource_id,omitempty"`
	Payload            string    `json:"payload,omitempty"`
	CommittedAt        time.Time `json:"committed_at"`
}

type pageDTO struct {
	Items []resourceDTO `json:"items"`
	Next  string        `json:"next_cursor,omitempty"`
}

func toRootDTO(v query.RootView) rootDTO {
	return rootDTO{RootID: v.RootID, LifecycleState: string(v.LifecycleState),
		CurrentGeneration: v.CurrentGeneration, CreatedAt: v.CreatedAt}
}

func toResourceDTO(v query.ResourceView) resourceDTO {
	return resourceDTO{
		ResourceID: v.ResourceID, RootID: v.RootID, CanonicalPath: v.CanonicalPath,
		ParentResourceID: v.ParentResourceID, Name: v.Name, IsDir: v.IsDir, Size: v.Size,
		Mtime: v.Mtime, ContentHash: v.ContentHash, ContentType: v.ContentType,
		ResourcePresence: string(v.ResourcePresence), IntroducedAtGeneration: v.IntroducedAtGeneration,
		LastConfirmedGeneration: v.LastConfirmedGeneration,
	}
}

func toJournalDTO(v query.JournalEventView) journalDTO {
	return journalDTO{
		EventSeq: v.EventSeq, GenerationNumber: v.GenerationNumber, IntraGenerationSeq: v.IntraGenerationSeq,
		EventType: string(v.EventType), ResourceID: v.ResourceID, Payload: string(v.Payload), CommittedAt: v.CommittedAt,
	}
}
