package config

import (
	"flag"
	"testing"
	"time"
)

func TestP10RuntimeDisabledByDefault(t *testing.T) {
	c := Defaults()
	if c.IncrementalRuntimeEnabled {
		t.Fatal("P10 runtime must be disabled by default")
	}
	if c.IncrementalWakeInterval != 5*time.Second {
		t.Fatalf("default wake interval = %s, want 5s", c.IncrementalWakeInterval)
	}
	c.DatabaseURL = "postgres://localhost/db"
	if err := c.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
}

func TestP10WakeIntervalValidation(t *testing.T) {
	base := func(d time.Duration) Config {
		c := Defaults()
		c.DatabaseURL = "postgres://localhost/db"
		c.IncrementalRuntimeEnabled = true
		c.IncrementalWakeInterval = d
		return c
	}
	for _, d := range []time.Duration{500 * time.Millisecond, 61 * time.Second, 0} {
		if err := base(d).Validate(); err == nil {
			t.Fatalf("wake interval %s must be rejected when enabled", d)
		}
	}
	for _, d := range []time.Duration{time.Second, 60 * time.Second} {
		if err := base(d).Validate(); err != nil {
			t.Fatalf("wake interval %s must be accepted: %v", d, err)
		}
	}
	// Ignored while disabled.
	c := Defaults()
	c.DatabaseURL = "postgres://localhost/db"
	c.IncrementalWakeInterval = 500 * time.Millisecond
	if err := c.Validate(); err != nil {
		t.Fatalf("wake interval must be ignored while disabled: %v", err)
	}
}

func TestP10RuntimeFlags(t *testing.T) {
	c := Defaults()
	c.DatabaseURL = "postgres://localhost/db"
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	c.RegisterFlags(fs)
	if fs.Lookup("incremental-runtime") == nil {
		t.Fatal("--incremental-runtime flag must exist")
	}
	if fs.Lookup("incremental-wake-interval") == nil {
		t.Fatal("--incremental-wake-interval flag must exist")
	}
	if err := fs.Parse([]string{"-incremental-runtime", "-incremental-wake-interval=10s"}); err != nil {
		t.Fatal(err)
	}
	if !c.IncrementalRuntimeEnabled || c.IncrementalWakeInterval != 10*time.Second {
		t.Fatalf("flags not applied: %+v", c)
	}
}

func TestP10RuntimeEnvLoad(t *testing.T) {
	t.Setenv("INDEXCORE_INCREMENTAL_RUNTIME_ENABLED", "true")
	t.Setenv("INDEXCORE_INCREMENTAL_WAKE_INTERVAL", "30s")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.IncrementalRuntimeEnabled || c.IncrementalWakeInterval != 30*time.Second {
		t.Fatalf("env not loaded: %+v", c)
	}
	t.Setenv("INDEXCORE_INCREMENTAL_RUNTIME_ENABLED", "not-a-bool")
	if _, err := Load(); err == nil {
		t.Fatal("invalid bool env must fail")
	}
}
