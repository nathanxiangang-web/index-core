package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/kernel/reconcile"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestIssue138DependencyPushdownEqualsWholeRootReference(t *testing.T) {
	pool := testutil.Issue138Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	root := reconcile.NewUUID()
	if err := st.CreateRoot(ctx, pool, root, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	targets := []string{"/scope", "/x_%/子", "/x'quote", "/outside", "/", "/scope-other"}
	for i := 0; i < 160; i++ {
		id := reconcile.NewUUID()
		name := fmt.Sprintf("item%d", i)
		isDir := i%2 == 0
		p := fmt.Sprintf("/other/%d", i)
		switch i % 8 {
		case 0:
			p = "/scope"
		case 1:
			p = fmt.Sprintf("/scope/deep/%d", i)
		case 2:
			p = "/"
		case 3:
			p = fmt.Sprintf("/scope-other/%d", i)
		case 4:
			p = fmt.Sprintf("/x_%%/子/%d", i)
		case 7:
			p = "/x'quote"
		}
		row := domain.CanonicalResource{ResourceID: id, RootID: root, ResourcePresence: domain.ResourcePresent, RemovalEvidenceState: domain.RemovalEvidenceNone, CanonicalPath: &p, Name: &name, IsDir: &isDir, CurrentAttributes: []byte(`{}`)}
		if i%8 == 6 {
			row.CanonicalPath = nil
		}
		if i%5 != 0 {
			size := int64(i % 9)
			row.Size = &size
		}
		if i%3 == 0 {
			hash, algo := fmt.Sprintf("hash-%d", i%6), "sha1"
			row.ContentHash = &hash
			row.HashAlgorithm = &algo
		}
		if i%11 == 0 {
			row.RemovalEvidenceState = domain.RemovalEvidenceMissingConfirmedByComplete
			at := time.Now().UTC().Truncate(time.Microsecond)
			row.MissingSince = &at
		}
		if i%22 == 0 {
			row.ResourcePresence = domain.ResourceRemoved
		}
		if err := st.InsertCanonicalResource(ctx, pool, row); err != nil {
			t.Fatal(err)
		}
		if i%7 == 0 {
			for _, prefix := range []string{"old", "latest"} {
				objectID, scope := fmt.Sprintf("%s-%d", prefix, i), "fixture"
				if err := st.InsertIdentityEvidence(ctx, pool, domain.IdentityEvidenceObservation{ResourceID: id, SnapshotID: reconcile.NewUUID(), ObservedAt: time.Now().UTC(), ProviderObjectID: &objectID, ProviderObjectIDScope: &scope, ProviderIdentityAssurance: domain.IdentityStableWithinScope, IsDir: isDir}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	otherRoot := reconcile.NewUUID()
	if err := st.CreateRoot(ctx, pool, otherRoot, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	otherPath, otherName, otherSize, otherHash, otherAlgo, otherDir := "/scope", "other-root", int64(1), "hash-0", "sha1", false
	if err := st.InsertCanonicalResource(ctx, pool, domain.CanonicalResource{ResourceID: reconcile.NewUUID(), RootID: otherRoot, ResourcePresence: domain.ResourcePresent, RemovalEvidenceState: domain.RemovalEvidenceNone, CanonicalPath: &otherPath, Name: &otherName, Size: &otherSize, ContentHash: &otherHash, HashAlgorithm: &otherAlgo, IsDir: &otherDir, CurrentAttributes: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	// A deliberately false current projection cannot become dependency authority.
	if _, err := pool.Exec(ctx, `INSERT INTO index_identity_evidence_current(resource_id,provider_object_id,provider_object_id_scope,provider_identity_assurance) SELECT resource_id,'bogus-projection','fixture','STABLE_WITHIN_SCOPE' FROM index_canonical_resource LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	full, err := st.LoadPriorResources(ctx, pool, root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 48; i++ {
		target := targets[i%len(targets)]
		entries := []domain.SnapshotEntry{}
		if i%4 != 0 {
			size := int64(i % 11)
			hash := fmt.Sprintf("hash-%d", i%6)
			id := fmt.Sprintf("latest-%d", (i%20)*7)
			if i%3 == 0 {
				id = fmt.Sprintf("old-%d", (i%20)*7)
			}
			entry := domain.SnapshotEntry{Size: &size}
			if i%2 == 0 {
				entry.ContentHash = &hash
			}
			if i%5 == 0 {
				entry.ProviderObjectID = &id
			}
			entries = append(entries, entry)
		}
		want, err := dependencyDigest(full, target, entries)
		if err != nil {
			t.Fatal(err)
		}
		// Both normal/custom and forced generic cached plans must preserve the set.
		for _, generic := range []bool{false, true} {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if generic {
				if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode='force_generic_plan'`); err != nil {
					tx.Rollback(ctx)
					t.Fatal(err)
				}
			}
			got, err := st.scopedDependencyDigest(ctx, tx, root, target, entries)
			tx.Rollback(ctx)
			if err != nil || got != want {
				t.Fatalf("dependency omission case=%d generic=%t: got=%s want=%s err=%v", i, generic, got, want, err)
			}
		}
	}
}

func TestIssue138DependencyQueryPlan(t *testing.T) {
	raw := os.Getenv("INDEXCORE_ISSUE138_DEPENDENCY_PLAN")
	if raw == "" {
		t.Skip("opt-in owned PG18 dependency EXPLAIN")
	}
	if raw != "50000" {
		t.Fatal("plan fixture must remain50000")
	}
	pool := testutil.Issue138Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	root := reconcile.NewUUID()
	if err := st.CreateRoot(ctx, pool, root, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	i := 0
	_, err := pool.CopyFrom(ctx, pgx.Identifier{"index_canonical_resource"}, []string{"resource_id", "root_id", "introduced_at_generation", "last_confirmed_generation", "resource_presence", "removal_evidence_state", "canonical_path", "name", "is_dir", "size"}, pgx.CopyFromFunc(func() ([]any, error) {
		if i == 50000 {
			return nil, nil
		}
		j := i
		i++
		return []any{reconcile.NewUUID(), root, 0, 0, "PRESENT", "NONE", fmt.Sprintf("/d%06d", j), fmt.Sprintf("d%06d", j), true, int64(0)}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	size := int64(100000)
	where, args := dependencyQuery(root, "/d000000", []domain.SnapshotEntry{{Size: &size}})
	for _, variant := range []string{"whole-root-reference", "pushed-dependencies"} {
		sql, params := priorQuery("c.root_id=$1::uuid"), []any{root}
		if variant == "pushed-dependencies" {
			sql, params = priorQuery(where), args
		}
		var rawPlan []byte
		if err := pool.QueryRow(ctx, `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) `+sql, params...).Scan(&rawPlan); err != nil {
			t.Fatal(err)
		}
		var decoded []struct {
			Plan struct {
				ActualRows float64 `json:"Actual Rows"`
			} `json:"Plan"`
			ExecutionTime float64 `json:"Execution Time"`
		}
		if err := json.Unmarshal(rawPlan, &decoded); err != nil || len(decoded) != 1 {
			t.Fatal(err)
		}
		expected := 50000
		if variant == "pushed-dependencies" {
			expected = 1
		}
		if decoded[0].Plan.ActualRows != float64(expected) {
			t.Fatal("unexpected dependency projection cardinality", variant, decoded)
		}
		receipt, _ := json.Marshal(map[string]any{"variant": variant, "preseeded": 50000, "actual_returned_rows": decoded[0].Plan.ActualRows, "execution_ms": decoded[0].ExecutionTime, "plan": json.RawMessage(rawPlan)})
		t.Log("ISSUE138_DEPENDENCY_PLAN " + string(receipt))
	}
}
