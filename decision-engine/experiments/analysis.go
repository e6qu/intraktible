// SPDX-License-Identifier: AGPL-3.0-or-later

package experiments

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/e6qu/intraktible/decision-engine/outcomes"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// AnalysisStatus is deliberately conservative: only StatusWinner authorizes a
// winner call; invalid, imbalanced, underpowered, and inconclusive cohorts are
// distinct operator states.
type AnalysisStatus string

const (
	StatusCollecting      AnalysisStatus = "collecting"
	StatusInvalid         AnalysisStatus = "invalid"
	StatusUnderpowered    AnalysisStatus = "underpowered"
	StatusInconclusive    AnalysisStatus = "inconclusive"
	StatusGuardrailFailed AnalysisStatus = "guardrail_failed"
	StatusWinner          AnalysisStatus = "winner"
)

// Interval is a confidence interval.
type Interval struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

// ArmMetric summarizes one metric within one arm.
type ArmMetric struct {
	ArmKey   string   `json:"arm_key"`
	Count    int      `json:"count"`
	Mean     float64  `json:"mean"`
	StdDev   float64  `json:"std_dev"`
	Interval Interval `json:"interval"`
}

// Comparison is one treatment's effect relative to the champion.
type Comparison struct {
	ArmKey     string   `json:"arm_key"`
	Effect     float64  `json:"effect"`
	EffectSize float64  `json:"effect_size"`
	Interval   Interval `json:"interval"`
	Favorable  bool     `json:"favorable"`
}

// MetricAnalysis contains exact-cohort observations for one metric.
type MetricAnalysis struct {
	Metric       Metric       `json:"metric"`
	Arms         []ArmMetric  `json:"arms"`
	Comparisons  []Comparison `json:"comparisons"`
	Excluded     int          `json:"excluded"`
	LabelVersion string       `json:"label_version,omitempty"`
}

// Analysis is the reproducible experiment report.
type Analysis struct {
	ExperimentID   string           `json:"experiment_id"`
	Cohort         int              `json:"cohort"`
	State          State            `json:"state"`
	Status         AnalysisStatus   `json:"status"`
	Reason         string           `json:"reason"`
	WinnerArmKey   string           `json:"winner_arm_key,omitempty"`
	ExposureCounts map[string]int   `json:"exposure_counts"`
	SRMPValue      float64          `json:"srm_p_value"`
	Primary        MetricAnalysis   `json:"primary"`
	Guardrails     []MetricAnalysis `json:"guardrails"`
	Assumptions    []string         `json:"assumptions"`
}

// Analyze computes the current exact-cohort report from reached exposures and
// current corrected outcomes.
func Analyze(ctx context.Context, st store.Store, id identity.Identity, experimentID string) (Analysis, error) {
	view, ok, err := Read(ctx, st, id, experimentID)
	if err != nil {
		return Analysis{}, err
	}
	if !ok {
		return Analysis{}, fmt.Errorf("experiments: unknown experiment %q", experimentID)
	}
	exposures, err := ListExposures(ctx, st, id, experimentID, view.Cohort)
	if err != nil {
		return Analysis{}, err
	}
	allOutcomes, err := outcomes.List(ctx, st, id, "", "")
	if err != nil {
		return Analysis{}, err
	}
	report := Analysis{
		ExperimentID: experimentID, Cohort: view.Cohort, State: view.State,
		Status: StatusCollecting, Reason: "waiting for reached-treatment exposures and outcomes",
		ExposureCounts: make(map[string]int, len(view.Spec.Arms)),
		Primary:        emptyMetricAnalysis(view.Spec, view.Spec.PrimaryMetric),
		Guardrails:     make([]MetricAnalysis, 0, len(view.Spec.Guardrails)),
		Assumptions: []string{
			"assignment is deterministic by subject key, salt, and cohort",
			"only reached-treatment exposures enter the denominator",
			"confidence intervals use normal/Wilson large-sample approximations",
			"one current corrected outcome per decision and metric is required",
		},
	}
	for _, guardrail := range view.Spec.Guardrails {
		report.Guardrails = append(report.Guardrails, emptyMetricAnalysis(view.Spec, guardrail))
	}
	armByKey := make(map[string]Arm, len(view.Spec.Arms))
	for _, arm := range view.Spec.Arms {
		armByKey[arm.Key] = arm
		report.ExposureCounts[arm.Key] = 0
	}
	exposureByDecision := make(map[string]Exposure, len(exposures))
	for _, exposure := range exposures {
		arm, known := armByKey[exposure.ArmKey]
		if !known || arm.Version != exposure.Version {
			report.Status, report.Reason = StatusInvalid, "an exposure does not match the cohort's exact arm version"
			return report, nil
		}
		report.ExposureCounts[exposure.ArmKey]++
		exposureByDecision[exposure.DecisionID] = exposure
	}
	if len(exposures) == 0 {
		return report, nil
	}
	report.SRMPValue = sampleRatioPValue(view.Spec.Arms, report.ExposureCounts, len(exposures))
	if report.SRMPValue < 0.01 {
		report.Status, report.Reason = StatusInvalid, "sample-ratio mismatch (p < 0.01)"
		return report, nil
	}
	byMetric := make(map[string][]outcomes.View)
	for _, outcome := range allOutcomes {
		if _, exposed := exposureByDecision[outcome.DecisionID]; exposed {
			byMetric[outcome.Key] = append(byMetric[outcome.Key], outcome)
		}
	}
	primary, invalidReason := analyzeMetric(view.Spec, view.Spec.PrimaryMetric, exposures, byMetric[view.Spec.PrimaryMetric.Key])
	report.Primary = primary
	if invalidReason != "" {
		report.Status, report.Reason = StatusInvalid, invalidReason
		return report, nil
	}
	report.Guardrails = report.Guardrails[:0]
	for _, metric := range view.Spec.Guardrails {
		analysis, invalid := analyzeMetric(view.Spec, metric, exposures, byMetric[metric.Key])
		report.Guardrails = append(report.Guardrails, analysis)
		if invalid != "" {
			report.Status, report.Reason = StatusInvalid, invalid
			return report, nil
		}
	}
	analyses := append([]MetricAnalysis{primary}, report.Guardrails...)
	for _, metric := range analyses {
		for _, arm := range metric.Arms {
			if arm.Count < view.Spec.MinimumSamplePerArm {
				report.Status = StatusUnderpowered
				report.Reason = fmt.Sprintf(
					"metric %q arm %q has %d observed outcomes; minimum is %d",
					metric.Metric.Key, arm.ArmKey, arm.Count, view.Spec.MinimumSamplePerArm,
				)
				return report, nil
			}
		}
	}
	for _, guardrail := range report.Guardrails {
		for _, comparison := range guardrail.Comparisons {
			regression := -normalizedEffect(guardrail.Metric.Direction, comparison.Effect)
			if regression > guardrail.Metric.MaxRegression {
				report.Status, report.Reason = StatusGuardrailFailed,
					fmt.Sprintf("guardrail %q regressed by %.6g", guardrail.Metric.Key, regression)
				return report, nil
			}
		}
	}
	var winners []Comparison
	for _, comparison := range primary.Comparisons {
		effect := normalizedEffect(primary.Metric.Direction, comparison.Effect)
		ciLow := math.Min(
			normalizedEffect(primary.Metric.Direction, comparison.Interval.Low),
			normalizedEffect(primary.Metric.Direction, comparison.Interval.High),
		)
		if effect >= view.Spec.MinimumEffect && ciLow > 0 {
			winners = append(winners, comparison)
		}
	}
	if len(winners) == 0 {
		report.Status, report.Reason = StatusInconclusive, "primary KPI does not show a powered, confidence-bounded minimum effect"
		return report, nil
	}
	sort.Slice(winners, func(i, j int) bool {
		return normalizedEffect(primary.Metric.Direction, winners[i].Effect) >
			normalizedEffect(primary.Metric.Direction, winners[j].Effect)
	})
	report.Status, report.Reason, report.WinnerArmKey =
		StatusWinner, "primary KPI clears power, effect, confidence, SRM, and guardrail gates", winners[0].ArmKey
	return report, nil
}

func emptyMetricAnalysis(spec Spec, metric Metric) MetricAnalysis {
	analysis := MetricAnalysis{
		Metric: metric, Arms: make([]ArmMetric, 0, len(spec.Arms)),
		Comparisons: []Comparison{},
	}
	for _, arm := range spec.Arms {
		analysis.Arms = append(analysis.Arms, ArmMetric{ArmKey: arm.Key})
	}
	return analysis
}

func analyzeMetric(spec Spec, metric Metric, exposures []Exposure, views []outcomes.View) (MetricAnalysis, string) {
	result := MetricAnalysis{Metric: metric, Arms: []ArmMetric{}, Comparisons: []Comparison{}}
	exposureByDecision := make(map[string]Exposure, len(exposures))
	for _, exposure := range exposures {
		exposureByDecision[exposure.DecisionID] = exposure
	}
	seen := make(map[string]bool)
	values := make(map[string][]float64)
	labelVersion := ""
	for _, outcome := range views {
		exposure := exposureByDecision[outcome.DecisionID]
		if seen[outcome.DecisionID] {
			return result, fmt.Sprintf("metric %q has multiple current outcomes for decision %q", metric.Key, outcome.DecisionID)
		}
		seen[outcome.DecisionID] = true
		if outcome.Kind != outcomes.Kind(metric.Kind) {
			return result, fmt.Sprintf("metric %q kind is %s in the experiment but %s in an outcome", metric.Key, metric.Kind, outcome.Kind)
		}
		if outcome.Treatment == nil ||
			outcome.Treatment.ExperimentID != exposure.ExperimentID ||
			outcome.Treatment.Cohort != exposure.Cohort ||
			outcome.Treatment.ArmKey != exposure.ArmKey ||
			outcome.Treatment.Version != exposure.Version {
			return result, fmt.Sprintf("metric %q has incoherent treatment lineage for decision %q", metric.Key, outcome.DecisionID)
		}
		if outcome.Current.EventTime.Before(exposure.ReachedAt) {
			result.Excluded++
			continue
		}
		windowDays := spec.ObservationWindowDays
		if outcome.Current.ObservationWindowDays > 0 {
			windowDays = outcome.Current.ObservationWindowDays
		}
		if windowDays > 0 && outcome.Current.EventTime.After(exposure.ReachedAt.AddDate(0, 0, windowDays)) {
			result.Excluded++
			continue
		}
		if labelVersion == "" {
			labelVersion = outcome.Current.LabelVersion
		} else if labelVersion != outcome.Current.LabelVersion {
			return result, fmt.Sprintf("metric %q mixes label versions %q and %q", metric.Key, labelVersion, outcome.Current.LabelVersion)
		}
		values[exposure.ArmKey] = append(values[exposure.ArmKey], outcome.Current.Value)
	}
	result.LabelVersion = labelVersion
	z := normalCritical(spec.Confidence)
	var champion ArmMetric
	for _, arm := range spec.Arms {
		summary := summarize(arm.Key, metric.Kind, values[arm.Key], z)
		result.Arms = append(result.Arms, summary)
		if arm.Kind == ArmChampion {
			champion = summary
		}
	}
	for _, arm := range result.Arms {
		if arm.ArmKey == champion.ArmKey {
			continue
		}
		effect := arm.Mean - champion.Mean
		se := math.Sqrt(square(arm.StdDev)/float64(max(arm.Count, 1)) +
			square(champion.StdDev)/float64(max(champion.Count, 1)))
		pooled := pooledStd(arm, champion)
		effectSize := 0.0
		if pooled > 0 {
			effectSize = effect / pooled
		}
		result.Comparisons = append(result.Comparisons, Comparison{
			ArmKey: arm.ArmKey, Effect: effect, EffectSize: effectSize,
			Interval:  Interval{Low: effect - z*se, High: effect + z*se},
			Favorable: normalizedEffect(metric.Direction, effect) > 0,
		})
	}
	return result, ""
}

func summarize(armKey string, kind MetricKind, values []float64, z float64) ArmMetric {
	summary := ArmMetric{ArmKey: armKey, Count: len(values)}
	if len(values) == 0 {
		return summary
	}
	for _, value := range values {
		summary.Mean += value
	}
	summary.Mean /= float64(len(values))
	if len(values) > 1 {
		for _, value := range values {
			summary.StdDev += square(value - summary.Mean)
		}
		summary.StdDev = math.Sqrt(summary.StdDev / float64(len(values)-1))
	}
	if kind == MetricBinary {
		summary.Interval = wilson(summary.Mean, len(values), z)
	} else {
		margin := z * summary.StdDev / math.Sqrt(float64(len(values)))
		summary.Interval = Interval{Low: summary.Mean - margin, High: summary.Mean + margin}
	}
	return summary
}

func wilson(p float64, n int, z float64) Interval {
	if n == 0 {
		return Interval{}
	}
	nf, z2 := float64(n), z*z
	center := (p + z2/(2*nf)) / (1 + z2/nf)
	margin := z * math.Sqrt((p*(1-p)+z2/(4*nf))/nf) / (1 + z2/nf)
	return Interval{Low: max(0.0, center-margin), High: min(1.0, center+margin)}
}

func pooledStd(a, b ArmMetric) float64 {
	degrees := a.Count + b.Count - 2
	if degrees <= 0 {
		return 0
	}
	return math.Sqrt(
		(float64(max(a.Count-1, 0))*square(a.StdDev) +
			float64(max(b.Count-1, 0))*square(b.StdDev)) / float64(degrees),
	)
}

func normalizedEffect(direction Direction, effect float64) float64 {
	if direction == DirectionDecrease {
		return -effect
	}
	return effect
}

func normalCritical(confidence float64) float64 {
	return math.Sqrt2 * math.Erfinv(confidence)
}

// sampleRatioPValue computes a chi-square goodness-of-fit p-value. For one
// degree of freedom the closed form is exact; for more arms the
// Wilson-Hilferty normal approximation is deterministic and sufficiently
// conservative for the 0.01 validity gate.
func sampleRatioPValue(arms []Arm, counts map[string]int, total int) float64 {
	if total == 0 {
		return 1
	}
	chi := 0.0
	for _, arm := range arms {
		expected := float64(total) * float64(arm.AllocationBPS) / 10_000
		chi += square(float64(counts[arm.Key])-expected) / expected
	}
	df := float64(len(arms) - 1)
	if df == 1 {
		return math.Erfc(math.Sqrt(chi / 2))
	}
	z := (math.Pow(chi/df, 1.0/3.0) - (1 - 2/(9*df))) / math.Sqrt(2/(9*df))
	return 0.5 * math.Erfc(z/math.Sqrt2)
}

func square(value float64) float64 { return value * value }
