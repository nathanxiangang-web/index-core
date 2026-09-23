// Package config defines the Gate 3 Alpha runtime configuration. Configuration
// is env + flags only (no config files, no framework). Invalid configuration
// must fail before any background work starts.
package config

import (
	"errors"
	"flag"
	"fmt"
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
	return nil
}
