package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// A small plan/correctness probe, not a scale or throughput acceptance test.
func TestIssue134RollingWholeRootPriorLatestIndex(t *testing.T) {
	pool := testutil.Pool(t)
	testutil.ResetSchema(t, pool)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	const root = "13440000-0000-4000-8000-000000000001"
	if err := st.CreateRoot(ctx, pool, root, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO index_canonical_resource(resource_id,root_id,introduced_at_generation,last_confirmed_generation,resource_presence,removal_evidence_state,canonical_path)
 SELECT md5(i::text)::uuid,$1,0,0,'PRESENT','NONE','/scope-'||i FROM generate_series(1,128) i`, root); err != nil {
		t.Fatal(err)
	}
	// Insert order deliberately disagrees with observed_at, and one quarter of
	// resources have no observations. The current projection is deliberately false.
	if _, err := pool.Exec(ctx, `INSERT INTO index_identity_evidence_observation(resource_id,snapshot_id,observed_at,provider_object_id,provider_object_id_scope,provider_identity_assurance,is_dir)
 SELECT md5(i::text)::uuid,'13440000-0000-4000-8000-000000000002',now()-n*interval '1 second','version-'||n,'fixture','STABLE_WITHIN_SCOPE',false
 FROM generate_series(1,128) i CROSS JOIN generate_series(1,24) n WHERE i%4<>0 ORDER BY n,i;
 INSERT INTO index_identity_evidence_current(resource_id,provider_object_id,provider_object_id_scope,provider_identity_assurance)
 SELECT resource_id,'false-cache','fixture','UNVERIFIED' FROM index_canonical_resource;
 DROP INDEX idx_identity_resource_latest;
 DELETE FROM schema_migrations WHERE version='0008_latest_identity_observation.sql';
 ANALYZE index_canonical_resource; ANALYZE index_identity_evidence_observation;`); err != nil {
		t.Fatal(err)
	}
	before, err := st.LoadPriorResources(ctx, pool, root)
	if err != nil || len(before) != 128 {
		t.Fatal(len(before), err)
	}
	for _, p := range before {
		if p.ProviderObjectID != nil && *p.ProviderObjectID != "version-24" {
			t.Fatal("not latest observation_id", p)
		}
	}
	plans := map[string]json.RawMessage{}
	for _, variant := range []string{"before", "after"} {
		if variant == "after" {
			if err := Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
		}
		var raw []byte
		if err := pool.QueryRow(ctx, `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) `+priorQuery("c.root_id=$1::uuid"), root).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		plans[variant] = json.RawMessage(raw)
	}
	if !strings.Contains(string(plans["after"]), "idx_identity_resource_latest") {
		t.Fatal("latest-row index not used", string(plans["after"]))
	}
	after, err := st.LoadPriorResources(ctx, pool, root)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("index changed authoritative context", err)
	}
	// Evidence-only commits need not advance canonical generation. A generation-
	// only process cache would already be stale on the next READ COMMITTED read.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := st.LoadPriorResources(ctx, tx, root); err != nil {
		t.Fatal(err)
	}
	id, scope := "new-evidence-only", "fixture"
	resource := before[0].ResourceID
	if err := st.InsertIdentityEvidence(ctx, pool, domain.IdentityEvidenceObservation{ResourceID: resource, SnapshotID: "13440000-0000-4000-8000-000000000003", ObservedAt: time.Now().Add(-time.Hour), ProviderObjectID: &id, ProviderObjectIDScope: &scope, ProviderIdentityAssurance: domain.IdentityUnverified}); err != nil {
		t.Fatal(err)
	}
	latest, err := st.LoadPriorResources(ctx, tx, root)
	if err != nil || latest[0].ProviderObjectID == nil || *latest[0].ProviderObjectID != id {
		t.Fatal("stale prior reuse", err)
	}
	receipt, _ := json.Marshal(map[string]any{"resources": 128, "initial_observations": 2304, "rows_unchanged": len(after), "sql": priorQuery("c.root_id=$1::uuid"), "plans": plans})
	t.Log("ISSUE134_PRIOR_PLAN " + string(receipt))
}
