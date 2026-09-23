package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/internal/transport/httpapi"
	"github.com/nathanxiangang-web/index-core/testutil"
)

// G3-R10(3): the HTTP layer maps a generation-bound cursor to 409 stale_cursor.
func TestHTTPStaleCursorMapping(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	testutil.ResetSchema(t, pool)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := postgres.New(pool)
	const rootID = "e5000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if err := st.UpsertRootPolicy(ctx, rootID, postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	cfg, _ := st.RootReconcileConfig(ctx, rootID)
	coord := postgres.NewCoordinator(st, cfg)

	processSnapshot(t, st, coord, ctx, rootID, "e5000000-0000-0000-0000-0000000000a1",
		[]string{"a.txt", "b.txt"})
	srv := httpapi.New(httpapi.Deps{
		Query: postgres.NewQueryReader(pool), Readiness: postgres.NewReadiness(pool), Version: "test",
	})
	code, body := do(t, srv, http.MethodGet, "/v1/roots/"+rootID+"/active?limit=1", nil)
	if code != http.StatusOK {
		t.Fatalf("page1 must be 200, got %d", code)
	}
	next, _ := body["next_cursor"].(string)
	if next == "" {
		t.Fatalf("page1 must return a next cursor, got %v", body)
	}

	// Advance the generation.
	processSnapshot(t, st, coord, ctx, rootID, "e5000000-0000-0000-0000-0000000000a2",
		[]string{"a.txt", "b.txt", "c.txt"})

	code2, body2 := do(t, srv, http.MethodGet,
		"/v1/roots/"+rootID+"/active?limit=1&cursor="+url.QueryEscape(next), nil)
	if code2 != http.StatusConflict || body2["error"] != "stale_cursor" {
		t.Fatalf("stale cursor must map to 409 stale_cursor over HTTP, got %d %v", code2, body2)
	}
}

func processSnapshot(t *testing.T, st *postgres.Store, coord *postgres.Coordinator, ctx context.Context, rootID, snapID string, names []string) {
	t.Helper()
	alg, scope, mt := "sha256", "root", time.Now().UTC()
	count := int64(len(names))
	fresh := domain.FreshDirect
	strong := domain.StrongFailureVisibility
	if err := st.InsertSnapshotStub(ctx, st.Pool(), domain.Snapshot{
		SnapshotID: snapID, RootID: rootID, Provenance: []byte(`{}`), ObservedAt: mt,
		TraversalStatus: domain.TraversalSuccess, SkippedScopesKnownEmpty: true,
		FreshnessEvidence: &fresh, CollectorCompletenessAssurance: &strong,
		CompletenessFlag: domain.CompletenessFlagComplete, LifecycleState: domain.SnapshotDraft, EntryCount: &count,
	}); err != nil {
		t.Fatalf("stub: %v", err)
	}
	if err := st.MarkSnapshotSubmitted(ctx, st.Pool(), snapID); err != nil {
		t.Fatalf("submit: %v", err)
	}
	for i, name := range names {
		hash := fmt.Sprintf("h-%s", name)
		size := int64(i + 1)
		prov := fmt.Sprintf("P-%s", name)
		if err := st.InsertSnapshotEntry(ctx, st.Pool(), domain.SnapshotEntry{
			SnapshotID: snapID, EntryLocalID: name, Name: name, ParentRef: "/",
			Size: &size, Mtime: &mt, ContentHash: &hash, HashAlgorithm: &alg,
			ProviderObjectID: &prov, ProviderObjectIDScope: &scope,
		}); err != nil {
			t.Fatalf("entry: %v", err)
		}
	}
	if _, err := coord.ProcessSnapshot(ctx, rootID, snapID); err != nil {
		t.Fatalf("process: %v", err)
	}
}
