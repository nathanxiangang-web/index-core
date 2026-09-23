package config

import (
	"flag"
	"testing"
)

func TestDefaultsAreValidWithDSN(t *testing.T) {
	c := Defaults()
	c.DatabaseURL = "postgres://localhost/db"
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults must validate with a DSN: %v", err)
	}
	if !c.LoopbackOnly() {
		t.Fatalf("default HTTP address must be loopback-only, got %s", c.HTTPAddr)
	}
}

func TestValidateRejectsInvalid(t *testing.T) {
	cases := map[string]Config{
		"missing dsn":       {MaxConcurrentRoots: 1, ScanTimeout: 1, ShutdownTimeout: 1, LogFormat: "text", LogLevel: "info"},
		"zero concurrency":  {DatabaseURL: "x", HTTPAddr: "127.0.0.1:1", MaxConcurrentRoots: 0, ScanTimeout: 1, ShutdownTimeout: 1, LogFormat: "text", LogLevel: "info"},
		"bad log format":    {DatabaseURL: "x", HTTPAddr: "127.0.0.1:1", MaxConcurrentRoots: 1, ScanTimeout: 1, ShutdownTimeout: 1, LogFormat: "xml", LogLevel: "info"},
		"bad log level":     {DatabaseURL: "x", HTTPAddr: "127.0.0.1:1", MaxConcurrentRoots: 1, ScanTimeout: 1, ShutdownTimeout: 1, LogFormat: "text", LogLevel: "trace"},
		"zero scan timeout": {DatabaseURL: "x", HTTPAddr: "127.0.0.1:1", MaxConcurrentRoots: 1, ScanTimeout: 0, ShutdownTimeout: 1, LogFormat: "text", LogLevel: "info"},
	}
	for name, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestLoopbackDetection(t *testing.T) {
	loopback := []string{"127.0.0.1:8080", "[::1]:8080", "localhost:1234"}
	for _, a := range loopback {
		if !(Config{HTTPAddr: a}).LoopbackOnly() {
			t.Errorf("%s must be loopback", a)
		}
	}
	nonLoopback := []string{"0.0.0.0:8080", ":8080", "192.168.1.5:8080"}
	for _, a := range nonLoopback {
		if (Config{HTTPAddr: a}).LoopbackOnly() {
			t.Errorf("%s must NOT be loopback", a)
		}
	}
}

func TestFlagsOverrideEnvDefaults(t *testing.T) {
	c := Defaults()
	c.DatabaseURL = "postgres://env/db"
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	c.RegisterFlags(fs)
	if err := fs.Parse([]string{"-max-concurrent-roots=9", "-log-format=json"}); err != nil {
		t.Fatal(err)
	}
	if c.MaxConcurrentRoots != 9 || c.LogFormat != "json" {
		t.Fatalf("flags must override defaults, got %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}
