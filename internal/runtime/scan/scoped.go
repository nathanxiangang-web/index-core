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
		return Result{}, scopeWrap(ScopeFailureInternal, fmt.Errorf("load root: %w", err))
	}
	if root.LifecycleState == domain.RootDeleted {
		return Result{}, scopeErrorf(ScopeFailureRootInactive, "root %s is DELETED", rootID)
	}

	acfg, err := s.store.GetAdapterConfig(ctx, rootID)
	if err != nil {
		return Result{}, scopeWrap(ScopeFailureConfigInvalid, fmt.Errorf("load adapter config: %w", err))
	}
	var ac adapterConfig
	if err := json.Unmarshal(acfg.Config, &ac); err != nil {
		return Result{}, scopeWrap(ScopeFailureConfigInvalid, fmt.Errorf("parse adapter config: %w", err))
	}

	// P0 scoped refresh is defined only for AList/OpenList single-response
	// refresh semantics. Any other collector kind fails closed.
	if acfg.CollectorKind != "alist" && acfg.CollectorKind != "openlist" {
		return Result{}, scopeErrorf(ScopeFailureConfigInvalid,
			"scoped refresh is not supported for collector kind %q (P0: alist/openlist only)", acfg.CollectorKind)
	}
	// P0 hard cap: the caller must never be able to widen a scoped observation.
	if maxEntries < 0 || maxEntries > alist.MaxScopedEntries {
		return Result{}, scopeErrorf(ScopeFailureConfigInvalid,
			"scoped refresh max_entries must be within [0, %d], got %d", alist.MaxScopedEntries, maxEntries)
	}

	// Normalize the root-relative scope, reject "." / ".." components, and
	// guarantee the resolved provider path stays inside the root.
	rel, err := canonicalScopePath(scope)
	if err != nil {
		return Result{}, scopeWrap(ScopeFailureInvalidScope, err)
	}
	target, err := scopeAPIPath(ac.Path, rel)
	if err != nil {
		return Result{}, scopeWrap(ScopeFailureInvalidScope, err)
	}
	// A non-root scope MUST already be an unambiguous PRESENT canonical
	// directory; otherwise a scoped observation would produce orphan resources
	// whose parent does not exist in the Canonical Inventory.
	//
	// Validate against the SAME provider-path namespace that Adapter.Scan()
	// records (EntryLocalID is the provider path, e.g. "/library/sub"), NOT the
	// root-relative scope. Using rel here would wrongly fail whenever the
	// configured root path is not "/".
	if rel != "" {
		if err := s.requirePresentDirectory(ctx, rootID, target); err != nil {
			return Result{}, scopeWrap(ScopeFailureInvalidScope, err)
		}
	}

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
		// truncation/overflow error. The classification is typed, never string
		// parsed.
		return Result{}, mapAListScopedError(err)
	}
	if raw.TraversalStatus != domain.TraversalPartial {
		return Result{}, scopeErrorf(ScopeFailureInternal,
			"internal: scoped observation must be PARTIAL, got %s", raw.TraversalStatus)
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
		return Result{}, scopeWrap(ScopeFailureConfigInvalid, fmt.Errorf("load root policy: %w", err))
	}
	coord := postgres.NewCoordinator(s.store, cfg)

	// Root-scoped recovery + head drain (same guarantees as Service.Scan).
	resolved, ambiguous, rerr := s.store.ResolveUnadmittedSubmittedForRoot(ctx, rootID)
	if rerr != nil {
		return Result{}, scopeWrap(ScopeFailureInternal, fmt.Errorf("resolve stranded snapshots: %w", rerr))
	}
	if ambiguous {
		return Result{}, scopeErrorf(ScopeFailureInternal,
			"ambiguous unadmitted SUBMITTED snapshots for root %s; refusing to guess order", rootID)
	}
	if resolved > 0 {
		s.logger.Info("scoped scan: recovered stranded submitted snapshots", "root_id", rootID, "resolved", resolved)
	}
	if err := drainHead(ctx, coord, rootID); err != nil {
		return Result{}, scopeWrap(ScopeFailureInternal, fmt.Errorf("drain existing pending head: %w", err))
	}

	entries := toEntries(raw)
	if err := s.store.CreateDraftSnapshot(ctx, snap, entries); err != nil {
		return Result{}, scopeWrap(ScopeFailureInternal, fmt.Errorf("persist draft snapshot: %w", err))
	}
	if _, err := s.store.SubmitAndAdmitSnapshot(ctx, rootID, snapID); err != nil {
		return Result{}, scopeWrap(ScopeFailureInternal, fmt.Errorf("submit and admit snapshot: %w", err))
	}

	out, err := coord.ProcessSnapshot(ctx, rootID, snapID)
	if errors.Is(err, postgres.ErrNotHead) {
		if derr := drainHead(ctx, coord, rootID); derr != nil {
			return Result{SnapshotID: snapID, Outcome: out}, scopeWrap(ScopeFailureInternal, fmt.Errorf("drain head: %w", derr))
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
		return Result{SnapshotID: snapID, Outcome: out}, scopeWrap(ScopeFailureInternal, fmt.Errorf("reconcile: %w", err))
	}
	if serr := scanOutcomeError(raw.TraversalStatus, out); serr != nil {
		return Result{SnapshotID: snapID, Outcome: out}, scopeWrap(s.lifecycleFailureKind(ctx, rootID), serr)
	}
	return Result{SnapshotID: snapID, Outcome: out}, nil
}

// lifecycleFailureKind attributes a post-observation rejection to a root
// lifecycle condition when that is positive, otherwise INTERNAL (doc P4 Sec 9.2).
func (s *Service) lifecycleFailureKind(ctx context.Context, rootID string) ScopeFailureKind {
	root, err := s.store.GetRoot(ctx, s.store.Pool(), rootID)
	if err == nil && root.LifecycleState != domain.RootActive {
		return ScopeFailureRootInactive
	}
	return ScopeFailureInternal
}

// mapAListScopedError translates a typed AList scoped failure into the
// provider-neutral scan classification. Untyped errors fail closed as INTERNAL.
func mapAListScopedError(err error) *ScopeError {
	var se *alist.ScopedError
	if errors.As(err, &se) {
		switch se.Kind {
		case alist.ScopedAuthOrPermission:
			return scopeWrap(ScopeFailureAuthOrPermission, err)
		case alist.ScopedThrottled:
			return scopeWrap(ScopeFailureThrottled, err)
		case alist.ScopedTransientProvider:
			return scopeWrap(ScopeFailureTransientProvider, err)
		case alist.ScopedTooLarge:
			return scopeWrap(ScopeFailureTooLarge, err)
		case alist.ScopedInvalidScope:
			return scopeWrap(ScopeFailureInvalidScope, err)
		case alist.ScopedConfigInvalid:
			return scopeWrap(ScopeFailureConfigInvalid, err)
		}
	}
	return scopeWrap(ScopeFailureInternal, err)
}

// canonicalScopePath normalizes a root-relative scope and rejects "." / ".."
// (and empty) path components so a scope can never escape the root. It returns
// "" for the root itself.
func canonicalScopePath(scope string) (string, error) {
	trimmed := strings.Trim(strings.TrimSpace(scope), "/")
	if trimmed == "" {
		return "", nil
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf(
				"scoped path %q must not contain empty, %q or %q components", scope, ".", "..")
		}
	}
	return "/" + trimmed, nil
}

// scopeAPIPath maps a normalized root-relative scope onto the root's provider
// path and guarantees containment: the result is the root path itself or a
// descendant of it.
func scopeAPIPath(rootPath, rel string) (string, error) {
	rawBase := strings.TrimSpace(rootPath)
	for _, seg := range strings.Split(rawBase, "/") {
		if seg == ".." {
			return "", fmt.Errorf("root path %q must not contain %q components", rootPath, "..")
		}
	}
	for _, seg := range strings.Split(strings.Trim(rel, "/"), "/") {
		if seg == ".." {
			return "", fmt.Errorf("scoped path %q must not contain %q components", rel, "..")
		}
	}
	base := normalizeAPIPath(rawBase)
	joined := normalizeAPIPath(base + rel)
	if !pathWithin(base, joined) {
		return "", fmt.Errorf("scoped path %q escapes root path %q", rel, base)
	}
	return joined, nil
}

// normalizeAPIPath collapses duplicate separators and drops "." segments; it
// never resolves ".." (callers reject that first).
func normalizeAPIPath(p string) string {
	segs := strings.Split(strings.TrimSpace(p), "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if s == "" || s == "." {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return "/"
	}
	return "/" + strings.Join(out, "/")
}

// pathWithin reports whether p is base itself or a descendant of base.
func pathWithin(base, p string) bool {
	if base == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == base || strings.HasPrefix(p, base+"/")
}

// requirePresentDirectory fails closed unless rel resolves to exactly one
// PRESENT canonical directory for the root.
func (s *Service) requirePresentDirectory(ctx context.Context, rootID, rel string) error {
	rows, err := s.store.PresentResourcesAtPath(ctx, s.store.Pool(), rootID, rel)
	if err != nil {
		return fmt.Errorf("load scoped parent %q: %w", rel, err)
	}
	if len(rows) != 1 {
		return fmt.Errorf(
			"scoped refresh requires exactly one PRESENT canonical directory at %q, found %d", rel, len(rows))
	}
	if rows[0].IsDir == nil || !*rows[0].IsDir {
		return fmt.Errorf("scoped refresh parent %q is not a directory", rel)
	}
	return nil
}
