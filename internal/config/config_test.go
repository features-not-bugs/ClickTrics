package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/features-not-bugs/clicktrics/internal/config"
)

func TestLoad_DefaultsWhenNoPath(t *testing.T) {
	t.Setenv("CLICKTRICS_HOST", "") // clear any test runner env
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host == "" {
		t.Fatal("Host should default to os.Hostname()")
	}
	if cfg.ErrorBudget != 10 {
		t.Fatalf("ErrorBudget default = %d, want 10", cfg.ErrorBudget)
	}
	if cfg.Exporter.Type != "stdout" {
		t.Fatalf("Exporter.Type default = %q, want stdout", cfg.Exporter.Type)
	}
	if cfg.Exporter.ClickHouse.FlushInterval != time.Second {
		t.Fatalf("FlushInterval default = %v, want 1s", cfg.Exporter.ClickHouse.FlushInterval)
	}
}

func TestLoad_EnvOverridesHost(t *testing.T) {
	t.Setenv("CLICKTRICS_HOST", "override-host")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != "override-host" {
		t.Fatalf("Host = %q, want override-host", cfg.Host)
	}
}

func TestLoad_YAMLFile(t *testing.T) {
	t.Setenv("CLICKTRICS_HOST", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "clicktrics.yaml")
	body := []byte(`
host: from-file
error_budget: 5
exporter:
  type: clickhouse
  clickhouse:
    dsn: clickhouse://ch:9000/metrics
    flush_interval: 2s
    queue_size: 500
collectors:
  cpu:
    enabled: true
    interval: 1s
  memory:
    enabled: false
    interval: 5s
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Host != "from-file" {
		t.Fatalf("Host = %q, want from-file", cfg.Host)
	}
	if cfg.ErrorBudget != 5 {
		t.Fatalf("ErrorBudget = %d, want 5", cfg.ErrorBudget)
	}
	if cfg.Exporter.Type != "clickhouse" {
		t.Fatalf("Exporter.Type = %q, want clickhouse", cfg.Exporter.Type)
	}
	if cfg.Exporter.ClickHouse.DSN != "clickhouse://ch:9000/metrics" {
		t.Fatalf("DSN = %q", cfg.Exporter.ClickHouse.DSN)
	}
	if cfg.Exporter.ClickHouse.FlushInterval != 2*time.Second {
		t.Fatalf("FlushInterval = %v, want 2s", cfg.Exporter.ClickHouse.FlushInterval)
	}
	if cfg.Exporter.ClickHouse.QueueSize != 500 {
		t.Fatalf("QueueSize = %d, want 500", cfg.Exporter.ClickHouse.QueueSize)
	}

	cpu, ok := cfg.Collectors["cpu"]
	if !ok || !cpu.Enabled || cpu.Interval != time.Second {
		t.Fatalf("cpu collector = %+v, want {enabled:true interval:1s}", cpu)
	}
	mem, ok := cfg.Collectors["memory"]
	if !ok || mem.Enabled || mem.Interval != 5*time.Second {
		t.Fatalf("memory collector = %+v, want {enabled:false interval:5s}", mem)
	}
}

func TestLoad_MissingFileErrors(t *testing.T) {
	_, err := config.Load("/nonexistent/path/clicktrics.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_InvalidYAMLErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("::: not: yaml: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected parse error")
	}
}
