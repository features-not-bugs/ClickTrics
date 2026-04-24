// Package config loads ClickTrics configuration from YAML + env overrides.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration.
type Config struct {
	// Host label attached to all samples. Defaults to os.Hostname().
	// CLICKTRICS_HOST env overrides the config file value.
	Host string `yaml:"host"`
	// ErrorBudget is consecutive failures before a collector self-disables.
	ErrorBudget int `yaml:"error_budget"`
	// Exporter selects the output sink and its settings.
	Exporter ExporterConfig `yaml:"exporter"`
	// Collectors maps collector name → per-collector config. Unknown keys
	// are ignored so forward-compat configs don't break old binaries.
	Collectors map[string]CollectorConfig `yaml:"collectors"`
	// UpdateCheck controls the background check against the GitHub API
	// for newer releases of this binary. Zero-value → enabled, 6h cadence.
	UpdateCheck UpdateCheckConfig `yaml:"update_check"`
}

// UpdateCheckConfig configures the background version check.
type UpdateCheckConfig struct {
	// Enabled toggles the entire feature. Default true.
	Enabled *bool `yaml:"enabled"`
	// Interval is the period between checks. Default 6h.
	Interval time.Duration `yaml:"interval"`
}

// IsEnabled returns the effective Enabled flag (nil → true).
func (u UpdateCheckConfig) IsEnabled() bool {
	if u.Enabled == nil {
		return true
	}
	return *u.Enabled
}

// EffectiveInterval returns the interval with the default applied.
func (u UpdateCheckConfig) EffectiveInterval() time.Duration {
	if u.Interval <= 0 {
		return 6 * time.Hour
	}
	return u.Interval
}

// ExporterConfig selects and configures the exporter.
type ExporterConfig struct {
	// Type: "stdout" or "clickhouse". Default "stdout".
	Type       string           `yaml:"type"`
	ClickHouse ClickHouseConfig `yaml:"clickhouse"`
}

// ClickHouseConfig configures the ClickHouse exporter. The connection —
// including the target database — is fully described by the DSN; there is
// no separate database field.
type ClickHouseConfig struct {
	DSN           string        `yaml:"dsn"`
	FlushInterval time.Duration `yaml:"flush_interval"`
	// SendTimeout caps a single table's prepare + append + send round trip.
	// Independent of FlushInterval. HTTP-mode PrepareBatch issues a
	// DESCRIBE round trip before the INSERT payload, so this needs headroom
	// for the WAN path. Zero → 30s default.
	SendTimeout time.Duration `yaml:"send_timeout"`
	// QueueSize caps in-memory rows per table. Drop-oldest on overflow.
	QueueSize int `yaml:"queue_size"`
}

// CollectorConfig is the per-collector toggle + cadence.
type CollectorConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
}

// Defaults returns a Config populated with sensible defaults.
func Defaults() *Config {
	return &Config{
		ErrorBudget: 10,
		Exporter: ExporterConfig{
			Type: "stdout",
			ClickHouse: ClickHouseConfig{
				FlushInterval: 1 * time.Second,
				SendTimeout:   30 * time.Second,
				QueueSize:     100_000,
			},
		},
		Collectors: map[string]CollectorConfig{},
	}
}

// Load reads a YAML config from path (empty path uses defaults only) and
// applies env overrides, then fills in the host label.
func Load(path string) (*Config, error) {
	cfg := Defaults()

	if path != "" {
		data, err := os.ReadFile(path) // #nosec G304 — operator-provided path
		if err != nil {
			return nil, fmt.Errorf("read config %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %q: %w", path, err)
		}
	}

	if v := os.Getenv("CLICKTRICS_HOST"); v != "" {
		cfg.Host = v
	}
	if cfg.Host == "" {
		h, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("resolve hostname: %w", err)
		}
		cfg.Host = h
	}

	return cfg, nil
}
