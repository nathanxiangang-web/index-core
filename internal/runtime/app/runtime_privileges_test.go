package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/query"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"github.com/nathanxiangang-web/index-core/testutil"
)

func TestIssue134RollingRuntimeAccountPreflight(t *testing.T) {
	st, h, _, roots := poolFixture(t, 1, 2)
	owner := st.Pool()
	ctx := context.Background()
	limited, role := testutil.Issue138RestrictedLogin(t, owner)
	testutil.ApplyRuntimeGrants(t, owner, role, false)
	u, err := url.Parse(testutil.TestDSN())
	if err != nil {
		t.Fatal("test DSN parse failed")
	}
	u.User = url.UserPassword(role, limited.Config().ConnConfig.Password)
	cfg := poolConfig(1)
	cfg.DatabaseURL = u.String()
	cfg.HTTPAddr = p9FreeAddr(t)
	if err := Doctor(ctx, cfg, poolLogger()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `REVOKE INSERT ON public.index_mirror_root_budget FROM `+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	before := h.api.Load()
	for _, run := range []struct {
		name string
		f    func() error
	}{
		{"doctor", func() error { return Doctor(ctx, cfg, poolLogger()) }},
		{"serve", func() error {
			bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			return Serve(bounded, cfg, poolLogger())
		}},
	} {
		t.Run(run.name, func(t *testing.T) {
			err := run.f()
			if !errors.Is(err, postgres.ErrRuntimePrivileges) || !strings.Contains(err.Error(), "index_mirror_root_budget.INSERT") {
				t.Fatal("preflight missed missing registration grant", err)
			}
		})
	}
	if h.api.Load() != before {
		t.Fatal("failed preflight reached source")
	}
	testutil.ApplyRuntimeGrants(t, owner, role, false)
	appStore := postgres.New(limited)
	if err := appStore.CreateRoot(ctx, limited, "13450000-0000-4000-8000-000000000001", []byte(`{}`), domain.RootActive); err != nil {
		t.Fatal("F4-off Root creation", err)
	}
	cycle, err := buildIncrementalCycle(appStore, cfg, poolLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle); err != nil {
		t.Fatal(err)
	}
	qr := postgres.NewQueryReader(limited)
	visible, err := qr.ResolvePath(ctx, roots[0], "/d000000/late.bin", query.ReadOptions{})
	if err != nil || len(visible.Matches) != 1 {
		t.Fatal("non-owner Q5", err)
	}
	events, err := qr.ReadJournal(ctx, roots[0], 0, 100)
	if err != nil || len(events) != 2 {
		t.Fatal("non-owner Kernel/Journal", err)
	}
	// Privilege profiles have no implicit F4 enablement. Test planned ON access
	// read-only, without starting serve or changing any production settings.
	cfg.MirrorRollingPercent = 25
	cfg.IncrementalMirrorRevisit = 2 * time.Minute
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := Doctor(ctx, cfg, poolLogger()); !errors.Is(err, postgres.ErrRuntimePrivileges) {
		t.Fatal("rolling grants not checked", err)
	}
	testutil.ApplyRuntimeGrants(t, owner, role, true)
	if err := Doctor(ctx, cfg, poolLogger()); err != nil {
		t.Fatal(err)
	}
	// Actual F4 admission, binding and serial Kernel completion with the same
	// non-owner LOGIN; no migration role or SET ROLE in the runtime connection.
	now := time.Now().UTC().Add(-time.Second)
	seconds := int64(120)
	if _, err := appStore.CreateWatch(ctx, state.ScopeWatchState{RootID: roots[0], ScopeKey: "/d000000", WatchState: state.WatchWarm, CadenceClass: "MIRROR_REVISIT", EffectiveIntervalSeconds: &seconds, SourceSet: []state.WatchSource{state.WatchSourceBackstopEnroll}, Priority: state.PriorityNormal, NextDueAt: &now}); err != nil {
		t.Fatal(err)
	}
	cycle, err = buildIncrementalCycle(appStore, cfg, poolLogger())
	if err != nil {
		t.Fatal(err)
	}
	result, err := cycle.RunCycle(ctx, runtimeConfig(cfg).Cycle)
	if err != nil || result.DueEmitted != 1 || result.Executor.Succeeded != 1 {
		t.Fatal("non-owner F4 runtime", result, err)
	}
	stats, err := appStore.MirrorRollingStats(ctx, time.Now())
	if err != nil || stats.Emitted != 1 || stats.OutstandingRoots != 0 || stats.Renewals != 1 {
		t.Fatal(stats, err)
	}
}
