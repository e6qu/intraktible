// SPDX-License-Identifier: AGPL-3.0-or-later

package models_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/models"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLogistic(t *testing.T) {
	spec := models.Spec{
		Kind:         models.KindLogistic,
		Intercept:    -1,
		Coefficients: map[string]float64{"fico": 0.01, "income": 0.00001},
	}
	p, err := models.Evaluate(spec, map[string]any{"fico": 700, "income": 50000})
	if err != nil {
		t.Fatal(err)
	}
	// z = -1 + 0.01*700 + 0.00001*50000 = -1 + 7 + 0.5 = 6.5
	if !approx(p.Score, 6.5) {
		t.Fatalf("score = %v, want 6.5", p.Score)
	}
	if p.Probability == nil || !approx(*p.Probability, 1/(1+math.Exp(-6.5))) {
		t.Fatalf("probability = %v", p.Probability)
	}
}

func TestLogisticMissingFeatureFailsLoudly(t *testing.T) {
	spec := models.Spec{Kind: models.KindLogistic, Coefficients: map[string]float64{"fico": 0.01}}
	if _, err := models.Evaluate(spec, map[string]any{"income": 1}); err == nil {
		t.Fatal("expected a missing-feature error")
	}
}

func TestGBM(t *testing.T) {
	// Two stumps splitting on fico at 650 and 720; logit link.
	spec := models.Spec{
		Kind: models.KindGBM,
		Base: 0.1,
		Link: "logit",
		Trees: []models.Tree{
			{Feature: "fico", Threshold: 650,
				Left:  &models.Tree{Leaf: true, Value: -0.5},
				Right: &models.Tree{Leaf: true, Value: 0.3}},
			{Feature: "fico", Threshold: 720,
				Left:  &models.Tree{Leaf: true, Value: -0.2},
				Right: &models.Tree{Leaf: true, Value: 0.4}},
		},
	}
	// fico=700: tree1 right (0.3), tree2 left (-0.2); raw = 0.1+0.3-0.2 = 0.2
	p, err := models.Evaluate(spec, map[string]any{"fico": 700})
	if err != nil {
		t.Fatal(err)
	}
	if !approx(p.Score, 0.2) {
		t.Fatalf("score = %v, want 0.2", p.Score)
	}
	if p.Probability == nil || !approx(*p.Probability, 1/(1+math.Exp(-0.2))) {
		t.Fatalf("probability = %v", p.Probability)
	}
	// fico=600: tree1 left (-0.5), tree2 left (-0.2); raw = 0.1-0.5-0.2 = -0.6
	p2, _ := models.Evaluate(spec, map[string]any{"fico": 600})
	if !approx(p2.Score, -0.6) {
		t.Fatalf("score = %v, want -0.6", p2.Score)
	}
}

func TestExpression(t *testing.T) {
	spec := models.Spec{Kind: models.KindExpression, Expr: "fico * 0.01 + income * 0.0001"}
	p, err := models.Evaluate(spec, map[string]any{"fico": 700, "income": 20000})
	if err != nil {
		t.Fatal(err)
	}
	if !approx(p.Score, 7+2) {
		t.Fatalf("score = %v, want 9", p.Score)
	}
	if p.Probability != nil {
		t.Fatalf("expression model should not produce a probability")
	}
}

func TestEvaluateIsDeterministic(t *testing.T) {
	spec := models.Spec{Kind: models.KindLogistic, Intercept: 0.5, Coefficients: map[string]float64{"x": 2}}
	f := map[string]any{"x": 3}
	a, _ := models.Evaluate(spec, f)
	b, _ := models.Evaluate(spec, f)
	if a.Score != b.Score || (a.Probability == nil) != (b.Probability == nil) || *a.Probability != *b.Probability {
		t.Fatal("Evaluate is not deterministic")
	}
}

func TestParseSpecAndValidate(t *testing.T) {
	good := []string{
		`{"kind":"logistic","intercept":-1,"coefficients":{"fico":0.01}}`,
		`{"kind":"gbm","trees":[{"leaf":true,"value":0.2}]}`,
		`{"kind":"expression","expr":"fico * 2"}`,
	}
	for _, g := range good {
		s, err := models.ParseSpec(json.RawMessage(g))
		if err != nil {
			t.Fatalf("parse %s: %v", g, err)
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("validate %s: %v", g, err)
		}
	}
	bad := []string{
		`{"kind":"logistic"}`,                       // no coefficients
		`{"kind":"gbm","trees":[]}`,                 // no trees
		`{"kind":"expression"}`,                     // no expr
		`{"kind":"mystery","coefficients":{"x":1}}`, // unknown kind
		`{"kind":"logistic","bogus":1}`,             // unknown field (strict decode)
	}
	for _, b := range bad {
		s, err := models.ParseSpec(json.RawMessage(b))
		if err != nil {
			continue // rejected at parse (strict) — fine
		}
		if err := s.Validate(); err == nil {
			t.Fatalf("expected %s to be rejected", b)
		}
	}
}

func TestValidateRejectsForeignFields(t *testing.T) {
	bad := []string{
		`{"kind":"logistic","coefficients":{"x":1},"endpoint":"https://x/score"}`, // stale endpoint
		`{"kind":"external","endpoint":"https://x/score","coefficients":{"x":1}}`, // stale coefficients
		`{"kind":"expression","expr":"x","trees":[{"leaf":true,"value":1}]}`,      // stale trees
		`{"kind":"gbm","trees":[{"leaf":true,"value":1}],"expr":"x"}`,             // stale expr
	}
	for _, b := range bad {
		s, err := models.ParseSpec(json.RawMessage(b))
		if err != nil {
			t.Fatalf("parse %s: %v", b, err)
		}
		if err := s.Validate(); err == nil {
			t.Fatalf("expected a foreign-field rejection for %s", b)
		}
	}
}
