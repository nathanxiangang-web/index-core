package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nathanxiangang-web/index-core/internal/diagnostic"
	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
	"github.com/nathanxiangang-web/index-core/internal/store/postgres"
)

func rootDiagnose(ctx context.Context, base config.Config, args []string) error {
	return runRootDiagnose(ctx, base, args, os.Stdout)
}

// A dedicated flag set deliberately excludes provider/rclone/runtime flags. It
// does not validate or initialize serve/scan configuration or resolve secrets.
func runRootDiagnose(ctx context.Context, base config.Config, args []string, out io.Writer) error {
	o := diagnostic.Defaults()
	fs := flag.NewFlagSet("root diagnose", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("root-id", "", "root UUID")
	dsn := fs.String("database-url", base.DatabaseURL, "database DSN; prefer protected INDEXCORE_DATABASE_URL")
	fs.IntVar(&o.RowLimit, "row-limit", o.RowLimit, "rows per dataset (1..10000)")
	fs.DurationVar(&o.Timeout, "timeout", o.Timeout, "total read timeout (1ms..30s)")
	fs.DurationVar(&o.StaleAfter, "stale-after", o.StaleAfter, "operator freshness/lag threshold (1s..24h)")
	if fs.Parse(args) != nil || fs.NArg() != 0 || o.Validate() != nil || !postgres.ValidDiagnosticRoot(*id) {
		return errors.New("root diagnose: invalid flags, limits or root UUID")
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(*dsn)
	if err == nil {
		cfg.MaxConns = 1
		cfg.MinConns = 0
		// No connection hook can mutate data. Startup parameters only, and the
		// reader also explicitly requests READ ONLY for every transaction.
		cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
		cfg.ConnConfig.RuntimeParams["search_path"] = "public,pg_catalog"
	}
	var r diagnostic.Report
	if err != nil || *dsn == "" {
		r = diagnostic.New(*id, o)
		r.Add("DATABASE_UNAVAILABLE", "ERROR", "Database configuration invalid; value omitted")
		r.Finish()
	} else {
		pool, e := pgxpool.NewWithConfig(ctx, cfg)
		if e != nil {
			r = diagnostic.New(*id, o)
			r.Add("DATABASE_UNAVAILABLE", "ERROR", "Database pool unavailable; error omitted")
			r.Finish()
		} else {
			r, _ = postgres.NewDiagnosticReader(pool).Read(ctx, *id, o)
			pool.Close()
		}
	}
	if json.NewEncoder(out).Encode(r) != nil {
		return errors.New("root diagnose: cannot write JSON")
	}
	if !r.Complete || r.Overall != "OK" {
		return errors.New("root diagnose: non-green evidence; inspect JSON checks (no recovery attempted)")
	}
	return nil
}
