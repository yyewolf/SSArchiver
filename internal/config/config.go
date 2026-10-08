// Package config loads SSArchiver's runtime configuration from the
// environment and command-line flags.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
)

// Config is the process configuration. Zero values are not meaningful; use FromEnv.
type Config struct {
	DataDir      string
	Listen       string
	BaseURL      string
	HourlyBudget int
	LogLevel     string
	TrustProxy   bool
}

// FromEnv builds a Config from SSA_* environment variables with defaults.
func FromEnv() Config {
	return Config{
		DataDir:      envOr("SSA_DATA_DIR", "./data"),
		Listen:       envOr("SSA_LISTEN", ":8080"),
		BaseURL:      os.Getenv("SSA_BASE_URL"),
		HourlyBudget: envInt("SSA_HOURLY_BUDGET", 300),
		LogLevel:     envOr("SSA_LOG_LEVEL", "info"),
		TrustProxy:   os.Getenv("SSA_TRUST_PROXY") == "true" || os.Getenv("SSA_TRUST_PROXY") == "1",
	}
}

// BindFlags registers flags whose defaults are the current (env-derived) values.
func (c *Config) BindFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.DataDir, "data-dir", c.DataDir, "data directory for the database and replays (SSA_DATA_DIR)")
	fs.StringVar(&c.Listen, "listen", c.Listen, "HTTP listen address (SSA_LISTEN)")
	fs.StringVar(&c.BaseURL, "base-url", c.BaseURL, "public base URL, e.g. https://replays.example.com (SSA_BASE_URL)")
	fs.IntVar(&c.HourlyBudget, "hourly-budget", c.HourlyBudget, "max ScoreSaber requests per hour, 1-360 (SSA_HOURLY_BUDGET)")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "debug, info, warn or error (SSA_LOG_LEVEL)")
	fs.BoolVar(&c.TrustProxy, "trust-proxy", c.TrustProxy, "trust X-Forwarded-* headers (SSA_TRUST_PROXY)")
}

// Validate checks values and normalises BaseURL (no trailing slash).
func (c *Config) Validate() error {
	if c.DataDir == "" {
		return errors.New("config: data dir must not be empty")
	}
	if c.HourlyBudget < 1 || c.HourlyBudget > 360 {
		return fmt.Errorf("config: hourly budget must be between 1 and 360, got %d", c.HourlyBudget)
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("config: base url must be an absolute http(s) URL, got %q", c.BaseURL)
		}
		c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	}
	if _, err := ParseLogLevel(c.LogLevel); err != nil {
		return err
	}
	return nil
}

// DBPath is the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "ssarchiver.db") }

// ReplayDir is the root directory of archived replay files.
func (c Config) ReplayDir() string { return filepath.Join(c.DataDir, "replays") }

// ParseLogLevel maps a case-insensitive level name to slog.Level.
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("config: unknown log level %q", s)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt returns def when unset and -1 when unparsable (Validate rejects -1).
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return n
}
