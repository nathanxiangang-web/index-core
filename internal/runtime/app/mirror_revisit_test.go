package app

import (
	"testing"
	"time"

	"github.com/nathanxiangang-web/index-core/internal/runtime/config"
)

func TestIssue134MirrorRuntimeComposition(t *testing.T) {
	c := config.Defaults()
	if runtimeConfig(c).Cycle.MirrorRevisitEnabled {
		t.Fatal("fair materialization default-on")
	}
	c.IncrementalMirrorRevisit = 2 * time.Minute
	r := runtimeConfig(c)
	if !r.Cycle.MirrorRevisitEnabled || r.Cycle.MaxDueWatchAttempts != 5 || r.Cycle.MaxExecuteItems != 5 || c.IncrementalFirstDiscovery {
		t.Fatal("composition changed caps or enabled F1", r)
	}
}
