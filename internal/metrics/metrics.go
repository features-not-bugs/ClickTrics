// Package metrics exposes clicktrics' own Prometheus counters so the scraper
// is observable even when the ClickHouse sink is down.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	CollectorRuns = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clicktrics_collector_runs_total",
		Help: "Total successful Collect calls per collector.",
	}, []string{"collector"})

	CollectorErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clicktrics_collector_errors_total",
		Help: "Total failed Collect calls per collector.",
	}, []string{"collector"})

	CollectorDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "clicktrics_collector_duration_seconds",
		Help:    "Collect call duration per collector.",
		Buckets: prometheus.ExponentialBuckets(0.0005, 2, 12),
	}, []string{"collector"})

	CollectorRowsEmitted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clicktrics_collector_rows_emitted_total",
		Help: "Rows produced by a collector (before exporter).",
	}, []string{"collector", "table"})

	ExporterBatches = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clicktrics_exporter_batches_total",
		Help: "Batches shipped per table.",
	}, []string{"table", "result"})

	ExporterRows = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clicktrics_exporter_rows_total",
		Help: "Rows shipped per table.",
	}, []string{"table"})

	ExporterRowsDropped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clicktrics_exporter_rows_dropped_total",
		Help: "Rows dropped per table due to full buffer.",
	}, []string{"table"})

	ExporterFlushDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "clicktrics_exporter_flush_duration_seconds",
		Help:    "Time to flush one batch to ClickHouse, per table.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
	}, []string{"table"})

	Up = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "clicktrics_up",
		Help: "1 if the runner is live and the exporter is connected.",
	})

	// BuildInfo is an always-1 gauge carrying build-time identifiers as
	// labels. Mirrors the prometheus_build_info pattern.
	BuildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "clicktrics_build_info",
		Help: "Build identifiers of the running binary; value is always 1.",
	}, []string{"version", "commit", "build_date", "go_version"})

	// UpstreamInfo carries the latest observed upstream release tag. Value
	// is the publish-time unix seconds if known, else 1.
	UpstreamInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "clicktrics_upstream_release_info",
		Help: "Latest upstream release tag as seen from the GitHub API.",
	}, []string{"version", "url"})

	// UpdateAvailable is 1 when the running binary is older than the
	// latest upstream release, 0 otherwise. Emitted only once we've
	// successfully queried GitHub.
	UpdateAvailable = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "clicktrics_update_available",
		Help: "1 if upstream has a newer tagged release than the running binary, 0 otherwise.",
	})

	// UpstreamCheckSuccess tracks whether the last upstream check
	// succeeded — useful for alerting on persistent failures (GitHub
	// rate limit, DNS, air-gapped host).
	UpstreamCheckSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "clicktrics_upstream_check_success",
		Help: "1 if the last upstream version check succeeded, 0 otherwise.",
	})
)
