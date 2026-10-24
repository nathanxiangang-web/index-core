package incrementalorch

import (
	"testing"
	"time"
)

func TestIssue134MirrorDueFairnessBudget(t *testing.T) {
	c := Config{MirrorRevisitEnabled: true, MaxDueWatchAttempts: 1, MaxExecuteItems: 5, MaxWallTime: time.Second}
	if c.Validate() == nil {
		t.Fatal("one slot cannot protect both due cohorts")
	}
	c.MaxDueWatchAttempts = 2
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.MirrorRevisitEnabled = false
	c.MaxDueWatchAttempts = 1
	if err := c.Validate(); err != nil {
		t.Fatal("legacy single attempt changed", err)
	}
}
