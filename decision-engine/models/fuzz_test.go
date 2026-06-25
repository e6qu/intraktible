// SPDX-License-Identifier: AGPL-3.0-or-later

package models_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/models"
)

// FuzzParseSpec asserts model-spec parsing + validation never panic on arbitrary
// input — ParseSpec/Validate guard the registry (and, for GBM, the non-nil tree
// children that (*Tree).eval relies on). It deliberately does NOT call Evaluate:
// expression evaluation (expr.Run) over a fuzzed operator-authored expression can
// be pathologically slow, which is a known unbounded-evaluation limitation (the
// pure deterministic core can't hold a wall-clock timeout), not a parse defect.
func FuzzParseSpec(f *testing.F) {
	seeds := []string{
		`{"kind":"logistic","intercept":-3,"coefficients":{"fico":0.005}}`,
		`{"kind":"gbm","trees":[{"feature":"x","threshold":1,"left":{"leaf":true,"value":-1},"right":{"leaf":true,"value":1}}]}`,
		`{"kind":"gbm","trees":[{"feature":"x","threshold":1}]}`, // missing children
		`{"kind":"expression","expr":"fico * 0.001"}`,
		`{"kind":"external","endpoint":"https://x/score"}`,
		`{"kind":"bogus"}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, specJSON string) {
		if !json.Valid([]byte(specJSON)) {
			return
		}
		spec, err := models.ParseSpec(json.RawMessage(specJSON))
		if err != nil {
			return
		}
		// Validate must never panic — notably a GBM split with nil Left/Right must be
		// rejected (an error), not crash, so Provider.Predict can rely on it.
		_ = spec.Validate()
	})
}

// FuzzEvaluate asserts in-core model scoring (the non-expression kinds — logistic
// and gbm, whose evaluation is bounded by the spec/feature size) never panics and
// never returns a non-finite prediction. A huge intercept, an overflowing tree sum,
// or a NaN/Inf feature must produce an error, not a +Inf/NaN score that would poison
// downstream branching and the drift histogram. The expression kind is excluded on
// purpose: it runs operator-authored expr-lang with no wall-clock bound here (see
// FuzzParseSpec), which is an evaluation limitation, not a scoring-finiteness defect.
func FuzzEvaluate(f *testing.F) {
	seeds := []struct{ spec, features string }{
		{`{"kind":"logistic","intercept":-3,"coefficients":{"fico":0.005}}`, `{"fico":700}`},
		{`{"kind":"logistic","intercept":1e308,"coefficients":{"x":1e308}}`, `{"x":1e308}`},
		{`{"kind":"gbm","base":1e308,"trees":[{"leaf":true,"value":1e308},{"leaf":true,"value":1e308}]}`, `{}`},
		{`{"kind":"gbm","trees":[{"feature":"x","threshold":1,"left":{"leaf":true,"value":-1},"right":{"leaf":true,"value":1}}]}`, `{"x":2}`},
	}
	for _, s := range seeds {
		f.Add(s.spec, s.features)
	}
	f.Fuzz(func(t *testing.T, specJSON, featuresJSON string) {
		if !json.Valid([]byte(specJSON)) || !json.Valid([]byte(featuresJSON)) {
			return
		}
		spec, err := models.ParseSpec(json.RawMessage(specJSON))
		if err != nil || spec.Kind == models.KindExpression {
			return
		}
		if err := spec.Validate(); err != nil {
			return
		}
		var features map[string]any
		if err := json.Unmarshal([]byte(featuresJSON), &features); err != nil {
			return
		}
		pred, err := models.Evaluate(spec, features) // must never panic
		if err != nil {
			return
		}
		// INVARIANT: a successful prediction is always finite (no NaN/±Inf leaks).
		if math.IsNaN(pred.Score) || math.IsInf(pred.Score, 0) {
			t.Fatalf("Evaluate returned non-finite score %v for spec %q", pred.Score, specJSON)
		}
		if pred.Probability != nil && (math.IsNaN(*pred.Probability) || math.IsInf(*pred.Probability, 0)) {
			t.Fatalf("Evaluate returned non-finite probability %v for spec %q", *pred.Probability, specJSON)
		}
	})
}
