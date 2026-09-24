// Command indexcore is the standalone Alpha runtime for IndexCore.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nathanxiangang-web/index-core/internal/runtime/app"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/runtime/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "indexcore: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return errors.New("missing subcommand")
	}
	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return nil
	case "version":
		fmt.Println("indexcore " + version.String())
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Subcommand-bearing commands parse their own config flags after the
	// subcommand (e.g. `root create --database-url ...`), so they receive the
	// env-based config as a base and parse the remaining args themselves.
	if cmd == "root" || cmd == "scan" || cmd == "incremental" {
		base, err := config.Load()
		if err != nil {
			return err
		}
		logger := newLogger(base)
		switch cmd {
		case "root":
			return app.Root(ctx, base, logger, rest)
		case "scan":
			return app.Scan(ctx, base, logger, rest)
		default:
			return app.Incremental(ctx, base, logger, rest)
		}
	}

	cfg, _, err := loadConfig(cmd, rest)
	if err != nil {
		return err
	}
	logger := newLogger(cfg)

	switch cmd {
	case "migrate":
		return app.Migrate(ctx, cfg, logger)
	case "doctor":
		return app.Doctor(ctx, cfg, logger)
	case "serve":
		return app.Serve(ctx, cfg, logger)
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown subcommand %q", cmd)
	}
}

// loadConfig loads env defaults, applies command flags, validates, and returns
// the remaining positional arguments.
func loadConfig(cmd string, args []string) (config.Config, []string, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, nil, err
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return config.Config{}, nil, err
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, nil, err
	}
	return cfg, fs.Args(), nil
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `indexcore — provider-neutral canonical resource indexing runtime

Usage:
  indexcore <command> [flags]

Commands:
  migrate        apply SQL-first migrations (explicit; never automatic)
  serve          run the read-only HTTP /v1 transport
  doctor         validate database connectivity and schema compatibility
  root           root administration (create/list/config/lifecycle)
  scan           run the configured collector scan for a root
  incremental    run one bounded manual incremental orchestration cycle
  version        print build version
  help           print this help

Configuration is env + flags; see --help on each command.

`+"`"+`indexcore incremental run`+"`"+` is one-shot/manual only: it calls the accepted P6
orchestration exactly once under the existing single-writer advisory lock (an
active `+"`"+`serve`+"`"+` writer makes it fail closed), stays within the hard
prototype bounds 5/5/60s (max-due-watch-attempts<=5 / max-execute-items<=5 /
max-wall-time<=60s), and exits 0 on a normal bounded MAX_WALL_TIME. It never starts a scheduler, ticker,
background/daemon mode, or automatic recovery.
`)
}
