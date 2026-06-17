// SPDX-License-Identifier: AGPL-3.0-or-later

// Package monitor is the Decision Engine's outcome-monitoring layer: thresholds
// over a flow's derived metrics (failure rate, refer/automation rate, latency,
// volume) that report firing/ok against the live analytics projection. The
// evaluator is a pure function of a metrics snapshot — the imperative shell reads
// the snapshot and the stored rules and joins them.
package monitor

import "github.com/e6qu/intraktible/decision-engine/analytics"

// Metric identifies the derived quantity a monitor watches.
const (
	MetricFailureRate    = "failure_rate"    // failed / (completed+failed)
	MetricReferRate      = "refer_rate"      // refer / dispositioned
	MetricAutomationRate = "automation_rate" // (approve+decline) / dispositioned
	MetricApproveRate    = "approve_rate"    // approve / dispositioned
	MetricDeclineRate    = "decline_rate"    // decline / dispositioned
	MetricAvgLatencyMS   = "avg_latency_ms"  // mean completed-decision duration
	MetricVolume         = "volume"          // total decisions started
)

// Op is the comparison that puts a monitor into the firing state.
const (
	OpGreaterThan = "gt" // fire when actual > threshold (e.g. failure_rate gt 0.05)
	OpLessThan    = "lt" // fire when actual < threshold (e.g. automation_rate lt 0.5)
)

// Rule is a threshold over a derived metric.
type Rule struct {
	Metric    string  `json:"metric"`
	Op        string  `json:"op"`
	Threshold float64 `json:"threshold"`
}

// Status is a rule's evaluation against a metrics snapshot. A metric with no data
// yet (a rate with no decisions) is not computable and never fires.
type Status struct {
	Actual     float64 `json:"actual"`
	Computable bool    `json:"computable"`
	Firing     bool    `json:"firing"`
}

// ValidMetric reports whether m is a known metric.
func ValidMetric(m string) bool {
	switch m {
	case MetricFailureRate, MetricReferRate, MetricAutomationRate,
		MetricApproveRate, MetricDeclineRate, MetricAvgLatencyMS, MetricVolume:
		return true
	}
	return false
}

// ValidOp reports whether o is a known comparison.
func ValidOp(o string) bool { return o == OpGreaterThan || o == OpLessThan }

// Evaluate computes the rule's metric from the snapshot and reports whether it
// breaches the threshold.
func Evaluate(m analytics.FlowMetrics, r Rule) Status {
	actual, ok := metricValue(m, r.Metric)
	if !ok {
		return Status{Computable: false}
	}
	return Status{Actual: actual, Computable: true, Firing: breached(actual, r.Op, r.Threshold)}
}

func breached(actual float64, op string, threshold float64) bool {
	switch op {
	case OpGreaterThan:
		return actual > threshold
	case OpLessThan:
		return actual < threshold
	}
	return false
}

// metricValue derives one metric from the snapshot, returning false when it has
// no denominator yet (so the caller can show "no data" rather than a false 0).
func metricValue(m analytics.FlowMetrics, metric string) (float64, bool) {
	// Disposition values mirror the policy package's approve|decline|refer; the
	// analytics projection keys ByDisposition by those raw strings.
	dispositioned := m.ByDisposition["approve"] + m.ByDisposition["decline"] + m.ByDisposition["refer"]
	resolved := m.Completed + m.Failed
	switch metric {
	case MetricFailureRate:
		return ratio(m.Failed, resolved)
	case MetricReferRate:
		return ratio(m.ByDisposition["refer"], dispositioned)
	case MetricAutomationRate:
		return ratio(m.ByDisposition["approve"]+m.ByDisposition["decline"], dispositioned)
	case MetricApproveRate:
		return ratio(m.ByDisposition["approve"], dispositioned)
	case MetricDeclineRate:
		return ratio(m.ByDisposition["decline"], dispositioned)
	case MetricAvgLatencyMS:
		if m.Completed == 0 {
			return 0, false
		}
		return float64(m.AvgDurationMS), true
	case MetricVolume:
		return float64(m.Total), true
	}
	return 0, false
}

func ratio(num, denom int) (float64, bool) {
	if denom == 0 {
		return 0, false
	}
	return float64(num) / float64(denom), true
}
