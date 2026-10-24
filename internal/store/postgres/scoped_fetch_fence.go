package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
)

var ErrScopedFetchStale = errors.New("invalid or stale scoped fetch fence")

// ScopedFetchFence is optional experimental Snapshot provenance, not a new
// canonical truth or claim API. Missing fence retains legacy admission behavior.
// It is checked in BOTH short admission and authoritative Stage2 transactions.
type ScopedFetchFence struct {
	Version           int                `json:"version"`
	Generation        int64              `json:"generation"`
	AdmissionFrontier int64              `json:"admission_frontier"`
	AdapterDigest     string             `json:"adapter_digest"`
	AdapterRevision   string             `json:"adapter_revision"`
	ScopeKey          string             `json:"scope_key"`
	TargetPath        string             `json:"target_path"`
	ParentID          string             `json:"parent_id"`
	ClaimedSequence   int64              `json:"claimed_sequence"`
	Attempt           int64              `json:"attempt"`
	AttemptStartedAt  time.Time          `json:"attempt_started_at"`
	Lineage           *ScopedReadLineage `json:"lineage,omitempty"`
}

// Caller already owns the Root row lock. Adapter -> Watch -> Work follows it.
// seq=nil means Stage1; otherwise seq is this exact Stage2 admission. No source
// I/O occurs while locks are held. Invalidated admitted input becomes STALE_INPUT
// through the existing Coordinator path, not a half-applied Canonical update.
func (s *Store) validateScopedFetch(ctx context.Context, tx pgx.Tx, snap domain.Snapshot, seq *int64) error {
	var metadata map[string]json.RawMessage
	if json.Unmarshal(snap.Provenance, &metadata) != nil {
		return nil // existing opaque legacy provenance never gains new authority
	}
	raw, ok := metadata["scoped_fetch_fence"]
	if !ok {
		return nil
	}
	var f *ScopedFetchFence
	if json.Unmarshal(raw, &f) != nil || f == nil {
		return ErrScopedFetchStale
	}
	digest, err := hex.DecodeString(f.AdapterDigest)
	if err != nil || len(digest) != 32 || (f.Version != 1 && f.Version != 2) || (f.Version == 1 && f.Lineage != nil) || f.Generation < 0 || f.AdmissionFrontier < 0 ||
		f.AdapterRevision == "" || f.ClaimedSequence < 1 || f.Attempt < 1 || f.AttemptStartedAt.IsZero() ||
		state.ValidateScopeKey(f.ScopeKey) != nil || f.TargetPath == "" || path.Clean(f.TargetPath) != f.TargetPath ||
		!strings.HasPrefix(f.TargetPath, "/") || strings.ContainsAny(f.TargetPath, "\\\x00") ||
		snap.TraversalStatus != domain.TraversalPartial || snap.CompletenessFlag != domain.CompletenessFlagPartial ||
		snap.EntryCount == nil || *snap.EntryCount < 0 || *snap.EntryCount > 1000 ||
		snap.FreshnessEvidence == nil || *snap.FreshnessEvidence != domain.FreshRefreshed {
		return ErrScopedFetchStale
	}
	root, err := s.GetRoot(ctx, tx, snap.RootID)
	if err != nil {
		return err
	}
	expected := f.AdmissionFrontier
	expectedGen := f.Generation
	if f.Version == 2 {
		expectedGen, expected, err = s.validateReadLineage(ctx, tx, snap, *f, seq)
		if err != nil {
			return err
		}
	} else if seq != nil {
		expected++
		if *seq != expected {
			return ErrScopedFetchStale
		}
	}
	if root.LifecycleState != domain.RootActive || root.CurrentGeneration != expectedGen || root.LatestAdmissionSeq != expected {
		return ErrScopedFetchStale
	}
	var dbNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return err
	}
	if snap.ObservedAt.Before(dbNow.Add(-2*time.Minute)) || snap.ObservedAt.After(dbNow.Add(30*time.Second)) {
		return ErrScopedFetchStale
	}
	var a AdapterConfig
	if err := tx.QueryRow(ctx, `SELECT collector_kind, config, xmin::text FROM index_root_adapter_config WHERE root_id=$1::uuid FOR SHARE`, snap.RootID).Scan(&a.CollectorKind, &a.Config, &a.Revision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrScopedFetchStale
		}
		return err
	}
	if AdapterConfigDigest(a) != f.AdapterDigest || a.Revision != f.AdapterRevision {
		return ErrScopedFetchStale
	}
	if f.Version == 2 {
		var policyRevision string
		if err := tx.QueryRow(ctx, `SELECT xmin::text FROM index_root_config WHERE root_id=$1::uuid FOR SHARE`, snap.RootID).Scan(&policyRevision); err != nil {
			return err
		}
		if f.Lineage == nil || policyRevision != f.Lineage.PolicyRevision {
			return ErrScopedFetchStale
		}
	}
	var cfg struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(a.Config, &cfg) != nil || path.Join("/", cfg.Path, strings.TrimPrefix(f.ScopeKey, "/")) != f.TargetPath {
		return ErrScopedFetchStale
	}
	if f.ScopeKey != "/" {
		parents, err := s.PresentResourcesAtPath(ctx, tx, snap.RootID, f.TargetPath)
		if err != nil {
			return err
		}
		if len(parents) != 1 || parents[0].ResourceID != f.ParentID || parents[0].IsDir == nil || !*parents[0].IsDir {
			return ErrScopedFetchStale
		}
	} else if f.ParentID != "" {
		return ErrScopedFetchStale
	}
	if _, _, err := lockWatchForUpdate(ctx, tx, snap.RootID, f.ScopeKey); err != nil {
		return err
	}
	w, found, err := loadWorkForUpdate(ctx, tx, snap.RootID, f.ScopeKey)
	if err != nil {
		return err
	}
	if !found || w.WorkState != state.WorkInFlight || w.ClaimedSignalSeq == nil || *w.ClaimedSignalSeq != f.ClaimedSequence ||
		w.AttemptCount != f.Attempt || w.LastAttemptStartedAt == nil || !w.LastAttemptStartedAt.Equal(f.AttemptStartedAt) {
		return ErrScopedFetchStale
	}
	// Include time spent waiting for subordinate locks in the freshness decision.
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return err
	}
	if snap.ObservedAt.Before(dbNow.Add(-2*time.Minute)) || snap.ObservedAt.After(dbNow.Add(30*time.Second)) {
		return ErrScopedFetchStale
	}
	return nil
}
