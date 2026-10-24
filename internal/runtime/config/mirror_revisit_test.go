package config

import (
	"flag"
	"testing"
	"time"
)

func TestIssue134MirrorConfigOptIn(t *testing.T) {
	if Defaults().IncrementalMirrorRevisit != 0 || Defaults().IncrementalFirstDiscovery {
		t.Fatal("default-on optimization")
	}
	t.Setenv("INDEXCORE_INCREMENTAL_MIRROR_REVISIT", "2m")
	c, err := Load()
	if err != nil || c.IncrementalMirrorRevisit != 2*time.Minute || c.IncrementalFirstDiscovery {
		t.Fatal(c, err)
	}
	fs := flag.NewFlagSet("mirror", flag.ContinueOnError)
	c.RegisterFlags(fs)
	if err := fs.Parse([]string{"--incremental-mirror-revisit=0s"}); err != nil || c.IncrementalMirrorRevisit != 0 {
		t.Fatal(err)
	}
	for _, v := range []string{"-1s", "29s", "1h1s", "30.5s"} {
		t.Setenv("INDEXCORE_INCREMENTAL_MIRROR_REVISIT", v)
		c, err = Load()
		if err != nil {
			t.Fatal(err)
		}
		c.DatabaseURL = "postgres://fixture"
		if c.Validate() == nil {
			t.Fatal("invalid policy accepted", v)
		}
	}
	t.Setenv("INDEXCORE_INCREMENTAL_MIRROR_REVISIT", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("invalid duration accepted")
	}
}
