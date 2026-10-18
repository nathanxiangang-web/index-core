package scan_test

import (
	"context"
	"encoding/json"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"strings"
	"testing"
	"time"
)

func TestDirectoryCoverageGenerationConfigAndShapeFences(t *testing.T) {
	for _, test := range []string{"generation", "config", "outside", "stale-time", "count", "entry-parent", "disabled", "legacy-proof", "missing-topology"} {
		t.Run(test, func(t *testing.T) {
			st, _, id := setupScopedRoot(t)
			ctx := context.Background()
			cfg, _ := json.Marshal(map[string]any{"path": "/library", "mirror_presence": true, "expected_base_path": "/drive/library", "topology_token_env": "TOPOLOGY_TOKEN"})
			adapter := postgres.AdapterConfig{CollectorKind: "openlist", Config: cfg}
			if err := st.UpsertAdapterConfig(ctx, id, adapter); err != nil {
				t.Fatal(err)
			}
			adapter, _ = st.GetAdapterConfig(ctx, id)
			proof := domain.DirectoryCoverage{Version: 2, Path: "/library", Generation: 0, EntryCount: 0, AdapterDigest: postgres.AdapterConfigDigest(adapter), TopologyDigest: strings.Repeat("a", 64)}
			at := time.Now().UTC()
			entries := []domain.SnapshotEntry{}
			count := int64(0)
			switch test {
			case "legacy-proof":
				proof.Version = 1
			case "missing-topology":
				proof.TopologyDigest = ""
			case "generation":
				proof.Generation = 2
			case "config":
				proof.AdapterDigest = "wrong"
			case "outside":
				proof.Path = "/elsewhere"
			case "stale-time":
				at = at.Add(-3 * time.Minute)
			case "count":
				proof.EntryCount = 3
			case "entry-parent":
				entries = []domain.SnapshotEntry{{EntryLocalID: "/wrong/a", Name: "a", ParentRef: "/wrong"}}
				proof.EntryCount = 1
				count = 1
			case "disabled":
				cfg = []byte(`{"path":"/library","mirror_presence":false}`)
				st.UpsertAdapterConfig(ctx, id, postgres.AdapterConfig{CollectorKind: "openlist", Config: cfg})
			}
			provenance, _ := json.Marshal(map[string]any{"directory_coverage": proof})
			fresh := domain.FreshRefreshed
			snap := domain.Snapshot{SnapshotID: reconcile.NewUUID(), RootID: id, Provenance: provenance, ObservedAt: at, TraversalStatus: domain.TraversalPartial, CompletenessFlag: domain.CompletenessFlagPartial, FreshnessEvidence: &fresh, LifecycleState: domain.SnapshotDraft, EntryCount: &count}
			if err := st.CreateDraftSnapshot(ctx, snap, entries); err != nil {
				t.Fatal(err)
			}
			if _, err := st.SubmitAndAdmitSnapshot(ctx, id, snap.SnapshotID); err != nil {
				t.Fatal(err)
			}
			coordinator := postgres.NewCoordinator(st, reconcile.Config{})
			out, err := coordinator.ProcessSnapshot(ctx, id, snap.SnapshotID)
			if err != nil || out.Status != domain.AdmissionStaleInput || out.Mutated {
				t.Fatalf("fence %s: %+v %v", test, out, err)
			}
		})
	}
}
