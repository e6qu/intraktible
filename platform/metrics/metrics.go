// SPDX-License-Identifier: AGPL-3.0-or-later

// Package metrics is the Prometheus instrumentation surface. It owns a private
// registry (so unrelated default collectors don't leak in), the metric
// definitions, and thin Record* helpers the imperative shell calls. Metrics are
// an effect: only the shell (HTTP middleware, projection runtime, schedulers)
// records them — the deterministic core never imports this package.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	reg = prometheus.NewRegistry()

	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "intraktible_http_requests_total",
		Help: "HTTP requests handled, by matched route, method, and status class.",
	}, []string{"route", "method", "status"})

	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "intraktible_http_request_duration_seconds",
		Help:    "HTTP request latency, by matched route and method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "method"})

	projectionApplied = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "intraktible_projection_applied_seq",
		Help: "Highest event sequence applied by the projection runtime (read-model freshness).",
	})

	projectionErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "intraktible_projection_errors_total",
		Help: "Projection live-apply failures (a stalled consumer; /healthz reports degraded).",
	})

	schedulerTicks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "intraktible_scheduler_ticks_total",
		Help: "Scheduler sweeps, by scheduler and outcome (ok|error).",
	}, []string{"scheduler", "outcome"})
)

func init() {
	reg.MustRegister(
		httpRequests, httpDuration, projectionApplied, projectionErrors, schedulerTicks,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// Handler serves the registry in the Prometheus text exposition format. Mount it
// unauthenticated alongside /healthz — it exposes only aggregate operational
// counters, never tenant data.
func Handler() http.Handler { return promhttp.HandlerFor(reg, promhttp.HandlerOpts{}) }

// statusClass buckets a status code into 2xx/4xx/5xx so the label set stays small.
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// RecordHTTP records one request. route is the matched ServeMux pattern (low
// cardinality); an unmatched request is bucketed under "unmatched" so a flood of
// distinct 404 paths can't explode the series.
func RecordHTTP(route, method string, status int, dur time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	httpRequests.WithLabelValues(route, method, statusClass(status)).Inc()
	httpDuration.WithLabelValues(route, method).Observe(dur.Seconds())
}

// SetProjectionApplied publishes the highest applied event sequence.
func SetProjectionApplied(seq uint64) { projectionApplied.Set(float64(seq)) }

// IncProjectionErrors counts a live-apply failure.
func IncProjectionErrors() { projectionErrors.Inc() }

// RecordSchedulerTick counts one scheduler sweep (outcome "ok" or "error").
func RecordSchedulerTick(scheduler, outcome string) {
	schedulerTicks.WithLabelValues(scheduler, outcome).Inc()
}
