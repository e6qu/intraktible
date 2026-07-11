// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"context"
	"math"
	"sort"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// This file computes the two GAPS #6 read models from the accumulated ModelStats:
// covariate (feature) drift, and live performance reconciled against actuals.

// --- Covariate (feature) drift ---

// featureDriftThreshold flags a feature whose standardized mean has shifted by at least
// this many baseline standard deviations since capture. 0.25 mirrors the "moderate PSI"
// convention used for the prediction distribution.
const featureDriftThreshold = 0.25

// FeatureDrift reports how far one input feature's distribution has moved since the
// baseline was captured — a covariate shift that typically precedes prediction drift.
type FeatureDrift struct {
	Feature      string  `json:"feature"`
	Count        int     `json:"count"`
	Mean         float64 `json:"mean"`
	Std          float64 `json:"std"`
	BaselineMean float64 `json:"baseline_mean"`
	BaselineStd  float64 `json:"baseline_std"`
	MeanShift    float64 `json:"mean_shift"` // |mean - baseline_mean| / baseline_std
	VarRatio     float64 `json:"var_ratio"`  // std / baseline_std
	Drifting     bool    `json:"drifting"`
}

// FeatureDrift returns per-feature covariate drift vs the captured baseline, ranked by
// the size of the shift (most-drifted first). Empty until a baseline exists.
func (st ModelStats) FeatureDrift() []FeatureDrift {
	if len(st.FeatureBaseline) == 0 {
		return nil
	}
	out := make([]FeatureDrift, 0, len(st.FeatureBaseline))
	for name, base := range st.FeatureBaseline {
		cur := st.Features[name]
		fd := FeatureDrift{
			Feature: name, Count: cur.Count, Mean: cur.Mean, Std: cur.Std(),
			BaselineMean: base.Mean, BaselineStd: base.Std,
		}
		if base.Std > 0 {
			fd.MeanShift = math.Abs(cur.Mean-base.Mean) / base.Std
			fd.VarRatio = cur.Std() / base.Std
			fd.Drifting = fd.MeanShift >= featureDriftThreshold || fd.VarRatio >= 2 || fd.VarRatio <= 0.5
		}
		out = append(out, fd)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MeanShift != out[j].MeanShift {
			return out[i].MeanShift > out[j].MeanShift
		}
		return out[i].Feature < out[j].Feature
	})
	return out
}

// --- Actuals reconciliation / live performance ---

// Calibration is one predicted-probability decile's realized outcome rate: a
// well-calibrated model's Actual tracks Predicted (the bucket midpoint).
type Calibration struct {
	Bucket    int     `json:"bucket"`
	Predicted float64 `json:"predicted"` // bucket midpoint
	Actual    float64 `json:"actual"`    // realized positive rate in the bucket
	Count     int     `json:"count"`
}

// Performance is a model's live performance measured from reconciled actuals.
type Performance struct {
	Model       string        `json:"model"`
	Count       int           `json:"count"`
	Positives   int           `json:"positives"`
	Accuracy    float64       `json:"accuracy"` // predicted class (p≥0.5) vs realized label
	Brier       float64       `json:"brier"`    // mean squared error of probability vs label
	AUC         float64       `json:"auc"`      // realized AUC from the bucketed outcomes
	Calibration []Calibration `json:"calibration"`
}

// Performance derives live metrics from the per-decile actual counts. It uses the
// bucket midpoint as the representative predicted probability (the raw probabilities
// aren't retained — the deciles keep the projection bounded).
func (st ModelStats) Performance() Performance {
	perf := Performance{Model: st.Name, Count: st.ActualCount, Calibration: []Calibration{}}
	if st.ActualCount == 0 {
		return perf
	}
	var correct, totalPos, totalNeg int
	var brier float64
	n := float64(len(st.Actuals))
	for i, b := range st.Actuals {
		mid := (float64(i) + 0.5) / n
		totalPos += b.Pos
		totalNeg += b.Neg
		// Predicted class is positive when the midpoint ≥ 0.5.
		if mid >= 0.5 {
			correct += b.Pos
		} else {
			correct += b.Neg
		}
		brier += float64(b.Pos)*(1-mid)*(1-mid) + float64(b.Neg)*mid*mid
		if c := b.Pos + b.Neg; c > 0 {
			perf.Calibration = append(perf.Calibration, Calibration{
				Bucket: i, Predicted: mid, Actual: float64(b.Pos) / float64(c), Count: c,
			})
		}
	}
	perf.Positives = totalPos
	perf.Accuracy = float64(correct) / float64(st.ActualCount)
	perf.Brier = brier / float64(st.ActualCount)
	perf.AUC = bucketedAUC(st.Actuals, totalPos, totalNeg)
	return perf
}

// bucketedAUC computes the realized AUC from the per-decile positive/negative counts:
// the fraction of positive/negative pairs the model ranked correctly (a positive in a
// higher decile than a negative), ties within a decile counting half.
func bucketedAUC(actuals [driftBuckets]ActualBucket, totalPos, totalNeg int) float64 {
	if totalPos == 0 || totalNeg == 0 {
		return 0.5 // undefined with a single class
	}
	var concordant float64
	for i := range actuals {
		for j := range actuals {
			pairs := float64(actuals[i].Pos) * float64(actuals[j].Neg)
			switch {
			case i > j:
				concordant += pairs
			case i == j:
				concordant += 0.5 * pairs
			}
		}
	}
	return concordant / (float64(totalPos) * float64(totalNeg))
}

// ReadPerformance loads a model's reconciled performance (zero-valued when the model
// has no stats yet).
func ReadPerformance(ctx context.Context, s store.Store, id identity.Identity, model string) (Performance, error) {
	st, ok, err := store.GetDoc[ModelStats](ctx, s, StatsCollection, store.Key(id.Org, id.Workspace, model))
	if err != nil {
		return Performance{}, err
	}
	if !ok {
		return Performance{Model: model, Calibration: []Calibration{}}, nil
	}
	return st.Performance(), nil
}
