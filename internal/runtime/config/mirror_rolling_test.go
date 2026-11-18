package config

import (
	"testing"
	"time"
)

func TestIssue134RollingOptInAndBounds(t *testing.T) {
	c := Defaults()
	c.DatabaseURL = "postgres://unused"
	if c.MirrorRollingPercent != 0 || c.RuntimeQueryCosts {
		t.Fatal("new policy default-on")
	}
	for _, share := range []int{-1, 1, 50, 51} {
		c.MirrorRollingPercent = share
		if c.Validate() == nil {
			t.Fatal("rolling without hybrid F2 accepted", share)
		}
	}
	c.IncrementalRuntimeEnabled = true
	c.IncrementalMirrorRevisit = 2 * time.Minute
	for _, share := range []int{1, 25, 50} {
		c.MirrorRollingPercent = share
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	c.SourceProviderInterval = 200 * time.Millisecond
	c.MirrorRollingPercent = 25
	if c.MirrorRollingPolicy().AdmissionSpacing() != 6400*time.Millisecond {
		t.Fatal("wrong pessimistic admission envelope")
	}
	t.Setenv("INDEXCORE_MIRROR_ROLLING_PERCENT", "25")
	t.Setenv("INDEXCORE_RUNTIME_QUERY_COSTS", "true")
	loaded, err := Load()
	if err != nil || loaded.MirrorRollingPercent != 25 || !loaded.RuntimeQueryCosts {
		t.Fatal("rolling flags not loaded", err)
	}
}
