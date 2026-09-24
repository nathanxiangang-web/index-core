package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/collector/alist"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// ScanScope runs one targeted, non-recursive scoped refresh for a root (P0,
// Issue #62 — prototype only).
//
// It observes only the direct children of scope, submits a mandatory PARTIAL /
// additive-safe Snapshot, and drives the EXISTING draft -> admission -> Kernel
// coordinator -> reconcile path. It is additive-only by construction: a scoped
// observation never produces removal evidence (the frozen Kernel already treats
// PARTIAL/absence that way).
//
// This does not change the behavior of Service.Scan(), the generic Collector
// interface, or any frozen contract. No scheduler, polling, public API, or
// production CLI is added.
func (s *Service) ScanScope(ctx context.Context, rootID, scope string, maxEntries int) (Result, error) {
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

	// P0 scoped refresh is defined only for AList/OpenList single-response
	// refresh semantics. Any other collector kind fails closed.
	if acfg.CollectorKind != "alist" && acfg.CollectorKind != "openlist" {
		return Result{}, fmt.Errorf(
			"scoped refresh is not supported for collector kind %q (P0: alist/openlist only)", acfg.CollectorKind)
	}
	target := scopedAPIPath(ac.Path, scope)
	col := alist.Adapter{
		BaseURL:  ac.BaseURL,
		Token:    os.Getenv(ac.TokenEnv),
		Username: os.Getenv(ac.UsernameEnv),
		Password: os.Getenv(ac.PasswordEnv),
		Timeout:  s.timeout,
	}
	raw, err := col.ScanScope(ctx, target, maxEntries)
	if err != nil {
		// Fail closed: no Snapshot is created on any refresh/permission/
		// truncation/overflow error.
		return Result{}, fmt.Errorf("scoped collector refresh: %w", err)
	}
	if raw.TraversalStatus != domain.TraversalPartial {
		return Result{}, fmt.Errorf("internal: scoped observation must be PARTIAL, got %s", raw.TraversalStatus)
	}

	snapID := reconcile.NewUUID()
	provenance, _ := json.Marshal(map[string]any{
		"collector_kind": acfg.CollectorKind, "path": ac.Path, "scope": target,
		"mode": "scoped_refresh", "max_entries": maxEntries,
	})
	freshness := raw.Freshness
	assurance := raw.Assurance
	entryCount := int64(len(raw.Entries))
	snap := domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: provenance, ObservedAt: time.Now().UTC(),
		TraversalStatus: raw.TraversalStatus, ErrorSummary: raw.ErrorSummary,
		SkippedScopes: raw.SkippedScopes, SkippedScopesKnownEmpty: raw.SkippedKnownEmpty,
		FreshnessEvidence: &freshness, CollectorCompletenessAssurance: &assurance,
		// Mandatory P0 semantics: a scoped observation is never a COMPLETE claim.
		CompletenessFlag: domain.CompletenessFlagPartial,
		LifecycleState:   domain.SnapshotDraft,
		EntryCount:       &entryCount,
	}

	// Load policy before persisting (G3-R3): a missing policy must fail early,
	// never strand a SUBMITTED Snapshot.
	cfg, err := s.store.RootReconcileConfig(ctx, rootID)
	if err != nil {
		return Result{}, fmt.Errorf("load root policy: %w", err)
	}
	coord := postgres.NewCoordinator(s.store, cfg)

	// Root-scoped recovery + head drain (same guarantees as Service.Scan).
	resolved, ambiguous, rerr := s.store.ResolveUnadmittedSubmittedForRoot(ctx, rootID)
	if rerr != nil {
		return Result{}, fmt.Errorf("resolve stranded snapshots: %w", rerr)
	}
	if ambiguous {
		return Result{}, fmt.Errorf("ambiguous unadmitted SUBMITTED snapshots for root %s; refusing to guess order", rootID)
	}
	if resolved > 0 {
		s.logger.Info("scoped scan: recovered stranded submitted snapshots", "root_id", rootID, "resolved", resolved)
	}
	if err := drainHead(ctx, coord, rootID); err != nil {
		return Result{}, fmt.Errorf("drain existing pending head: %w", err)
	}

	entries := toEntries(raw)
	if err := s.store.CreateDraftSnapshot(ctx, snap, entries); err != nil {
		return Result{}, fmt.Errorf("persist draft snapshot: %w", err)
	}
	if _, err := s.store.SubmitAndAdmitSnapshot(ctx, rootID, snapID); err != nil {
		return Result{}, fmt.Errorf("submit and admit snapshot: %w", err)
	}

	out, err := coord.ProcessSnapshot(ctx, rootID, snapID)
	if errors.Is(err, postgres.ErrNotHead) {
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
	s.logger.Info("scoped scan complete",
		"root_id", rootID, "snapshot_id", snapID, "scope", target,
		"entry_count", len(entries), "scan_duration_ms", time.Since(start).Milliseconds(),
		"traversal_status", string(raw.TraversalStatus),
		"acceptance_state", acceptance,
		"reconcile_outcome", string(out.Status), "generation", out.Generation,
		"applied_generation", out.AppliedGeneration, "error_class", errClass)
	if err != nil {
		return Result{SnapshotID: snapID, Outcome: out}, fmt.Errorf("reconcile: %w", err)
	}
	if serr := scanOutcomeError(raw.TraversalStatus, out); serr != nil {
		return Result{SnapshotID: snapID, Outcome: out}, serr
	}
	return Result{SnapshotID: snapID, Outcome: out}, nil
}

// scopedAPIPath resolves a root-relative scope into an AList directory path.
//
//	rootPath="/loc", scope="sub"   -> "/loc/sub"
//	rootPath="/loc", scope="/sub"  -> "/loc/sub"
//	rootPath="/",    scope="down"  -> "/down"
//	rootPath="/loc", scope=""      -> "/loc"
func scopedAPIPath(rootPath, scope string) string {
	base := strings.TrimRight(strings.TrimSpace(rootPath), "/")
	rel := strings.Trim(strings.TrimSpace(scope), "/")
	switch {
	case rel == "":
		if base == "" {
			return "/"
		}
		return base
	case base == "":
		return "/" + rel
	default:
		return base + "/" + rel
	}
}
