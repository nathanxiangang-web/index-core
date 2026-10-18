package app

import (
	"context"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
	"testing"
)

func TestRootWatchUsesExistingCASAndCanDisable(t *testing.T) {
	p7PrepareSchema(t)
	ctx := context.Background()
	pool := testutil.Pool(t)
	st := postgres.New(pool)
	const id = "f3000000-0000-0000-0000-000000000001"
	if err := st.CreateRoot(ctx, pool, id, []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal(err)
	}
	if err := rootWatch(ctx, p7BaseConfig(), []string{"--root-id", id, "--interval", "30s"}); err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWatch(ctx, id, "/")
	if err != nil || w.WatchState != state.WatchHot || w.EffectiveIntervalSeconds == nil || *w.EffectiveIntervalSeconds != 30 {
		t.Fatalf("watch %+v %v", w, err)
	}
	version := w.Version
	if err := rootWatch(ctx, p7BaseConfig(), []string{"--root-id", id, "--disable"}); err != nil {
		t.Fatal(err)
	}
	w, err = st.GetWatch(ctx, id, "/")
	if err != nil || w.WatchState != state.WatchDisabled || w.NextDueAt != nil || w.EffectiveIntervalSeconds != nil || w.Version <= version {
		t.Fatalf("disabled watch %+v %v", w, err)
	}
}

func TestRootWatchRejectsUnsafeIntervalsBeforeDatabase(t *testing.T) {
	for _, interval := range []string{"0s", "1s", "25h"} {
		if err := rootWatch(context.Background(), p7BaseConfig(), []string{"--root-id", "any", "--interval", interval}); err == nil {
			t.Fatal("accepted " + interval)
		}
	}
}
