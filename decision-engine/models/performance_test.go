// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"math"
	"testing"
)

func TestPerformanceFromActuals(t *testing.T) {
	// A well-ranked model: outcomes concentrate positives in the high deciles and
	// negatives in the low deciles.
	var st ModelStats
	st.Name = "risk"
	st.Actuals[9] = ActualBucket{Pos: 40, Neg: 2} // predicted ~0.95
	st.Actuals[0] = ActualBucket{Pos: 3, Neg: 45} // predicted ~0.05
	st.ActualCount = 40 + 2 + 3 + 45

	perf := st.Performance()
	if perf.Count != 90 || perf.Positives != 43 {
		t.Fatalf("count/positives = %d/%d", perf.Count, perf.Positives)
	}
	// Accuracy: high bucket predicts positive (40 right, 2 wrong), low bucket predicts
	// negative (45 right, 3 wrong) → 85/90.
	if math.Abs(perf.Accuracy-85.0/90.0) > 1e-9 {
		t.Fatalf("accuracy = %v", perf.Accuracy)
	}
	if perf.AUC < 0.9 {
		t.Fatalf("AUC = %v, want ≥ 0.9 for a well-ranked model", perf.AUC)
	}
	if perf.Brier <= 0 || perf.Brier > 0.2 {
		t.Fatalf("brier = %v, want a small positive", perf.Brier)
	}
	// Calibration: the high decile's realized rate ≈ 40/42, the low decile's ≈ 3/48.
	byBucket := map[int]Calibration{}
	for _, c := range perf.Calibration {
		byBucket[c.Bucket] = c
	}
	if math.Abs(byBucket[9].Actual-40.0/42.0) > 1e-9 || math.Abs(byBucket[0].Actual-3.0/48.0) > 1e-9 {
		t.Fatalf("calibration wrong: %+v", perf.Calibration)
	}
}

func TestPerformanceEmpty(t *testing.T) {
	var st ModelStats
	if p := st.Performance(); p.Count != 0 || len(p.Calibration) != 0 {
		t.Fatalf("empty performance = %+v", p)
	}
}

func TestFeatureDriftDetectsShift(t *testing.T) {
	st := ModelStats{
		FeatureBaseline: map[string]FeatureBaselineStat{
			"fico":   {Mean: 700, Std: 40, Count: 1000},
			"income": {Mean: 50000, Std: 10000, Count: 1000},
		},
		Features: map[string]FeatureStat{},
	}
	// fico shifted down by ~1 baseline std (drift); income unchanged in both mean and
	// spread (a comparable variance, so the variance-ratio check doesn't fire either).
	st.Features["fico"] = statOf([]float64{660, 660, 660, 660}) // mean 660 = -1 std
	st.Features["income"] = statOf([]float64{40000, 60000, 50000, 55000, 45000})

	fd := st.FeatureDrift()
	byName := map[string]FeatureDrift{}
	for _, f := range fd {
		byName[f.Feature] = f
	}
	if !byName["fico"].Drifting {
		t.Fatalf("fico should be drifting: %+v", byName["fico"])
	}
	if byName["income"].Drifting {
		t.Fatalf("income should be stable: %+v", byName["income"])
	}
	// Most-drifted first.
	if fd[0].Feature != "fico" {
		t.Fatalf("most-drifted feature = %q, want fico", fd[0].Feature)
	}
}

func statOf(xs []float64) FeatureStat {
	var f FeatureStat
	for _, x := range xs {
		f.observe(x)
	}
	return f
}
