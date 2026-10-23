package config

import (
	"flag"
	"testing"
)

func TestIssue134FirstDiscoveryOptIn(t *testing.T) {
	if Defaults().IncrementalFirstDiscovery {
		t.Fatal("must be disabled by default")
	}
	t.Setenv("INDEXCORE_INCREMENTAL_FIRST_DISCOVERY_ENABLED", "true")
	c, err := Load()
	if err != nil || !c.IncrementalFirstDiscovery {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("first", flag.ContinueOnError)
	c.RegisterFlags(fs)
	if err = fs.Parse([]string{"--incremental-first-discovery=false"}); err != nil || c.IncrementalFirstDiscovery {
		t.Fatal("flag override failed", err)
	}
	t.Setenv("INDEXCORE_INCREMENTAL_FIRST_DISCOVERY_ENABLED", "invalid")
	if _, err = Load(); err == nil {
		t.Fatal("invalid boolean accepted")
	}
}
