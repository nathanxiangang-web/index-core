// Package adapter defines the provider-neutral normalized Collector output shared
// by concrete adapters (rclone, AList/OpenList). It is the only shape the runtime
// scan service consumes; adapters never write Canonical Inventory.
package adapter

import (
	"context"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
)

// NormalizedEntry is a provider-neutral SnapshotEntry candidate. ProviderObjectID
// is optional evidence only; it is never the Snapshot revision token.
type NormalizedEntry struct {
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
}

// RawScan is the normalized Collector evidence for one scan.
type RawScan struct {
	Entries                   []NormalizedEntry
	TraversalStatus           domain.TraversalStatus
	ErrorSummary              []byte
	SkippedScopes             []byte
	SkippedKnownEmpty         bool
	Freshness                 domain.FreshnessEvidence
	Assurance                 domain.FailureVisibility
	ProviderIdentityAssurance domain.ProviderIdentityAssurance
}

// Collector is a provider-neutral source adapter.
type Collector interface {
	Scan(ctx context.Context, subPath string) (RawScan, error)
}
