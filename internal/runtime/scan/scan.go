// Package scan is the provider-neutral runtime scan service. rclone is the first
// implementation. The Collector never writes Canonical Inventory directly: it
// produces DRAFT Snapshot + entries -> SUBMITTED, then the Kernel Coordinator
// reconciles. rclone stays additive-safe (skip evidence stays UNKNOWN).
package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/collector/rclone"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Service runs scans for roots.
type Service struct {
	store      *postgres.Store
	rclonePath string
	timeout    time.Duration
	logger     *slog.Logger
}

// New builds a scan service.
func New(store *postgres.Store, rclonePath string, timeout time.Duration, logger *slog.Logger) *Service {
	return &Service{store: store, rclonePath: rclonePath, timeout: timeout, logger: logger}
}

// rcloneConfig is the provider-specific adapter config (never Domain semantics).
type rcloneConfig struct {
	Remote string `json:"remote"`
	Path   string `json:"path"`
}

// Result reports a completed scan.
type Result struct {
	SnapshotID string
	Outcome    postgres.ReconcileOutcome
}

// Scan runs one scan for a root through the full runtime path.
func (s *Service) Scan(ctx context.Context, rootID string) (Result, error) {
	start := time.Now()

	root, err := s.store.GetRoot(ctx, s.store.Pool(), rootID)
	if err != nil {
		return Result{}, fmt.Errorf("load root: %w", err)
	}
	if root.LifecycleState == domain.RootDeleted {
		return Result{}, errors.New("root is DELETED")
	}

	adapter, err := s.store.GetAdapterConfig(ctx, rootID)
	if err != nil {
		return Result{}, fmt.Errorf("load adapter config: %w", err)
	}
	if adapter.CollectorKind != "rclone" {
		return Result{}, fmt.Errorf("unsupported collector kind %q (Gate 3 P5 implements rclone)", adapter.CollectorKind)
	}
	var ac rcloneConfig
	if err := json.Unmarshal(adapter.Config, &ac); err != nil {
		return Result{}, fmt.Errorf("parse rclone adapter config: %w", err)
	}

	ad := rclone.Adapter{Binary: s.rclonePath, Remote: ac.Remote, Timeout: s.timeout}
	raw, err := ad.Scan(ctx, ac.Path)
	if err != nil {
		return Result{}, fmt.Errorf("rclone scan: %w", err)
	}

	snapID := reconcile.NewUUID()
	provenance, _ := json.Marshal(map[string]any{"collector_kind": "rclone", "remote": ac.Remote, "path": ac.Path})
	flag := domain.CompletenessFlagComplete
	if raw.TraversalStatus != domain.TraversalSuccess {
		flag = domain.CompletenessFlagPartial
	}
	freshness := raw.Freshness
	assurance := raw.Assurance
	entryCount := int64(len(raw.Entries))
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: provenance, ObservedAt: time.Now().UTC(),
		TraversalStatus: raw.TraversalStatus, ErrorSummary: raw.ErrorSummary,
		SkippedScopes: raw.SkippedScopes, SkippedScopesKnownEmpty: raw.SkippedKnownEmpty,
		FreshnessEvidence: &freshness, CollectorCompletenessAssurance: &assurance,
		CompletenessFlag: flag, LifecycleState: domain.SnapshotDraft, EntryCount: &entryCount,
	}
	entries := toEntries(raw)
	if err := s.store.CreateSubmittedSnapshot(ctx, snap, entries); err != nil {
		return Result{}, fmt.Errorf("persist submitted snapshot: %w", err)
	}

	cfg, err := s.store.RootReconcileConfig(ctx, rootID)
	if err != nil {
		return Result{}, fmt.Errorf("load root policy: %w", err)
	}
	out, err := postgres.NewCoordinator(s.store, cfg).ProcessSnapshot(ctx, rootID, snapID)

	acceptance := ""
	if row, gerr := s.store.GetSnapshot(ctx, s.store.Pool(), snapID); gerr == nil && row.AcceptanceState != nil {
		acceptance = string(*row.AcceptanceState)
	}
	errClass := ""
	if err != nil {
		errClass = "reconcile"
	}
	s.logger.Info("scan complete",
		"root_id", rootID, "snapshot_id", snapID,
		"entry_count", len(entries), "scan_duration_ms", time.Since(start).Milliseconds(),
		"traversal_status", string(raw.TraversalStatus),
		"acceptance_state", acceptance,
		"reconcile_outcome", string(out.Status), "generation", out.Generation,
		"applied_generation", out.AppliedGeneration, "error_class", errClass)
	if err != nil {
		return Result{SnapshotID: snapID, Outcome: out}, fmt.Errorf("reconcile: %w", err)
	}
	return Result{SnapshotID: snapID, Outcome: out}, nil
}

func toEntries(raw rclone.RawScan) []domain.SnapshotEntry {
	out := make([]domain.SnapshotEntry, 0, len(raw.Entries))
	assurance := raw.ProviderIdentityAssurance
	for _, e := range raw.Entries {
		se := domain.SnapshotEntry{
			EntryLocalID: e.EntryLocalID, Name: e.Name, ParentRef: e.ParentRef, IsDir: e.IsDir,
			Size: e.Size, Mtime: e.Mtime, ContentHash: e.ContentHash, HashAlgorithm: e.HashAlgorithm,
			ProviderObjectID: e.ProviderObjectID, ProviderObjectIDScope: e.ProviderObjectIDScope,
			ContentType: e.ContentType,
		}
		a := assurance
		se.ProviderIdentityAssurance = &a
		out = append(out, se)
	}
	return out
}
