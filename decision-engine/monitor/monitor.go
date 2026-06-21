// SPDX-License-Identifier: AGPL-3.0-or-later

// Package monitor is the Decision Engine's outcome-monitoring layer: thresholds
// over a flow's derived metrics (failure rate, refer/automation rate, latency,
// volume, and distribution drift vs a captured baseline) that report firing/ok
// against the live analytics projection. The evaluator is a pure function of a
// snapshot — the imperative shell reads the snapshot (metrics + baseline) and the
// stored rules and joins them; a scheduler periodically checks and notifies.
package monitor

import (
	"math"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/policy"
)

// Metric identifies the derived quantity a monitor watches. A named type (not a
// bare string) so an invalid metric is caught at the boundary — the convention
// every domain enum follows (see ModelKind/CaseStatus/…); event/wire payloads
// keep string and convert at the Rule boundary.
type Metric string

// Known metrics.
const (
	MetricFailureRate       Metric = "failure_rate"       // failed / (completed+failed)
	MetricReferRate         Metric = "refer_rate"         // refer / dispositioned
	MetricAutomationRate    Metric = "automation_rate"    // (approve+decline) / dispositioned
	MetricApproveRate       Metric = "approve_rate"       // approve / dispositioned
	MetricDeclineRate       Metric = "decline_rate"       // decline / dispositioned
	MetricAvgLatencyMS      Metric = "avg_latency_ms"     // mean completed-decision duration
	MetricVolume            Metric = "volume"             // total decisions started
	MetricDistributionDrift Metric = "distribution_drift" // max |current-baseline| disposition share
	// Richer drift over the same disposition distribution. PSI (population stability
	// index) sums (c-b)·ln(c/b) per bucket — the lending-industry standard, with the
	// rule of thumb <0.1 stable / 0.1–0.25 moderate / >0.25 significant. KL is the
	// Kullback–Leibler divergence sum c·ln(c/b). Both weight proportional shifts in
	// small buckets more than the raw max-delta does, catching subtler drift.
	MetricDistributionDriftPSI Metric = "distribution_drift_psi"
	MetricDistributionDriftKL  Metric = "distribution_drift_kl"
)

// Valid reports whether m is a known metric.
func (m Metric) Valid() bool {
	switch m {
	case MetricFailureRate, MetricReferRate, MetricAutomationRate, MetricApproveRate,
		MetricDeclineRate, MetricAvgLatencyMS, MetricVolume, MetricDistributionDrift,
		MetricDistributionDriftPSI, MetricDistributionDriftKL:
		return true
	}
	return false
}

// Op is the comparison that puts a monitor into the firing state.
type Op string

// Known comparisons.
const (
	OpGreaterThan Op = "gt" // fire when actual > threshold (e.g. failure_rate gt 0.05)
	OpLessThan    Op = "lt" // fire when actual < threshold (e.g. automation_rate lt 0.5)
)

// Valid reports whether o is a known comparison.
func (o Op) Valid() bool { return o == OpGreaterThan || o == OpLessThan }

// Rule is a threshold over a derived metric.
type Rule struct {
	Metric    Metric  `json:"metric"`
	Op        Op      `json:"op"`
	Threshold float64 `json:"threshold"`
}

// Status is a rule's evaluation against a snapshot. A metric with no data yet (a
// rate with no decisions, or drift with no baseline) is not computable, never fires.
type Status struct {
	Actual     float64 `json:"actual"`
	Computable bool    `json:"computable"`
	Firing     bool    `json:"firing"`
}

// Baseline is a captured disposition distribution (shares summing to ~1), the
// reference that distribution_drift measures against.
type Baseline struct {
	Approve float64 `json:"approve"`
	Decline float64 `json:"decline"`
	Refer   float64 `json:"refer"`
	Total   int     `json:"total"`
}

// Snapshot is the evaluator's input: the live metrics plus an optional baseline.
type Snapshot struct {
	Metrics  analytics.FlowMetrics
	Baseline *Baseline // nil when none captured for the flow
}

// ValidMetric reports whether a raw string names a known metric — the boundary
// helper for request strings before they are typed onto a Rule.
func ValidMetric(m string) bool { return Metric(m).Valid() }

// ValidOp reports whether a raw string names a known comparison.
func ValidOp(o string) bool { return Op(o).Valid() }

// Evaluate computes the rule's metric from the snapshot and reports whether it
// breaches the threshold.
func Evaluate(snap Snapshot, r Rule) Status {
	actual, ok := metricValue(snap, r.Metric)
	if !ok {
		return Status{Computable: false}
	}
	return Status{Actual: actual, Computable: true, Firing: breached(actual, r.Op, r.Threshold)}
}

func breached(actual float64, op Op, threshold float64) bool {
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
func metricValue(snap Snapshot, metric Metric) (float64, bool) {
	m := snap.Metrics
	dispositioned := m.ByDisposition[string(policy.Approve)] + m.ByDisposition[string(policy.Decline)] + m.ByDisposition[string(policy.Refer)]
	resolved := m.Completed + m.Failed
	switch metric {
	case MetricFailureRate:
		return ratio(m.Failed, resolved)
	case MetricReferRate:
		return ratio(m.ByDisposition[string(policy.Refer)], dispositioned)
	case MetricAutomationRate:
		return ratio(m.ByDisposition[string(policy.Approve)]+m.ByDisposition[string(policy.Decline)], dispositioned)
	case MetricApproveRate:
		return ratio(m.ByDisposition[string(policy.Approve)], dispositioned)
	case MetricDeclineRate:
		return ratio(m.ByDisposition[string(policy.Decline)], dispositioned)
	case MetricAvgLatencyMS:
		if m.Completed == 0 {
			return 0, false
		}
		return float64(m.AvgDurationMS), true
	case MetricVolume:
		return float64(m.Total), true
	case MetricDistributionDrift:
		return driftValue(snap)
	case MetricDistributionDriftPSI:
		return divergenceValue(snap, psiTerm)
	case MetricDistributionDriftKL:
		return divergenceValue(snap, klTerm)
	}
	return 0, false
}

func ratio(num, denom int) (float64, bool) {
	if denom == 0 {
		return 0, false
	}
	return float64(num) / float64(denom), true
}

// distribution returns the current disposition shares (approve, decline, refer),
// computable only once at least one dispositioned decision exists.
func distribution(m analytics.FlowMetrics) (approve, decline, refer float64, ok bool) {
	d := m.ByDisposition
	total := d["approve"] + d["decline"] + d["refer"]
	if total == 0 {
		return 0, 0, 0, false
	}
	t := float64(total)
	return float64(d["approve"]) / t, float64(d["decline"]) / t, float64(d["refer"]) / t, true
}

// driftValue is the largest absolute shift of any disposition share from the
// baseline — undefined without a baseline or without current dispositioned data.
func driftValue(snap Snapshot) (float64, bool) {
	if snap.Baseline == nil {
		return 0, false
	}
	a, dc, r, ok := distribution(snap.Metrics)
	if !ok {
		return 0, false
	}
	drift := math.Abs(a - snap.Baseline.Approve)
	drift = math.Max(drift, math.Abs(dc-snap.Baseline.Decline))
	drift = math.Max(drift, math.Abs(r-snap.Baseline.Refer))
	return drift, true
}

// driftEps floors a share so a zeroed bucket doesn't blow up the log term.
const driftEps = 1e-4

// psiTerm / klTerm are the per-bucket contributions to PSI and KL respectively.
func psiTerm(cur, base float64) float64 { return (cur - base) * math.Log(cur/base) }
func klTerm(cur, base float64) float64  { return cur * math.Log(cur/base) }

// divergenceValue sums a per-bucket divergence term over the disposition shares
// (current vs baseline). Undefined without a baseline or current data; shares are
// floored by driftEps so an empty bucket contributes a bounded term.
func divergenceValue(snap Snapshot, term func(cur, base float64) float64) (float64, bool) {
	if snap.Baseline == nil {
		return 0, false
	}
	a, dc, r, ok := distribution(snap.Metrics)
	if !ok {
		return 0, false
	}
	cur := []float64{a, dc, r}
	base := []float64{snap.Baseline.Approve, snap.Baseline.Decline, snap.Baseline.Refer}
	sum := 0.0
	for i := range cur {
		sum += term(math.Max(cur[i], driftEps), math.Max(base[i], driftEps))
	}
	return sum, true
}

// DistributionOf captures the current disposition shares as a Baseline (the shell
// records this as a baseline snapshot). ok is false when nothing is dispositioned.
func DistributionOf(m analytics.FlowMetrics) (Baseline, bool) {
	a, dc, r, ok := distribution(m)
	if !ok {
		return Baseline{}, false
	}
	total := m.ByDisposition[string(policy.Approve)] + m.ByDisposition[string(policy.Decline)] + m.ByDisposition[string(policy.Refer)]
	return Baseline{Approve: a, Decline: dc, Refer: r, Total: total}, true
}

// DriftBucket is one disposition's baseline vs current share.
type DriftBucket struct {
	Disposition string  `json:"disposition"`
	Baseline    float64 `json:"baseline"`
	Current     float64 `json:"current"`
	Delta       float64 `json:"delta"`
}

// DriftReport compares the current distribution against the baseline per bucket.
type DriftReport struct {
	HasBaseline   bool          `json:"has_baseline"`
	HasCurrent    bool          `json:"has_current"`
	MaxDrift      float64       `json:"max_drift"`
	PSI           float64       `json:"psi"` // population stability index over the buckets
	KL            float64       `json:"kl"`  // Kullback–Leibler divergence over the buckets
	BaselineTotal int           `json:"baseline_total,omitempty"`
	CurrentTotal  int           `json:"current_total"`
	Buckets       []DriftBucket `json:"buckets,omitempty"`
}

// ComputeDrift builds a per-bucket drift report from a snapshot.
func ComputeDrift(snap Snapshot) DriftReport {
	rep := DriftReport{HasBaseline: snap.Baseline != nil}
	a, dc, r, ok := distribution(snap.Metrics)
	rep.HasCurrent = ok
	rep.CurrentTotal = snap.Metrics.ByDisposition[string(policy.Approve)] + snap.Metrics.ByDisposition[string(policy.Decline)] + snap.Metrics.ByDisposition[string(policy.Refer)]
	if !rep.HasBaseline {
		return rep
	}
	rep.BaselineTotal = snap.Baseline.Total
	cur := map[string]float64{"approve": a, "decline": dc, "refer": r}
	base := map[string]float64{"approve": snap.Baseline.Approve, "decline": snap.Baseline.Decline, "refer": snap.Baseline.Refer}
	for _, d := range []string{"approve", "decline", "refer"} {
		delta := cur[d] - base[d]
		rep.Buckets = append(rep.Buckets, DriftBucket{Disposition: d, Baseline: base[d], Current: cur[d], Delta: delta})
		if ok && math.Abs(delta) > rep.MaxDrift {
			rep.MaxDrift = math.Abs(delta)
		}
	}
	if psi, ok := divergenceValue(snap, psiTerm); ok {
		rep.PSI = psi
	}
	if kl, ok := divergenceValue(snap, klTerm); ok {
		rep.KL = kl
	}
	return rep
}
