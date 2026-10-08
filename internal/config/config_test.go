package config_test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/yyewolf/ssarchiver/internal/config"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SSA_DATA_DIR", "SSA_LISTEN", "SSA_BASE_URL", "SSA_HOURLY_BUDGET", "SSA_LOG_LEVEL", "SSA_TRUST_PROXY"} {
		t.Setenv(k, "")
	}
}

func TestFromEnvDefaults(t *testing.T) {
	clearEnv(t)
	c := config.FromEnv()
	if c.DataDir != "./data" || c.Listen != ":8080" || c.HourlyBudget != 300 || c.LogLevel != "info" || c.TrustProxy || c.BaseURL != "" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("SSA_DATA_DIR", "/srv/ssa")
	t.Setenv("SSA_LISTEN", "127.0.0.1:9000")
	t.Setenv("SSA_BASE_URL", "https://replays.example.com/")
	t.Setenv("SSA_HOURLY_BUDGET", "120")
	t.Setenv("SSA_LOG_LEVEL", "debug")
	t.Setenv("SSA_TRUST_PROXY", "true")
	c := config.FromEnv()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/srv/ssa" || c.Listen != "127.0.0.1:9000" || c.HourlyBudget != 120 || !c.TrustProxy {
		t.Fatalf("overrides not applied: %+v", c)
	}
	if c.BaseURL != "https://replays.example.com" {
		t.Fatalf("base URL not normalised: %q", c.BaseURL)
	}
	if got, want := c.DBPath(), filepath.Join("/srv/ssa", "ssarchiver.db"); got != want {
		t.Fatalf("DBPath = %q, want %q", got, want)
	}
	if got, want := c.ReplayDir(), filepath.Join("/srv/ssa", "replays"); got != want {
		t.Fatalf("ReplayDir = %q, want %q", got, want)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*config.Config){
		"budget zero":       func(c *config.Config) { c.HourlyBudget = 0 },
		"budget above 360":  func(c *config.Config) { c.HourlyBudget = 361 },
		"budget unparsable": func(c *config.Config) { c.HourlyBudget = -1 },
		"relative base url": func(c *config.Config) { c.BaseURL = "replays.example.com" },
		"ftp base url":      func(c *config.Config) { c.BaseURL = "ftp://example.com" },
		"bad log level":     func(c *config.Config) { c.LogLevel = "loud" },
		"empty data dir":    func(c *config.Config) { c.DataDir = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			c := config.FromEnv()
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestInvalidBudgetEnvFailsValidation(t *testing.T) {
	clearEnv(t)
	t.Setenv("SSA_HOURLY_BUDGET", "lots")
	c := config.FromEnv()
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for unparsable SSA_HOURLY_BUDGET")
	}
}

func TestParseLogLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		got, err := config.ParseLogLevel(in)
		if err != nil || got != want {
			t.Fatalf("ParseLogLevel(%q) = %v, %v", in, got, err)
		}
	}
}
