// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"fmt"
	"math"
	"sort"
)

// This file is the light TRAINING pipeline: fit a logistic-regression Spec from a
// labelled dataset with k-fold cross-validation and feature importance. The fit is
// pure and deterministic (batch gradient descent, fixed initialization, no
// randomness), so re-training the same dataset with the same options yields the same
// model — the same replayability contract the rest of the engine keeps. The output is
// an ordinary KindLogistic Spec the Predict node already serves; training is just a
// way to produce one from data instead of hand-authoring coefficients.

// maxTrainRows bounds a training dataset so a single request can't exhaust memory or
// wedge the server in gradient descent.
const maxTrainRows = 100_000

// Row is one labelled training example: feature name→value, and a binary Label (0/1).
type Row struct {
	Features map[string]float64 `json:"features"`
	Label    float64            `json:"label"`
}

// TrainOptions configures the fit. Zero values pick sensible defaults.
type TrainOptions struct {
	// Features fixes the model's feature set and order. Empty → the sorted union of the
	// dataset's feature keys (so a feature absent from a row contributes 0).
	Features     []string `json:"features,omitempty"`
	Iterations   int      `json:"iterations,omitempty"`    // gradient-descent steps (default 500)
	LearningRate float64  `json:"learning_rate,omitempty"` // default 0.5 (on standardized features)
	L2           float64  `json:"l2,omitempty"`            // ridge penalty (default 0)
	Folds        int      `json:"folds,omitempty"`         // CV folds (default 5; <2 disables CV)
}

func (o TrainOptions) withDefaults() TrainOptions {
	if o.Iterations <= 0 {
		o.Iterations = 500
	}
	if o.LearningRate <= 0 {
		o.LearningRate = 0.5
	}
	if o.Folds == 0 {
		o.Folds = 5
	}
	return o
}

// FeatureImportance ranks a feature's contribution: its raw-space Coefficient plus a
// standardized Importance (|coefficient on z-scored features|, normalized to sum 1)
// that is comparable across features on different scales.
type FeatureImportance struct {
	Feature     string  `json:"feature"`
	Coefficient float64 `json:"coefficient"`
	Importance  float64 `json:"importance"`
}

// TrainReport is the diagnostic output of a fit: dataset shape, the training fit's
// log-loss, cross-validated metrics, and per-feature importance.
type TrainReport struct {
	Rows         int                 `json:"rows"`
	Positives    int                 `json:"positives"`
	Features     []string            `json:"features"`
	Iterations   int                 `json:"iterations"`
	TrainLogLoss float64             `json:"train_log_loss"`
	Folds        int                 `json:"folds"`
	CVAUC        float64             `json:"cv_auc"`
	CVLogLoss    float64             `json:"cv_log_loss"`
	CVAccuracy   float64             `json:"cv_accuracy"`
	Importance   []FeatureImportance `json:"importance"`
}

// FitLogistic fits a logistic-regression model to rows and returns a servable Spec
// plus a TrainReport. It fails loudly on an unusable dataset (empty, non-binary
// labels, or only one class — nothing to separate).
func FitLogistic(rows []Row, opts TrainOptions) (Spec, TrainReport, error) {
	opts = opts.withDefaults()
	if len(rows) == 0 {
		return Spec{}, TrainReport{}, fmt.Errorf("models: training needs a non-empty dataset")
	}
	if len(rows) > maxTrainRows {
		return Spec{}, TrainReport{}, fmt.Errorf("models: training dataset has %d rows, over the %d cap", len(rows), maxTrainRows)
	}
	pos := 0
	for i, r := range rows {
		if r.Label != 0 && r.Label != 1 {
			return Spec{}, TrainReport{}, fmt.Errorf("models: row %d label %v is not binary (0 or 1)", i, r.Label)
		}
		if r.Label == 1 {
			pos++
		}
	}
	if pos == 0 || pos == len(rows) {
		return Spec{}, TrainReport{}, fmt.Errorf("models: training needs both classes present (got %d positives of %d)", pos, len(rows))
	}

	feats := opts.Features
	if len(feats) == 0 {
		feats = unionFeatures(rows)
	}
	if len(feats) == 0 {
		return Spec{}, TrainReport{}, fmt.Errorf("models: training rows carry no features")
	}

	// Fit the full dataset for the served model.
	spec, stdW, err := fitStandardized(rows, feats, opts)
	if err != nil {
		return Spec{}, TrainReport{}, err
	}

	report := TrainReport{
		Rows: len(rows), Positives: pos, Features: feats, Iterations: opts.Iterations,
		TrainLogLoss: logLossOf(spec, rows, feats),
		Importance:   importance(feats, spec.Coefficients, stdW),
	}
	if opts.Folds >= 2 && opts.Folds <= len(rows) {
		report.Folds = opts.Folds
		report.CVAUC, report.CVLogLoss, report.CVAccuracy = crossValidate(rows, feats, opts)
	}
	return spec, report, nil
}

// fitStandardized fits on z-scored features (well-conditioned gradient descent) and
// de-standardizes the weights back into raw-feature space, so the returned Spec reads
// the raw features at serve time. It also returns the standardized weights for the
// importance calculation.
func fitStandardized(rows []Row, feats []string, opts TrainOptions) (Spec, []float64, error) {
	mean, std := featureStats(rows, feats)
	x := make([][]float64, len(rows))
	y := make([]float64, len(rows))
	for i, r := range rows {
		x[i] = standardizeRow(r, feats, mean, std)
		y[i] = r.Label
	}
	w, b := gradientDescent(x, y, opts.Iterations, opts.LearningRate, opts.L2)

	// De-standardize: wᵢ·(xᵢ-meanᵢ)/stdᵢ = (wᵢ/stdᵢ)·xᵢ - wᵢ·meanᵢ/stdᵢ.
	coef := make(map[string]float64, len(feats))
	intercept := b
	for j, f := range feats {
		if std[j] == 0 {
			continue // a constant feature carries no signal; drop it from the served model
		}
		coef[f] = w[j] / std[j]
		intercept -= w[j] * mean[j] / std[j]
	}
	if len(coef) == 0 {
		return Spec{}, nil, fmt.Errorf("models: no non-constant features to fit")
	}
	return Spec{Kind: KindLogistic, Intercept: intercept, Coefficients: coef}, w, nil
}

// gradientDescent minimizes the L2-regularized logistic log-loss by batch gradient
// descent from a zero start (deterministic).
func gradientDescent(x [][]float64, y []float64, iters int, lr, l2 float64) (w []float64, b float64) {
	n := len(x)
	d := len(x[0])
	w = make([]float64, d)
	invn := 1.0 / float64(n)
	for it := 0; it < iters; it++ {
		gw := make([]float64, d)
		gb := 0.0
		for i := 0; i < n; i++ {
			z := b
			for j := 0; j < d; j++ {
				z += w[j] * x[i][j]
			}
			e := sigmoid(z) - y[i]
			gb += e
			for j := 0; j < d; j++ {
				gw[j] += e * x[i][j]
			}
		}
		b -= lr * gb * invn
		for j := 0; j < d; j++ {
			w[j] -= lr * (gw[j]*invn + l2*w[j])
		}
	}
	return w, b
}

// crossValidate runs k-fold CV with a deterministic index%folds split and returns the
// mean AUC, log-loss, and accuracy over the folds.
func crossValidate(rows []Row, feats []string, opts TrainOptions) (auc, logloss, acc float64) {
	k := opts.Folds
	var sumAUC, sumLL, sumAcc float64
	valid := 0
	for fold := 0; fold < k; fold++ {
		var train, test []Row
		for i, r := range rows {
			if i%k == fold {
				test = append(test, r)
			} else {
				train = append(train, r)
			}
		}
		if len(test) == 0 || !bothClasses(train) {
			continue // a degenerate fold can't train/score; skip it
		}
		spec, _, err := fitStandardized(train, feats, opts)
		if err != nil {
			continue
		}
		scores := make([]float64, len(test))
		labels := make([]float64, len(test))
		var ll float64
		correct := 0
		for i, r := range test {
			p := predictProb(spec, r, feats)
			scores[i] = p
			labels[i] = r.Label
			ll += -(r.Label*math.Log(clamp01(p)) + (1-r.Label)*math.Log(clamp01(1-p)))
			if (p >= 0.5 && r.Label == 1) || (p < 0.5 && r.Label == 0) {
				correct++
			}
		}
		sumAUC += rankAUC(scores, labels)
		sumLL += ll / float64(len(test))
		sumAcc += float64(correct) / float64(len(test))
		valid++
	}
	if valid == 0 {
		return 0, 0, 0
	}
	return sumAUC / float64(valid), sumLL / float64(valid), sumAcc / float64(valid)
}

// importance normalizes the standardized-space coefficient magnitudes to sum 1 (a
// dropped constant feature gets 0), ranked most-important first.
func importance(feats []string, coef map[string]float64, stdW []float64) []FeatureImportance {
	total := 0.0
	for j := range feats {
		total += math.Abs(stdW[j])
	}
	out := make([]FeatureImportance, 0, len(feats))
	for j, f := range feats {
		imp := 0.0
		if total > 0 {
			imp = math.Abs(stdW[j]) / total
		}
		out = append(out, FeatureImportance{Feature: f, Coefficient: coef[f], Importance: imp})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Importance != out[j].Importance {
			return out[i].Importance > out[j].Importance
		}
		return out[i].Feature < out[j].Feature
	})
	return out
}

// --- helpers ---

func unionFeatures(rows []Row) []string {
	set := map[string]struct{}{}
	for _, r := range rows {
		for k := range r.Features {
			set[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func featureStats(rows []Row, feats []string) (mean, std []float64) {
	mean = make([]float64, len(feats))
	std = make([]float64, len(feats))
	for _, r := range rows {
		for j, f := range feats {
			mean[j] += r.Features[f]
		}
	}
	n := float64(len(rows))
	for j := range mean {
		mean[j] /= n
	}
	for _, r := range rows {
		for j, f := range feats {
			d := r.Features[f] - mean[j]
			std[j] += d * d
		}
	}
	for j := range std {
		std[j] = math.Sqrt(std[j] / n)
	}
	return mean, std
}

func standardizeRow(r Row, feats []string, mean, std []float64) []float64 {
	z := make([]float64, len(feats))
	for j, f := range feats {
		if std[j] == 0 {
			z[j] = 0
			continue
		}
		z[j] = (r.Features[f] - mean[j]) / std[j]
	}
	return z
}

// predictProb serves the fitted spec against a row's raw features (missing → 0).
func predictProb(s Spec, r Row, feats []string) float64 {
	z := s.Intercept
	for _, f := range feats {
		z += s.Coefficients[f] * r.Features[f]
	}
	return sigmoid(z)
}

func logLossOf(s Spec, rows []Row, feats []string) float64 {
	var ll float64
	for _, r := range rows {
		p := predictProb(s, r, feats)
		ll += -(r.Label*math.Log(clamp01(p)) + (1-r.Label)*math.Log(clamp01(1-p)))
	}
	return ll / float64(len(rows))
}

// rankAUC is the Mann-Whitney AUC: the fraction of positive/negative score pairs the
// model ranks correctly (ties count half). 0.5 when a class is absent.
func rankAUC(scores, labels []float64) float64 {
	var pos, neg []float64
	for i, l := range labels {
		if l == 1 {
			pos = append(pos, scores[i])
		} else {
			neg = append(neg, scores[i])
		}
	}
	if len(pos) == 0 || len(neg) == 0 {
		return 0.5
	}
	count := 0.0
	for _, p := range pos {
		for _, n := range neg {
			switch {
			case p > n:
				count++
			case p == n:
				count += 0.5
			}
		}
	}
	return count / float64(len(pos)*len(neg))
}

func bothClasses(rows []Row) bool {
	var seen0, seen1 bool
	for _, r := range rows {
		if r.Label == 1 {
			seen1 = true
		} else {
			seen0 = true
		}
		if seen0 && seen1 {
			return true
		}
	}
	return false
}

// clamp01 keeps a probability strictly inside (0,1) so log-loss never hits ±Inf on a
// perfectly confident prediction.
func clamp01(p float64) float64 {
	const eps = 1e-15
	return math.Max(eps, math.Min(1-eps, p))
}
