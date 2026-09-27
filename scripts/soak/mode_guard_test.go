package soak_test

import (
	"os/exec"
	"testing"
)

func TestP12ModeGuard(t *testing.T) {
	cmd := exec.Command("bash", "test-p12-mode-guard.sh")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("P12 mode guard failed: %v\n%s", err, output)
	}
}
