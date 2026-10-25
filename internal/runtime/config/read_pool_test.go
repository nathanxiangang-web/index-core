package config

import (
	"flag"
	"testing"
	"time"
)

func TestIssue138PoolConfigDefaultsAndBounds(t *testing.T) {
	c := Defaults()
	c.DatabaseURL = "isolated"
	if c.IncrementalReadWorkers != 1 {
		t.Fatal("not default serial")
	}
	for _, n := range []int{1, 2, 4, 8} {
		c.IncrementalReadWorkers = n
		if e := c.Validate(); e != nil {
			t.Fatal(e)
		}
	}
	for _, n := range []int{-1, 0, 3, 5, 9, 100} {
		c.IncrementalReadWorkers = n
		if e := c.Validate(); e == nil {
			t.Fatal("unbounded readers", n)
		}
	}
	c.IncrementalReadWorkers = 1
	for _, d := range []time.Duration{0, -time.Second, time.Minute + 1} {
		c.SourceGlobalInterval = d
		if e := c.Validate(); e == nil {
			t.Fatal("invalid spacing accepted", d)
		}
	}
}
func TestIssue138PoolEnvironmentAndFlags(t *testing.T) {
	t.Setenv("INDEXCORE_INCREMENTAL_READ_WORKERS", "4")
	t.Setenv("INDEXCORE_SOURCE_GLOBAL_INTERVAL", "7ms")
	c, e := Load()
	if e != nil {
		t.Fatal(e)
	}
	if c.IncrementalReadWorkers != 4 || c.SourceGlobalInterval != 7*time.Millisecond {
		t.Fatal("environment not applied")
	}
	fs := flag.NewFlagSet("owned", flag.ContinueOnError)
	c.RegisterFlags(fs)
	if e := fs.Parse([]string{"--incremental-read-workers=2", "--source-root-interval=12ms"}); e != nil {
		t.Fatal(e)
	}
	if c.IncrementalReadWorkers != 2 || c.SourceRootInterval != 12*time.Millisecond {
		t.Fatal("flags not applied")
	}
}
