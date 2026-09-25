// Package config defines the Gate 3 Alpha runtime configuration. Configuration
// is env + flags only (no config files, no framework). Invalid configuration
// must fail before any background work starts.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the runtime configuration for the indexcore process.
type Config struct {
	DatabaseURL        string        `json:"-"`
	HTTPAddr           string        `json:"http_addr"`
	RclonePath         string        `json:"rclone_path"`
	RcloneConfig       string        `json:"rclone_config"`
	MaxConcurrentRoots int           `json:"max_concurrent_roots"`
	ScanTimeout        time.Duration `json:"scan_timeout"`
	ShutdownTimeout    time.Duration `json:"shutdown_timeout"`
	LogLevel           string        `json:"log_level"`
	LogFormat          string        `json:"log_format"`

	// P9 trusted hint transport. HintAddr empty disables it; HintToken is an
	// env-only secret (never serialized, never exposed as a CLI flag).
	HintAddr  string `json:"hint_addr,omitempty"`
	HintToken string `json:"-"`

	// P10 hybrid incremental runtime. Disabled by default; when enabled the wake
	// interval must be within [1s, 60s]. It is a scheduler check interval, not
	// provider polling cadence.
	IncrementalRuntimeEnabled bool          `json:"incremental_runtime_enabled"`
	IncrementalWakeInterval   time.Duration `json:"incremental_wake_interval"`
}

// Defaults returns the default configuration (loopback HTTP, safe limits).
func Defaults() Config {
	return Config{
		HTTPAddr:           "127.0.0.1:8080",
		RclonePath:         "rclone",
		MaxConcurrentRoots: 4,
		ScanTimeout:        30 * time.Minute,
		ShutdownTimeout:    15 * time.Second,
		LogLevel:           "info",
		LogFormat:          "text",

		IncrementalWakeInterval: 5 * time.Second,
	}
}

// Load reads configuration from the environment on top of Defaults.
func Load() (Config, error) {
	c := Defaults()
	c.DatabaseURL = os.Getenv("INDEXCORE_DATABASE_URL")
	if v := os.Getenv("INDEXCORE_HTTP_ADDR"); v != "" {
		c.HTTPAddr = v
	}
	if v := os.Getenv("INDEXCORE_RCLONE_PATH"); v != "" {
		c.RclonePath = v
	}
	c.RcloneConfig = os.Getenv("INDEXCORE_RCLONE_CONFIG")
	if v := os.Getenv("INDEXCORE_MAX_CONCURRENT_ROOTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("INDEXCORE_MAX_CONCURRENT_ROOTS: %w", err)
		}
		c.MaxConcurrentRoots = n
	}
	if v := os.Getenv("INDEXCORE_SCAN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("INDEXCORE_SCAN_TIMEOUT: %w", err)
		}
		c.ScanTimeout = d
	}
	if v := os.Getenv("INDEXCORE_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("INDEXCORE_SHUTDOWN_TIMEOUT: %w", err)
		}
		c.ShutdownTimeout = d
	}
	if v := os.Getenv("INDEXCORE_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("INDEXCORE_LOG_FORMAT"); v != "" {
		c.LogFormat = v
	}
	c.HintAddr = os.Getenv("INDEXCORE_HINT_ADDR")
	c.HintToken = os.Getenv("INDEXCORE_HINT_TOKEN")
	if v := os.Getenv("INDEXCORE_INCREMENTAL_RUNTIME_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("INDEXCORE_INCREMENTAL_RUNTIME_ENABLED: %w", err)
		}
		c.IncrementalRuntimeEnabled = b
	}
	if v := os.Getenv("INDEXCORE_INCREMENTAL_WAKE_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("INDEXCORE_INCREMENTAL_WAKE_INTERVAL: %w", err)
		}
		c.IncrementalWakeInterval = d
	}
	return c, nil
}

// RegisterFlags binds the configuration onto a flag set. Flags override env.
func (c *Config) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.DatabaseURL, "database-url", c.DatabaseURL, "PostgreSQL DSN (or INDEXCORE_DATABASE_URL)")
	fs.StringVar(&c.HTTPAddr, "http-addr", c.HTTPAddr, "HTTP listen address (loopback by default)")
	fs.StringVar(&c.RclonePath, "rclone-path", c.RclonePath, "path to the rclone executable")
	fs.StringVar(&c.RcloneConfig, "rclone-config", c.RcloneConfig, "optional rclone config file path")
	fs.IntVar(&c.MaxConcurrentRoots, "max-concurrent-roots", c.MaxConcurrentRoots, "bounded per-root worker concurrency")
	fs.DurationVar(&c.ScanTimeout, "scan-timeout", c.ScanTimeout, "per-scan timeout")
	fs.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", c.ShutdownTimeout, "graceful shutdown timeout")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "log level: debug|info|warn|error")
	fs.StringVar(&c.LogFormat, "log-format", c.LogFormat, "log format: text|json")
	// P9: the hint token is deliberately env-only (no --hint-token flag).
	fs.StringVar(&c.HintAddr, "hint-addr", c.HintAddr, "loopback address for the trusted hint transport (disabled when empty)")
	fs.BoolVar(&c.IncrementalRuntimeEnabled, "incremental-runtime", c.IncrementalRuntimeEnabled, "enable the in-process P10 hybrid incremental runtime (default disabled)")
	fs.DurationVar(&c.IncrementalWakeInterval, "incremental-wake-interval", c.IncrementalWakeInterval, "P10 scheduler wake interval (1s..60s)")
}

// LoopbackOnly reports whether the HTTP address binds only to loopback.
func (c Config) LoopbackOnly() bool {
	host := c.HTTPAddr
	if i := strings.LastIndex(c.HTTPAddr, ":"); i >= 0 {
		host = c.HTTPAddr[:i]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}

// Validate rejects an invalid configuration before any background work starts.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return errors.New("database URL is required (INDEXCORE_DATABASE_URL or --database-url)")
	}
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return errors.New("http listen address is required")
	}
	if c.MaxConcurrentRoots < 1 {
		return fmt.Errorf("max-concurrent-roots must be >= 1, got %d", c.MaxConcurrentRoots)
	}
	if c.ScanTimeout <= 0 {
		return fmt.Errorf("scan-timeout must be > 0, got %s", c.ScanTimeout)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown-timeout must be > 0, got %s", c.ShutdownTimeout)
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		return fmt.Errorf("log-format must be text|json, got %q", c.LogFormat)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log-level must be debug|info|warn|error, got %q", c.LogLevel)
	}
	if c.HintEnabled() {
		if err := validateHintAddr(c.HintAddr); err != nil {
			return err
		}
		if len(c.HintToken) < minHintTokenBytes {
			return fmt.Errorf("INDEXCORE_HINT_TOKEN must be at least %d bytes when the hint transport is enabled", minHintTokenBytes)
		}
	}
	if c.IncrementalRuntimeEnabled {
		if c.IncrementalWakeInterval < minWakeInterval || c.IncrementalWakeInterval > maxWakeInterval {
			return fmt.Errorf("INDEXCORE_INCREMENTAL_WAKE_INTERVAL must be within [%s, %s], got %s",
				minWakeInterval, maxWakeInterval, c.IncrementalWakeInterval)
		}
	}
	return nil
}

const (
	minHintTokenBytes = 32
	minWakeInterval   = time.Second
	maxWakeInterval   = 60 * time.Second
)

// HintEnabled reports whether the P9 trusted hint transport is enabled. An empty
// HintAddr keeps the transport disabled so existing deployments are unchanged.
func (c Config) HintEnabled() bool {
	return strings.TrimSpace(c.HintAddr) != ""
}

// validateHintAddr requires a literal loopback IP host with an explicit non-zero
// port: 127.0.0.1:<port> or [::1]:<port>. Wildcard, non-loopback, hostname,
// missing/invalid and zero ports are rejected; a non-loopback address is never
// silently rewritten.
func validateHintAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("hint address %q must be <literal-loopback-ip>:<port>: %w", addr, err)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("hint address %q must use a port in 1..65535", addr)
	}
	// The P9 contract allows only the exact literal loopback hosts. This
	// deliberately rejects the wider 127.0.0.0/8 range (e.g. 127.0.0.2),
	// IPv4-mapped forms (::ffff:127.0.0.1), and hostnames.
	switch host {
	case "127.0.0.1", "::1":
	default:
		return fmt.Errorf("hint address %q must use the literal loopback host 127.0.0.1 or ::1", addr)
	}
	return nil
}
