package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestP7TopLevelHelpListsIncremental(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	out := buf.String()
	if !strings.Contains(out, "incremental") {
		t.Fatalf("top-level usage must list incremental:\n%s", out)
	}
	if !strings.Contains(out, "indexcore incremental run") {
		t.Fatalf("usage must document `indexcore incremental run`:\n%s", out)
	}
	if !strings.Contains(out, "5/5/60s") {
		t.Fatalf("usage must state the hard bounds:\n%s", out)
	}
}

func TestP7DispatchMissingAndUnknownIncrementalSubcommand(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("empty args must fail")
	}
	err := run([]string{"incremental"})
	if err == nil {
		t.Fatal("incremental without a subcommand must fail")
	}
	if !strings.Contains(err.Error(), "subcommand") {
		t.Fatalf("unexpected error: %v", err)
	}
	err = run([]string{"incremental", "watch"})
	if err == nil {
		t.Fatal("unknown incremental subcommand must fail")
	}
	if !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("unexpected error: %v", err)
	}
	err = run([]string{"incremental", "daemon"})
	if err == nil {
		t.Fatal("reserved incremental subcommand must fail in P7")
	}
}

func TestP7DispatchRunSubcommandFailsOnConfigNotUnknown(t *testing.T) {
	// `incremental run` with a bad budget must be dispatched to the run path and
	// fail on validation, not be reported as an unknown subcommand.
	t.Setenv("INDEXCORE_DATABASE_URL", "postgres://127.0.0.1:1/never")
	err := run([]string{"incremental", "run", "--max-due-watch-attempts", "0"})
	if err == nil {
		t.Fatal("invalid run budget must fail")
	}
	if strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("`run` must be a known subcommand, got %v", err)
	}
}
