// SPDX-License-Identifier: AGPL-3.0-or-later

// Package models hosts predictive models as data and evaluates them deterministically
// — no external runtime, no CGO (the §9 "ONNX serving at scale" non-goal stands).
// A model is a typed spec stored in the registry; Evaluate is a pure function so a
// prediction is reproducible and a decision that uses one stays replayable. The
// decision shell resolves a Predict node's model from the registry, evaluates it,
// and injects the result under predict.<output> (mirroring the Connect/AI nodes).
package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/expr-lang/expr"
)

// Model kinds. All three evaluate purely over a feature map.
const (
	KindLogistic   = "logistic"   // sigmoid(intercept + Σ wᵢ·xᵢ)
	KindGBM        = "gbm"        // sum of regression trees (+ base), optional logit link
	KindExpression = "expression" // a single expr-lang scoring expression over features
)

// Spec is a model definition: a kind plus the kind-specific parameters. Unknown
// fields are rejected at decode so a misconfigured model fails loudly.
type Spec struct {
	Kind string `json:"kind"`

	// logistic
	Intercept    float64            `json:"intercept,omitempty"`
	Coefficients map[string]float64 `json:"coefficients,omitempty"`

	// gbm
	Base  float64 `json:"base,omitempty"`
	Trees []Tree  `json:"trees,omitempty"`
	Link  string  `json:"link,omitempty"` // "logit" applies a sigmoid to the raw sum

	// expression
	Expr string `json:"expr,omitempty"`
}

// Tree is a binary regression tree: a leaf carries Value; a split sends the row to
// Left when feature < Threshold, else Right.
type Tree struct {
	Leaf      bool    `json:"leaf,omitempty"`
	Value     float64 `json:"value,omitempty"`
	Feature   string  `json:"feature,omitempty"`
	Threshold float64 `json:"threshold,omitempty"`
	Left      *Tree   `json:"left,omitempty"`
	Right     *Tree   `json:"right,omitempty"`
}

// Prediction is the model output injected under predict.<output>. Probability is
// present for classifiers (logistic, or a gbm with a logit link).
type Prediction struct {
	Score       float64  `json:"score"`
	Probability *float64 `json:"probability,omitempty"`
}

// ParseSpec strictly decodes a model spec (rejecting unknown fields).
func ParseSpec(raw json.RawMessage) (Spec, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("models: spec: %w", err)
	}
	return s, nil
}

// Validate checks a spec is well-formed for its kind (cheap, structural).
func (s Spec) Validate() error {
	switch s.Kind {
	case KindLogistic:
		if len(s.Coefficients) == 0 {
			return fmt.Errorf("models: logistic model needs coefficients")
		}
	case KindGBM:
		if len(s.Trees) == 0 {
			return fmt.Errorf("models: gbm model needs at least one tree")
		}
		for i := range s.Trees {
			if err := s.Trees[i].validate(); err != nil {
				return fmt.Errorf("models: gbm tree %d: %w", i, err)
			}
		}
		if s.Link != "" && s.Link != "logit" {
			return fmt.Errorf("models: gbm link %q is not supported (logit only)", s.Link)
		}
	case KindExpression:
		if s.Expr == "" {
			return fmt.Errorf("models: expression model needs an expr")
		}
	default:
		return fmt.Errorf("models: unknown model kind %q (logistic|gbm|expression)", s.Kind)
	}
	return nil
}

func (t *Tree) validate() error {
	if t == nil {
		return fmt.Errorf("nil node")
	}
	if t.Leaf {
		return nil
	}
	if t.Feature == "" {
		return fmt.Errorf("split needs a feature")
	}
	if t.Left == nil || t.Right == nil {
		return fmt.Errorf("split needs left and right children")
	}
	if err := t.Left.validate(); err != nil {
		return err
	}
	return t.Right.validate()
}

// Evaluate runs the model over the feature map and returns its prediction. It is a
// pure, deterministic function (no clock, no I/O), so a recorded prediction replays
// identically. A coefficient/feature referenced but absent fails loudly.
func Evaluate(s Spec, features map[string]any) (Prediction, error) {
	switch s.Kind {
	case KindLogistic:
		return evalLogistic(s, features)
	case KindGBM:
		return evalGBM(s, features)
	case KindExpression:
		return evalExpression(s, features)
	default:
		return Prediction{}, fmt.Errorf("models: unknown model kind %q", s.Kind)
	}
}

func evalLogistic(s Spec, features map[string]any) (Prediction, error) {
	z := s.Intercept
	for name, w := range s.Coefficients {
		x, err := feature(features, name)
		if err != nil {
			return Prediction{}, err
		}
		z += w * x
	}
	p := sigmoid(z)
	return Prediction{Score: z, Probability: &p}, nil
}

func evalGBM(s Spec, features map[string]any) (Prediction, error) {
	raw := s.Base
	for i := range s.Trees {
		v, err := s.Trees[i].eval(features)
		if err != nil {
			return Prediction{}, fmt.Errorf("models: gbm tree %d: %w", i, err)
		}
		raw += v
	}
	if s.Link == "logit" {
		p := sigmoid(raw)
		return Prediction{Score: raw, Probability: &p}, nil
	}
	return Prediction{Score: raw}, nil
}

func (t *Tree) eval(features map[string]any) (float64, error) {
	if t.Leaf {
		return t.Value, nil
	}
	x, err := feature(features, t.Feature)
	if err != nil {
		return 0, err
	}
	if x < t.Threshold {
		return t.Left.eval(features)
	}
	return t.Right.eval(features)
}

func evalExpression(s Spec, features map[string]any) (Prediction, error) {
	program, err := expr.Compile(s.Expr, expr.Env(features))
	if err != nil {
		return Prediction{}, fmt.Errorf("models: compile expression: %w", err)
	}
	out, err := expr.Run(program, features)
	if err != nil {
		return Prediction{}, fmt.Errorf("models: run expression: %w", err)
	}
	score, ok := toFloat(out)
	if !ok {
		return Prediction{}, fmt.Errorf("models: expression did not evaluate to a number (got %T)", out)
	}
	return Prediction{Score: score}, nil
}

func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

// feature reads a numeric feature by name, failing loudly when it is absent or
// non-numeric (a model must not silently score on missing data).
func feature(features map[string]any, name string) (float64, error) {
	v, ok := features[name]
	if !ok {
		return 0, fmt.Errorf("models: feature %q is missing from the input", name)
	}
	f, ok := toFloat(v)
	if !ok {
		return 0, fmt.Errorf("models: feature %q is not numeric (got %T)", name, v)
	}
	return f, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
