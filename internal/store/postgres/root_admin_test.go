package postgres_test

import (
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func TestRootLifecycleJournalAndGeneration(t *testing.T) {
	st, ctx := newStore(t)
	const rootID = "a0000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}

	dep, err := st.TransitionRootLifecycle(ctx, rootID, domain.RootDeprecated)
	if err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	if dep.From != domain.RootActive || dep.To != domain.RootDeprecated || dep.Generation != 1 {
		t.Fatalf("deprecate must ACTIVE->DEPRECATED at generation 1, got %+v", dep)
	}
	if dep.EventSeq == nil || *dep.EventSeq != 1 {
		t.Fatalf("deprecate must emit journal event seq 1, got %+v", dep.EventSeq)
	}

	del, err := st.TransitionRootLifecycle(ctx, rootID, domain.RootDeleted)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if del.From != domain.RootDeprecated || del.To != domain.RootDeleted || del.Generation != 2 {
		t.Fatalf("delete must DEPRECATED->DELETED at generation 2, got %+v", del)
	}
	if del.EventSeq == nil || *del.EventSeq != 2 {
		t.Fatalf("delete must emit journal event seq 2, got %+v", del.EventSeq)
	}

	events, err := st.ReadJournal(ctx, st.Pool(), rootID, 0, 10)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(events) != 2 || events[0].EventType != domain.EventRootDeprecated || events[1].EventType != domain.EventRootDeleted {
		t.Fatalf("journal must contain ordered [root-deprecated, root-deleted], got %+v", events)
	}
	if events[0].GenerationNumber != 1 || events[1].GenerationNumber != 2 {
		t.Fatalf("journal events must carry generations 1 then 2, got %d,%d", events[0].GenerationNumber, events[1].GenerationNumber)
	}

	if _, err := st.TransitionRootLifecycle(ctx, rootID, domain.RootActive); err == nil {
		t.Fatal("DELETED -> ACTIVE must be rejected (root_id never reused/revived)")
	}
}

func TestRootPolicyAndAdapterConfig(t *testing.T) {
	st, ctx := newStore(t)
	const rootID = "a0000000-0000-0000-0000-000000000002"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}

	pol := postgres.RootPolicy{
		RemovalGracePeriod: 2 * time.Hour, MoveRecognitionHorizon: time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}
	if err := st.UpsertRootPolicy(ctx, rootID, pol); err != nil {
		t.Fatalf("upsert policy: %v", err)
	}
	got, err := st.GetRootPolicy(ctx, rootID)
	if err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if got.RemovalGracePeriod != 2*time.Hour || got.MoveRecognitionHorizon != time.Hour {
		t.Fatalf("policy round-trip mismatch: %+v", got)
	}
	cfg, err := st.RootReconcileConfig(ctx, rootID)
	if err != nil {
		t.Fatalf("root reconcile config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("persisted policy must yield a valid config: %v", err)
	}

	adapterCfg := []byte(`{"remote":"local","path":"/data"}`)
	if err := st.UpsertAdapterConfig(ctx, rootID, postgres.AdapterConfig{CollectorKind: "rclone", Config: adapterCfg}); err != nil {
		t.Fatalf("upsert adapter: %v", err)
	}
	a, err := st.GetAdapterConfig(ctx, rootID)
	if err != nil {
		t.Fatalf("get adapter: %v", err)
	}
	if a.CollectorKind != "rclone" || len(a.Config) == 0 {
		t.Fatalf("adapter config mismatch: %+v", a)
	}
}

func TestRootPolicyRejectsGraceBelowHorizon(t *testing.T) {
	st, ctx := newStore(t)
	const rootID = "a0000000-0000-0000-0000-000000000003"
	if err := st.CreateRoot(ctx, st.Pool(), rootID, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatalf("create root: %v", err)
	}
	bad := postgres.RootPolicy{
		RemovalGracePeriod: time.Hour, MoveRecognitionHorizon: 2 * time.Hour,
		MinConsecutiveCompleteMissing: 1, MinIndependentConfirmations: 1,
	}
	if err := st.UpsertRootPolicy(ctx, rootID, bad); err == nil {
		t.Fatal("grace < horizon must be rejected by the schema invariant")
	}
}
