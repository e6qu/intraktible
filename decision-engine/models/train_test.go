// SPDX-License-Identifier: AGPL-3.0-or-later

package models_test

import (
	"math"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/models"
)

// buildDataset makes a separable-ish binary dataset: the label is driven by `signal`
// (higher → positive) while `noise` is irrelevant, so a good fit weights signal and
// ignores noise.
func buildDataset() []models.Row {
	var rows []models.Row
	for i := 0; i < 200; i++ {
		signal := float64(i%100) / 10 // 0..9.9
		label := 0.0
		if signal >= 5 {
			label = 1
		}
		noise := float64((i*37)%50) / 10
		rows = append(rows, models.Row{
			Features: map[string]float64{"signal": signal, "noise": noise},
			Label:    label,
		})
	}
	return rows
}

func TestFitLogisticSeparates(t *testing.T) {
	rows := buildDataset()
	spec, report, err := models.FitLogistic(rows, models.TrainOptions{Iterations: 800, Folds: 5})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != models.KindLogistic || len(spec.Coefficients) == 0 {
		t.Fatalf("expected a logistic spec with coefficients, got %+v", spec)
	}
	// The fitted spec must be servable and separate the classes well.
	if report.CVAUC < 0.9 {
		t.Fatalf("cross-validated AUC = %v, want ≥ 0.9 on a separable dataset", report.CVAUC)
	}
	if report.CVAccuracy < 0.9 {
		t.Fatalf("cross-validated accuracy = %v, want ≥ 0.9", report.CVAccuracy)
	}
	// The signal feature must dominate importance over the noise feature.
	imp := map[string]float64{}
	for _, fi := range report.Importance {
		imp[fi.Feature] = fi.Importance
	}
	if imp["signal"] <= imp["noise"] {
		t.Fatalf("signal importance %v should exceed noise %v", imp["signal"], imp["noise"])
	}
	if report.Importance[0].Feature != "signal" {
		t.Fatalf("most-important feature = %q, want signal", report.Importance[0].Feature)
	}

	// The served model agrees with the training labels on clear cases.
	hi, _ := models.Evaluate(spec, map[string]any{"signal": 9.0, "noise": 1.0})
	lo, _ := models.Evaluate(spec, map[string]any{"signal": 1.0, "noise": 4.0})
	if hi.Probability == nil || lo.Probability == nil {
		t.Fatal("logistic prediction must carry a probability")
	}
	if *hi.Probability < 0.5 || *lo.Probability > 0.5 {
		t.Fatalf("served probabilities wrong: hi=%v lo=%v", *hi.Probability, *lo.Probability)
	}
}

func TestFitLogisticDeterministic(t *testing.T) {
	rows := buildDataset()
	a, _, err := models.FitLogistic(rows, models.TrainOptions{Iterations: 300})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := models.FitLogistic(rows, models.TrainOptions{Iterations: 300})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(a.Intercept-b.Intercept) > 1e-12 {
		t.Fatalf("intercepts differ: %v vs %v", a.Intercept, b.Intercept)
	}
	for f, w := range a.Coefficients {
		if math.Abs(w-b.Coefficients[f]) > 1e-12 {
			t.Fatalf("coefficient %q differs: %v vs %v", f, w, b.Coefficients[f])
		}
	}
}

func TestFitLogisticRejectsUnusableData(t *testing.T) {
	// Empty.
	if _, _, err := models.FitLogistic(nil, models.TrainOptions{}); err == nil {
		t.Fatal("empty dataset must fail")
	}
	// One class only — nothing to separate.
	oneClass := []models.Row{
		{Features: map[string]float64{"x": 1}, Label: 1},
		{Features: map[string]float64{"x": 2}, Label: 1},
	}
	if _, _, err := models.FitLogistic(oneClass, models.TrainOptions{}); err == nil {
		t.Fatal("a single-class dataset must fail")
	}
	// Non-binary label.
	badLabel := []models.Row{
		{Features: map[string]float64{"x": 1}, Label: 0},
		{Features: map[string]float64{"x": 2}, Label: 2},
	}
	if _, _, err := models.FitLogistic(badLabel, models.TrainOptions{}); err == nil {
		t.Fatal("a non-binary label must fail")
	}
}
