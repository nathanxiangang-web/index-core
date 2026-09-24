package config

import (
	"flag"
	"strings"
	"testing"
)

const p9Token = "0123456789abcdef0123456789abcdef" // exactly 32 bytes

func hintConfig(addr, token string) Config {
	c := Defaults()
	c.DatabaseURL = "postgres://localhost/db"
	c.HintAddr = addr
	c.HintToken = token
	return c
}

func TestP9HintDisabledByDefault(t *testing.T) {
	c := Defaults()
	if c.HintEnabled() {
		t.Fatal("hint transport must be disabled by default")
	}
	c.DatabaseURL = "postgres://localhost/db"
	if err := c.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
}

func TestP9HintAddrValidation(t *testing.T) {
	invalid := []string{
		"0.0.0.0:8081", "[::]:8081", "192.168.1.5:8081", "localhost:8081",
		"127.0.0.1", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:notaport",
		":8081", "127.0.0.1:70000", "127.0.0.1:8081extra",
	}
	for _, addr := range invalid {
		if err := hintConfig(addr, p9Token).Validate(); err == nil {
			t.Fatalf("hint addr %q must be rejected", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8081", "[::1]:8081"} {
		if err := hintConfig(addr, p9Token).Validate(); err != nil {
			t.Fatalf("hint addr %q must be accepted: %v", addr, err)
		}
	}
}

func TestP9HintTokenRequirements(t *testing.T) {
	for _, tok := range []string{"", "short", strings.Repeat("a", 31)} {
		if err := hintConfig("127.0.0.1:8081", tok).Validate(); err == nil {
			t.Fatalf("token of length %d must be rejected when the transport is enabled", len(tok))
		}
	}
	if err := hintConfig("127.0.0.1:8081", strings.Repeat("a", 32)).Validate(); err != nil {
		t.Fatalf("32-byte token must be accepted: %v", err)
	}
	// The token is irrelevant while the transport is disabled.
	if err := hintConfig("", "").Validate(); err != nil {
		t.Fatalf("disabled transport must not require a token: %v", err)
	}
}

func TestP9HintFlags(t *testing.T) {
	c := Defaults()
	c.DatabaseURL = "postgres://localhost/db"
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	c.RegisterFlags(fs)
	if fs.Lookup("hint-addr") == nil {
		t.Fatal("--hint-addr flag must exist")
	}
	if fs.Lookup("hint-token") != nil {
		t.Fatal("--hint-token flag must NOT exist (env-only secret)")
	}
	if err := fs.Parse([]string{"-hint-addr=127.0.0.1:9099"}); err != nil {
		t.Fatal(err)
	}
	if c.HintAddr != "127.0.0.1:9099" || !c.HintEnabled() {
		t.Fatalf("--hint-addr must enable the transport, got %q", c.HintAddr)
	}
}

func TestP9HintEnvLoad(t *testing.T) {
	t.Setenv("INDEXCORE_HINT_ADDR", "127.0.0.1:9098")
	t.Setenv("INDEXCORE_HINT_TOKEN", p9Token)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HintAddr != "127.0.0.1:9098" || c.HintToken != p9Token {
		t.Fatalf("hint env not loaded: %+v", c)
	}
}
