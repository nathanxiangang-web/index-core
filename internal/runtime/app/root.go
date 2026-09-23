package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nathanxiangang-web/index-core/internal/domain"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

// Root implements `indexcore root ...` (Gate 3 P3). All lifecycle mutations use
// the frozen Kernel transaction/journal semantics. Each subcommand parses its own
// config flags so `root <sub> --database-url ...` works.
func Root(ctx context.Context, base config.Config, logger *slog.Logger, args []string) error {
	if len(args) == 0 {
		return errors.New("root: missing subcommand (create|list|activate|deprecate|delete|config|adapter)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return rootCreate(ctx, base, logger, rest)
	case "list":
		return rootList(ctx, base, rest)
	case "activate":
		return rootTransition(ctx, base, logger, rest, domain.RootActive)
	case "deprecate":
		return rootTransition(ctx, base, logger, rest, domain.RootDeprecated)
	case "delete":
		return rootTransition(ctx, base, logger, rest, domain.RootDeleted)
	case "config":
		return rootConfig(ctx, base, logger, rest)
	case "adapter":
		return rootAdapter(ctx, base, rest)
	default:
		return fmt.Errorf("root: unknown subcommand %q", sub)
	}
}

func rootFlagSet(name string, base config.Config) (*flag.FlagSet, *config.Config) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := base
	cfg.RegisterFlags(fs)
	return fs, &cfg
}

func openStore(ctx context.Context, cfg config.Config) (*postgres.Store, *pgxpool.Pool, error) {
	pool, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("ping database: %w", err)
	}
	return postgres.New(pool), pool, nil
}

func rootCreate(ctx context.Context, base config.Config, logger *slog.Logger, args []string) error {
	fs, cfg := rootFlagSet("root create", base)
	rootID := fs.String("root-id", "", "root UUID (immutable; never reused)")
	scope := fs.String("scope", "{}", "scope descriptor JSON")
	lifecycle := fs.String("lifecycle", "NEW", "NEW|ACTIVE")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if *rootID == "" {
		return errors.New("root create: --root-id is required")
	}
	lc := domain.RootLifecycleState(strings.ToUpper(*lifecycle))
	if lc != domain.RootNew && lc != domain.RootActive {
		return fmt.Errorf("root create: --lifecycle must be NEW or ACTIVE, got %q", *lifecycle)
	}
	st, pool, err := openStore(ctx, *cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := st.CreateRoot(ctx, st.Pool(), *rootID, []byte(*scope), lc); err != nil {
		return fmt.Errorf("root create: %w", err)
	}
	logger.Info("root created", "root_id", *rootID, "lifecycle", string(lc))
	return nil
}

func rootList(ctx context.Context, base config.Config, args []string) error {
	fs, cfg := rootFlagSet("root list", base)
	incDep := fs.Bool("include-deprecated", false, "include DEPRECATED roots")
	incDel := fs.Bool("include-deleted", false, "include DELETED roots")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	st, pool, err := openStore(ctx, *cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	roots, err := st.ListRoots(ctx, st.Pool(), *incDep, *incDel)
	if err != nil {
		return err
	}
	for _, r := range roots {
		owner := ""
		if r.OwningCollectorRef != nil {
			owner = *r.OwningCollectorRef
		}
		fmt.Printf("%s\t%s\tgen=%d\tcollector=%s\n", r.RootID, r.LifecycleState, r.CurrentGeneration, owner)
	}
	return nil
}

func rootTransition(ctx context.Context, base config.Config, logger *slog.Logger, args []string, next domain.RootLifecycleState) error {
	fs, cfg := rootFlagSet("root "+strings.ToLower(string(next)), base)
	rootID := fs.String("root-id", "", "root UUID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if *rootID == "" {
		return errors.New("--root-id is required")
	}
	st, pool, err := openStore(ctx, *cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	res, err := st.TransitionRootLifecycle(ctx, *rootID, next)
	if err != nil {
		return err
	}
	out := map[string]any{"root_id": *rootID, "from": string(res.From), "to": string(res.To), "generation": res.Generation}
	if res.EventSeq != nil {
		out["event_seq"] = *res.EventSeq
	}
	writeJSON(out)
	logger.Info("root lifecycle transition", "root_id", *rootID, "from", string(res.From), "to", string(res.To), "generation", res.Generation)
	return nil
}

func rootConfig(ctx context.Context, base config.Config, logger *slog.Logger, args []string) error {
	if len(args) == 0 {
		return errors.New("root config: missing subcommand (set|get)")
	}
	sub, rest := args[0], args[1:]
	fs, cfg := rootFlagSet("root config "+sub, base)
	rootID := fs.String("root-id", "", "root UUID")
	grace := fs.Duration("grace", 0, "removal grace period")
	horizon := fs.Duration("move-horizon", 0, "move-recognition horizon")
	minConsec := fs.Int("min-consecutive", 1, "min consecutive COMPLETE missing")
	minIndep := fs.Int("min-independent", 1, "min independent confirmations")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if *rootID == "" {
		return errors.New("root config: --root-id is required")
	}
	st, pool, err := openStore(ctx, *cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch sub {
	case "set":
		p := postgres.RootPolicy{
			RemovalGracePeriod: *grace, MoveRecognitionHorizon: *horizon,
			MinConsecutiveCompleteMissing: *minConsec, MinIndependentConfirmations: *minIndep,
		}
		if err := p.ReconcileConfig().Validate(); err != nil {
			return fmt.Errorf("invalid policy: %w", err)
		}
		if err := st.UpsertRootPolicy(ctx, *rootID, p); err != nil {
			return err
		}
		logger.Info("root policy set", "root_id", *rootID, "grace", grace.String(), "move_horizon", horizon.String())
		return nil
	case "get":
		p, err := st.GetRootPolicy(ctx, *rootID)
		if err != nil {
			return err
		}
		writeJSON(map[string]any{
			"root_id":                          *rootID,
			"removal_grace_period":             p.RemovalGracePeriod.String(),
			"move_recognition_horizon":         p.MoveRecognitionHorizon.String(),
			"min_consecutive_complete_missing": p.MinConsecutiveCompleteMissing,
			"min_independent_confirmations":    p.MinIndependentConfirmations,
		})
		return nil
	default:
		return fmt.Errorf("root config: unknown subcommand %q", sub)
	}
}

func rootAdapter(ctx context.Context, base config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("root adapter: missing subcommand (set|get)")
	}
	sub, rest := args[0], args[1:]
	fs, cfg := rootFlagSet("root adapter "+sub, base)
	rootID := fs.String("root-id", "", "root UUID")
	collector := fs.String("collector", "rclone", "collector kind")
	cfgJSON := fs.String("config", "{}", "adapter configuration JSON (provider-specific)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if *rootID == "" {
		return errors.New("root adapter: --root-id is required")
	}
	st, pool, err := openStore(ctx, *cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch sub {
	case "set":
		if !json.Valid([]byte(*cfgJSON)) {
			return errors.New("root adapter set: --config must be valid JSON")
		}
		if err := st.UpsertAdapterConfig(ctx, *rootID, postgres.AdapterConfig{CollectorKind: *collector, Config: []byte(*cfgJSON)}); err != nil {
			return err
		}
		fmt.Printf("adapter set for %s (collector=%s)\n", *rootID, *collector)
		return nil
	case "get":
		a, err := st.GetAdapterConfig(ctx, *rootID)
		if err != nil {
			return err
		}
		writeJSON(map[string]any{"root_id": *rootID, "collector_kind": a.CollectorKind, "config": json.RawMessage(a.Config)})
		return nil
	default:
		return fmt.Errorf("root adapter: unknown subcommand %q", sub)
	}
}

func writeJSON(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}
