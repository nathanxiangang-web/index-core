package app

import (
	"context"
	"errors"
	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/incremental/state"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
	"time"
)

// root watch uses existing watch/CAS state and the existing runtime; no second
// scheduler or provider traversal is created. DISABLED preserves watch history.
func rootWatch(ctx context.Context, base config.Config, args []string) error {
	fs, cfg := rootFlagSet("root watch", base)
	rootID := fs.String("root-id", "", "root UUID")
	interval := fs.Duration("interval", 30*time.Second, "root polling interval (15s..24h)")
	disable := fs.Bool("disable", false, "disable this root's recurring watch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *rootID == "" || *interval < 15*time.Second || *interval > 24*time.Hour {
		return errors.New("root watch: root-id and interval 15s..24h required; no positionals")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	st, pool, err := openStore(ctx, *cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	r, err := st.GetRoot(ctx, pool, *rootID)
	if err != nil {
		return err
	}
	if r.LifecycleState != domain.RootActive {
		return errors.New("root watch requires ACTIVE root")
	}
	w, err := st.GetWatch(ctx, *rootID, "/")
	create := errors.Is(err, postgres.ErrNotFound)
	if err != nil && !create {
		return err
	}
	if create {
		w = state.ScopeWatchState{RootID: *rootID, ScopeKey: "/", Priority: state.PriorityNormal, CadenceClass: "SOURCE_MIRROR", SourceSet: []state.WatchSource{state.WatchSourceOperatorPolicy}}
	}
	if *disable {
		w.WatchState = state.WatchDisabled
		w.EffectiveIntervalSeconds = nil
		w.NextDueAt = nil
	} else {
		seconds := int64(interval.Seconds())
		now := time.Now().UTC()
		w.WatchState = state.WatchHot
		w.EffectiveIntervalSeconds = &seconds
		w.NextDueAt = &now
	}
	if create {
		_, err = st.CreateWatch(ctx, w)
	} else {
		_, err = st.CASUpdateWatch(ctx, w, w.Version)
	}
	if err != nil {
		return err
	}
	writeJSON(map[string]any{"root_id": *rootID, "scope": "/", "watch_state": w.WatchState})
	return nil
}
