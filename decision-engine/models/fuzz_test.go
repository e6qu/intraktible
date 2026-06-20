// SPDX-License-Identifier: AGPL-3.0-or-later

package models_test

import (
	"encoding/json"
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
