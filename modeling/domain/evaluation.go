// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/stats"
)

// ScoredRow is the value-free input to statistical binary evaluation.
type ScoredRow struct {
	EntityID      string            `json:"entity_id"`
	Score         float64           `json:"score"`
	Label         float64           `json:"label"`
	Partition     string            `json:"partition"`
	Segments      map[string]string `json:"segments,omitempty"`
	ObservationAt time.Time         `json:"observation_at"`
}

// EvaluationOptions configures threshold and statistical slices.
type EvaluationOptions struct {
	Threshold         float64 `json:"threshold"`
	FalsePositiveCost float64 `json:"false_positive_cost"`
	FalseNegativeCost float64 `json:"false_negative_cost"`
	CalibrationBins   int     `json:"calibration_bins"`
	MinimumSegmentN   int     `json:"minimum_segment_n"`
}

// ConfusionMatrix is the thresholded classification result.
type ConfusionMatrix struct {
	TruePositive  int `json:"true_positive"`
	TrueNegative  int `json:"true_negative"`
	FalsePositive int `json:"false_positive"`
	FalseNegative int `json:"false_negative"`
}

// ConfidenceInterval is a 95% interval.
type ConfidenceInterval struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

// CalibrationBin compares predicted and realized rates.
type CalibrationBin struct {
	LowerBound   float64 `json:"lower_bound"`
	UpperBound   float64 `json:"upper_bound"`
	Count        int     `json:"count"`
	MeanScore    float64 `json:"mean_score"`
	ObservedRate float64 `json:"observed_rate"`
}

// SegmentMetric is a fairness/performance slice with uncertainty.
type SegmentMetric struct {
	Segment       string             `json:"segment"`
	Value         string             `json:"value"`
	Count         int                `json:"count"`
	Positives     int                `json:"positives"`
	SelectionRate float64            `json:"selection_rate"`
	Accuracy      float64            `json:"accuracy"`
	AccuracyCI    ConfidenceInterval `json:"accuracy_ci"`
	ImpactRatio   float64            `json:"impact_ratio,omitempty"`
	PValue        float64            `json:"p_value,omitempty"`
	Significant   bool               `json:"significant"`
	Underpowered  bool               `json:"underpowered"`
}

// TemporalMetric is one observation-month slice.
type TemporalMetric struct {
	Period   string  `json:"period"`
	Count    int     `json:"count"`
	Accuracy float64 `json:"accuracy"`
	Brier    float64 `json:"brier"`
}

// BinaryEvaluation is reproducible model-validation evidence.
type BinaryEvaluation struct {
	Rows                int                `json:"rows"`
	Positives           int                `json:"positives"`
	Threshold           float64            `json:"threshold"`
	OptimalThreshold    float64            `json:"optimal_threshold"`
	OptimalExpectedCost float64            `json:"optimal_expected_cost"`
	AUC                 float64            `json:"auc"`
	LogLoss             float64            `json:"log_loss"`
	Brier               float64            `json:"brier"`
	Accuracy            float64            `json:"accuracy"`
	AccuracyCI          ConfidenceInterval `json:"accuracy_ci"`
	Confusion           ConfusionMatrix    `json:"confusion"`
	Calibration         []CalibrationBin   `json:"calibration"`
	Segments            []SegmentMetric    `json:"segments"`
	Intersections       []SegmentMetric    `json:"intersections"`
	Temporal            []TemporalMetric   `json:"temporal"`
	LeakageFindings     []string           `json:"leakage_findings"`
	PassedLeakageChecks bool               `json:"passed_leakage_checks"`
}

// EvaluateBinary calculates statistically explicit binary-model evidence.
func EvaluateBinary(rows []ScoredRow, options EvaluationOptions) (BinaryEvaluation, error) {
	if len(rows) == 0 {
		return BinaryEvaluation{}, errors.New("modeling: evaluation rows are empty")
	}
	if options.Threshold == 0 {
		options.Threshold = 0.5
	}
	if options.Threshold < 0 || options.Threshold > 1 ||
		math.IsNaN(options.Threshold) || math.IsInf(options.Threshold, 0) {
		return BinaryEvaluation{}, errors.New("modeling: threshold must be finite and between 0 and 1")
	}
	if options.FalsePositiveCost == 0 {
		options.FalsePositiveCost = 1
	}
	if options.FalseNegativeCost == 0 {
		options.FalseNegativeCost = 1
	}
	if options.FalsePositiveCost < 0 || options.FalseNegativeCost < 0 {
		return BinaryEvaluation{}, errors.New("modeling: threshold costs must not be negative")
	}
	if options.CalibrationBins == 0 {
		options.CalibrationBins = 10
	}
	if options.CalibrationBins < 2 || options.CalibrationBins > 100 {
		return BinaryEvaluation{}, errors.New("modeling: calibration_bins must be between 2 and 100")
	}
	if options.MinimumSegmentN == 0 {
		options.MinimumSegmentN = 30
	}
	if options.MinimumSegmentN < 1 {
		return BinaryEvaluation{}, errors.New("modeling: minimum_segment_n must be positive")
	}
	scores, labels := make([]float64, len(rows)), make([]float64, len(rows))
	positives := 0
	leakage := map[string]bool{}
	for index, row := range rows {
		if row.Score < 0 || row.Score > 1 || math.IsNaN(row.Score) || math.IsInf(row.Score, 0) {
			return BinaryEvaluation{}, fmt.Errorf("modeling: row %d score must be finite and between 0 and 1", index)
		}
		if row.Label != 0 && row.Label != 1 {
			return BinaryEvaluation{}, fmt.Errorf("modeling: row %d label must be binary", index)
		}
		scores[index], labels[index] = row.Score, row.Label
		if row.Label == 1 {
			positives++
		}
		if row.Partition == "train" {
			leakage["training partition included in evaluation"] = true
		}
	}
	if positives == 0 || positives == len(rows) {
		return BinaryEvaluation{}, errors.New("modeling: evaluation requires both classes")
	}
	confusion, accuracy := confusionAt(rows, options.Threshold)
	logLoss, brier := loss(rows)
	optimalThreshold, optimalCost := optimizeThreshold(
		rows, options.FalsePositiveCost, options.FalseNegativeCost,
	)
	findings := make([]string, 0, len(leakage))
	for finding := range leakage {
		findings = append(findings, finding)
	}
	sort.Strings(findings)
	report := BinaryEvaluation{
		Rows: len(rows), Positives: positives, Threshold: options.Threshold,
		OptimalThreshold: optimalThreshold, OptimalExpectedCost: optimalCost,
		AUC: rankAUC(scores, labels), LogLoss: logLoss, Brier: brier,
		Accuracy: accuracy, AccuracyCI: wilson(int(math.Round(accuracy*float64(len(rows)))), len(rows)),
		Confusion: confusion, Calibration: calibration(rows, options.CalibrationBins),
		LeakageFindings: findings, PassedLeakageChecks: len(findings) == 0,
	}
	report.Segments, report.Intersections = segmentMetrics(rows, options)
	report.Temporal = temporalMetrics(rows, options.Threshold)
	return report, nil
}

func confusionAt(rows []ScoredRow, threshold float64) (ConfusionMatrix, float64) {
	var matrix ConfusionMatrix
	for _, row := range rows {
		predicted := row.Score >= threshold
		switch {
		case predicted && row.Label == 1:
			matrix.TruePositive++
		case predicted:
			matrix.FalsePositive++
		case row.Label == 1:
			matrix.FalseNegative++
		default:
			matrix.TrueNegative++
		}
	}
	correct := matrix.TruePositive + matrix.TrueNegative
	return matrix, float64(correct) / float64(len(rows))
}

func loss(rows []ScoredRow) (float64, float64) {
	var logLoss, brier float64
	for _, row := range rows {
		probability := math.Min(math.Max(row.Score, 1e-15), 1-1e-15)
		logLoss -= row.Label*math.Log(probability) + (1-row.Label)*math.Log(1-probability)
		delta := probability - row.Label
		brier += delta * delta
	}
	return logLoss / float64(len(rows)), brier / float64(len(rows))
}

func optimizeThreshold(rows []ScoredRow, falsePositiveCost, falseNegativeCost float64) (float64, float64) {
	candidates := []float64{0, 1}
	for _, row := range rows {
		candidates = append(candidates, row.Score)
	}
	sort.Float64s(candidates)
	bestThreshold, bestCost := 0.5, math.Inf(1)
	for _, threshold := range candidates {
		matrix, _ := confusionAt(rows, threshold)
		cost := (float64(matrix.FalsePositive)*falsePositiveCost +
			float64(matrix.FalseNegative)*falseNegativeCost) / float64(len(rows))
		if cost < bestCost || cost == bestCost && threshold < bestThreshold {
			bestThreshold, bestCost = threshold, cost
		}
	}
	return bestThreshold, bestCost
}

func calibration(rows []ScoredRow, count int) []CalibrationBin {
	bins := make([]CalibrationBin, count)
	var scoreSums, labelSums []float64
	scoreSums, labelSums = make([]float64, count), make([]float64, count)
	for index := range bins {
		bins[index].LowerBound = float64(index) / float64(count)
		bins[index].UpperBound = float64(index+1) / float64(count)
	}
	for _, row := range rows {
		index := int(row.Score * float64(count))
		if index == count {
			index--
		}
		bins[index].Count++
		scoreSums[index] += row.Score
		labelSums[index] += row.Label
	}
	for index := range bins {
		if bins[index].Count > 0 {
			bins[index].MeanScore = scoreSums[index] / float64(bins[index].Count)
			bins[index].ObservedRate = labelSums[index] / float64(bins[index].Count)
		}
	}
	return bins
}

func wilson(successes, total int) ConfidenceInterval {
	lower, upper := stats.Wilson(successes, total)
	return ConfidenceInterval{Lower: lower, Upper: upper}
}

func segmentMetrics(rows []ScoredRow, options EvaluationOptions) ([]SegmentMetric, []SegmentMetric) {
	groups := map[string][]ScoredRow{}
	intersections := map[string][]ScoredRow{}
	for _, row := range rows {
		keys := make([]string, 0, len(row.Segments))
		for field, value := range row.Segments {
			if value == "" {
				value = "[missing]"
			}
			groups[field+"\x00"+value] = append(groups[field+"\x00"+value], row)
			keys = append(keys, field+"="+value)
		}
		sort.Strings(keys)
		if len(keys) > 1 {
			key := strings.Join(keys, " ∩ ")
			intersections[key] = append(intersections[key], row)
		}
	}
	return buildGroupMetrics(groups, options, false), buildGroupMetrics(intersections, options, true)
}

func buildGroupMetrics(
	groups map[string][]ScoredRow,
	options EvaluationOptions,
	intersection bool,
) []SegmentMetric {
	type referenceGroup struct {
		rate     float64
		selected int
		total    int
	}
	references := map[string]referenceGroup{}
	for key, rows := range groups {
		field := key
		if !intersection {
			field = strings.SplitN(key, "\x00", 2)[0]
		}
		matrix, _ := confusionAt(rows, options.Threshold)
		selected := matrix.TruePositive + matrix.FalsePositive
		rate := float64(selected) / float64(len(rows))
		if rate > references[field].rate {
			references[field] = referenceGroup{rate: rate, selected: selected, total: len(rows)}
		}
	}
	out := make([]SegmentMetric, 0, len(groups))
	for key, rows := range groups {
		field, value := "intersection", key
		if !intersection {
			parts := strings.SplitN(key, "\x00", 2)
			field, value = parts[0], parts[1]
		}
		matrix, accuracy := confusionAt(rows, options.Threshold)
		selected := matrix.TruePositive + matrix.FalsePositive
		positives := 0
		for _, row := range rows {
			if row.Label == 1 {
				positives++
			}
		}
		reference := references[field]
		ratio := 0.0
		selectionRate := float64(selected) / float64(len(rows))
		if reference.rate > 0 {
			ratio = selectionRate / reference.rate
		}
		pValue := twoProportionPValue(selected, len(rows), reference.selected, reference.total)
		out = append(out, SegmentMetric{
			Segment: field, Value: value, Count: len(rows), Positives: positives,
			SelectionRate: selectionRate,
			Accuracy:      accuracy, AccuracyCI: wilson(matrix.TruePositive+matrix.TrueNegative, len(rows)),
			ImpactRatio: ratio, PValue: pValue, Significant: pValue < 0.05,
			Underpowered: len(rows) < options.MinimumSegmentN,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Segment != out[j].Segment {
			return out[i].Segment < out[j].Segment
		}
		return out[i].Value < out[j].Value
	})
	return out
}

func twoProportionPValue(successA, totalA, successB, totalB int) float64 {
	if totalA == 0 || totalB == 0 {
		return 1
	}
	pA, pB := float64(successA)/float64(totalA), float64(successB)/float64(totalB)
	pooled := float64(successA+successB) / float64(totalA+totalB)
	standardError := math.Sqrt(pooled * (1 - pooled) * (1/float64(totalA) + 1/float64(totalB)))
	if standardError == 0 {
		return 1
	}
	z := math.Abs(pA-pB) / standardError
	return math.Erfc(z / math.Sqrt2)
}

func temporalMetrics(rows []ScoredRow, threshold float64) []TemporalMetric {
	groups := map[string][]ScoredRow{}
	for _, row := range rows {
		period := row.ObservationAt.UTC().Format("2006-01")
		groups[period] = append(groups[period], row)
	}
	periods := make([]string, 0, len(groups))
	for period := range groups {
		periods = append(periods, period)
	}
	sort.Strings(periods)
	out := make([]TemporalMetric, 0, len(periods))
	for _, period := range periods {
		_, accuracy := confusionAt(groups[period], threshold)
		_, brier := loss(groups[period])
		out = append(out, TemporalMetric{
			Period: period, Count: len(groups[period]), Accuracy: accuracy, Brier: brier,
		})
	}
	return out
}

func rankAUC(scores, labels []float64) float64 {
	type pair struct{ score, label float64 }
	pairs := make([]pair, len(scores))
	positives := 0
	for index := range scores {
		pairs[index] = pair{score: scores[index], label: labels[index]}
		if labels[index] == 1 {
			positives++
		}
	}
	negatives := len(scores) - positives
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].score < pairs[j].score })
	var rankSum float64
	for start := 0; start < len(pairs); {
		end := start + 1
		for end < len(pairs) && pairs[end].score == pairs[start].score {
			end++
		}
		averageRank := float64(start+1+end) / 2
		for index := start; index < end; index++ {
			if pairs[index].label == 1 {
				rankSum += averageRank
			}
		}
		start = end
	}
	return (rankSum - float64(positives*(positives+1))/2) / float64(positives*negatives)
}
