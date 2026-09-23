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

	"github.com/nathanxiangang-web/index-core/internal/collector/adapter"
	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
	"github.com/nathanxiangang-web/index-core/internal/collector/rclone"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Service runs scans for roots.
type Service struct {
	store        *postgres.Store
	rclonePath   string
	rcloneConfig string
	timeout      time.Duration
	logger       *slog.Logger
}

// New builds a scan service.
func New(store *postgres.Store, rclonePath, rcloneConfig string, timeout time.Duration, logger *slog.Logger) *Service {
	return &Service{store: store, rclonePath: rclonePath, rcloneConfig: rcloneConfig, timeout: timeout, logger: logger}
}

// adapterConfig is the provider-specific adapter config (never Domain semantics).
type adapterConfig struct {
	Remote   string `json:"remote"` // rclone
	Path     string `json:"path"`   // shared
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

// collector builds the provider-neutral Collector for a root's adapter kind.
func (s *Service) collector(kind string, ac adapterConfig) (adapter.Collector, error) {
	switch kind {
	case "rclone":
		return rclone.Adapter{Binary: s.rclonePath, Remote: ac.Remote, ConfigPath: s.rcloneConfig, Timeout: s.timeout}, nil
	case "alist", "openlist":
		return alist.Adapter{BaseURL: ac.BaseURL, Token: ac.Token, Username: ac.Username, Password: ac.Password, Timeout: s.timeout}, nil
	default:
		return nil, fmt.Errorf("unsupported collector kind %q (supported: rclone, alist)", kind)
	}
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

	acfg, err := s.store.GetAdapterConfig(ctx, rootID)
	if err != nil {
		return Result{}, fmt.Errorf("load adapter config: %w", err)
	}
	var ac adapterConfig
	if err := json.Unmarshal(acfg.Config, &ac); err != nil {
		return Result{}, fmt.Errorf("parse adapter config: %w", err)
	}
	col, err := s.collector(acfg.CollectorKind, ac)
	if err != nil {
		return Result{}, err
	}
	raw, err := col.Scan(ctx, ac.Path)
	if err != nil {
		return Result{}, fmt.Errorf("collector scan: %w", err)
	}

	snapID := reconcile.NewUUID()
	provenance, _ := json.Marshal(map[string]any{"collector_kind": acfg.CollectorKind, "path": ac.Path})
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
	// Load policy BEFORE persisting, so a missing policy fails early instead of
	// stranding a SUBMITTED Snapshot (G3-R3 / regression "failure after creation").
	cfg, err := s.store.RootReconcileConfig(ctx, rootID)
	if err != nil {
		return Result{}, fmt.Errorf("load root policy: %w", err)
	}
	coord := postgres.NewCoordinator(s.store, cfg)

	// One-shot CLI must not strand/leapfrog older durable work: drain any existing
	// absolute PENDING head for this root before creating new work (G3-R3).
	if err := drainHead(ctx, coord, rootID); err != nil {
		return Result{}, fmt.Errorf("drain existing pending head: %w", err)
	}

	entries := toEntries(raw)
	if err := s.store.CreateSubmittedSnapshot(ctx, snap, entries); err != nil {
		return Result{}, fmt.Errorf("persist submitted snapshot: %w", err)
	}

	out, err := coord.ProcessSnapshot(ctx, rootID, snapID)
	if errors.Is(err, postgres.ErrNotHead) {
		// Another durable head exists (e.g. admitted concurrently); drain it and
		// retry our own snapshot once rather than leaving it stranded.
		if derr := drainHead(ctx, coord, rootID); derr != nil {
			return Result{SnapshotID: snapID, Outcome: out}, fmt.Errorf("drain head: %w", derr)
		}
		out, err = coord.ProcessSnapshot(ctx, rootID, snapID)
	}

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

// drainHead processes the existing absolute PENDING head(s) for a root until none
// remain, so a one-shot CLI does not strand or leapfrog older durable work.
func drainHead(ctx context.Context, coord *postgres.Coordinator, rootID string) error {
	for {
		_, done, err := coord.ProcessHead(ctx, rootID)
		if err != nil {
			return err
		}
		if !done {
			return nil
		}
	}
}

func toEntries(raw adapter.RawScan) []domain.SnapshotEntry {
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
