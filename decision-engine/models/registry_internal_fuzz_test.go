// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"math"
	"testing"
)

// FuzzParseExternalPrediction asserts the external-model response parser — the trust
// boundary for an attacker-influenced HTTP body — never panics and never accepts a
// non-finite score or an out-of-[0,1] (or non-finite) probability. Malformed JSON
// must error, not crash; an accepted prediction must be safe to record and branch on.
func FuzzParseExternalPrediction(f *testing.F) {
	seeds := []string{
		`{"score":0.5}`,
		`{"score":0.5,"probability":0.9}`,
		`{"score":1e308}`,
		`{"score":0,"probability":2}`,
		`{"score":"NaN"}`,
		`not json`,
		``,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		pred, err := parseExternalPrediction([]byte(body)) // must never panic
		if err != nil {
			return
		}
		if math.IsNaN(pred.Score) || math.IsInf(pred.Score, 0) {
			t.Fatalf("accepted non-finite score %v from %q", pred.Score, body)
		}
		if pred.Probability != nil {
			p := *pred.Probability
			if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
				t.Fatalf("accepted out-of-range probability %v from %q", p, body)
			}
		}
	})
}
