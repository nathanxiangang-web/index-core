package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"path"
	"strings"
	"time"
)

var ErrDirectoryCoverage = errors.New("invalid or stale directory coverage")

func AdapterConfigDigest(a AdapterConfig) string {
	sum := sha256.Sum256(append([]byte(a.CollectorKind+"\x00"), a.Config...))
	return hex.EncodeToString(sum[:])
}

// Revalidate immutable coverage against current generation and adapter. The
// adapter row lock prevents concurrent namespace/config changes before commit.
func (s *Store) directoryCoverage(ctx context.Context, tx pgx.Tx, snap domain.Snapshot, entries []domain.SnapshotEntry, generation int64) (*domain.DirectoryCoverage, error) {
	var provenance map[string]json.RawMessage
	// Legacy provenance is opaque JSON, not necessarily an object. It never
	// gains removal authority merely because the opt-in feature now exists.
	if json.Unmarshal(snap.Provenance, &provenance) != nil {
		return nil, nil
	}
	raw, ok := provenance["directory_coverage"]
	if !ok {
		return nil, nil
	}
	var p *domain.DirectoryCoverage
	if json.Unmarshal(raw, &p) != nil || p == nil {
		return nil, ErrDirectoryCoverage
	}
	invalid := ErrDirectoryCoverage
	generationOK := p.Generation == generation && p.Version == 2
	if p.Version == 3 {
		// Explicit same-root lineage contract, NEVER a synthetic current generation.
		// Preserve the captured base; establish its committed prefix/dependencies.
		_, f, ferr := readFence(snap)
		if ferr != nil || f.Version != 2 || f.Generation != p.Generation {
			return nil, invalid
		}
		var n int
		var seq *int64
		if err := tx.QueryRow(ctx, `SELECT count(*),min(admission_seq) FROM index_admission WHERE root_id=$1::uuid AND snapshot_id=$2::uuid`, snap.RootID, snap.SnapshotID).Scan(&n, &seq); err != nil {
			return nil, err
		}
		if n != 1 || seq == nil {
			return nil, invalid
		}
		if err := s.validateScopedFetch(ctx, tx, snap, seq); err != nil {
			if errors.Is(err, ErrScopedFetchStale) {
				return nil, invalid
			}
			return nil, err
		}
		generationOK = true
	}
	decoded, decodeErr := hex.DecodeString(p.TopologyDigest)
	if !generationOK || decodeErr != nil || len(decoded) != sha256.Size || p.EntryCount != len(entries) || p.Path == "" || !strings.HasPrefix(p.Path, "/") || path.Clean(p.Path) != p.Path || strings.ContainsAny(p.Path, "\\\x00") || p.EntryCount > 10000 {
		return nil, invalid
	}
	if snap.TraversalStatus != domain.TraversalPartial || snap.CompletenessFlag != domain.CompletenessFlagPartial || snap.FreshnessEvidence == nil || *snap.FreshnessEvidence != domain.FreshRefreshed || len(snap.ErrorSummary) != 0 || len(snap.SkippedScopes) != 0 {
		return nil, invalid
	}
	var dbNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return nil, err
	}
	if snap.ObservedAt.Before(dbNow.Add(-2*time.Minute)) || snap.ObservedAt.After(dbNow.Add(30*time.Second)) || snap.EntryCount == nil || *snap.EntryCount != int64(p.EntryCount) {
		return nil, invalid
	}
	a := AdapterConfig{}
	if err := tx.QueryRow(ctx, `SELECT collector_kind,config FROM index_root_adapter_config WHERE root_id=$1::uuid FOR SHARE`, snap.RootID).Scan(&a.CollectorKind, &a.Config); err != nil {
		return nil, err
	}
	var ac struct {
		Mirror           bool   `json:"mirror_presence"`
		Path             string `json:"path"`
		Base             string `json:"expected_base_path"`
		Topology         string `json:"topology_token_env"`
		TopologyUsername string `json:"topology_username_env"`
		TopologyPassword string `json:"topology_password_env"`
	}
	if json.Unmarshal(a.Config, &ac) != nil || !ac.Mirror || ac.Base == "" || (ac.Topology == "" && (ac.TopologyUsername == "" || ac.TopologyPassword == "")) || (a.CollectorKind != "alist" && a.CollectorKind != "openlist") || p.AdapterDigest != AdapterConfigDigest(a) {
		return nil, invalid
	}
	base := path.Clean("/" + strings.Trim(ac.Path, "/"))
	if p.Path != base && !strings.HasPrefix(p.Path, strings.TrimSuffix(base, "/")+"/") {
		return nil, invalid
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.ParentRef != p.Path || e.Name == "" || e.Name == "." || e.Name == ".." || strings.ContainsAny(e.Name, "/\\\x00") || seen[e.Name] {
			return nil, invalid
		}
		seen[e.Name] = true
	}
	return p, nil
}
