// SPDX-License-Identifier: AGPL-3.0-or-later

package agents

import (
	"math"
	"testing"
)

// FuzzParsePricing asserts the INTRAKTIBLE_AI_PRICES parser never panics on an
// arbitrary value (it does string-cut math and float parsing) and that any pricing
// it accepts holds only finite, non-negative rates — a NaN/Inf or negative rate
// would silently corrupt cost reporting downstream.
func FuzzParsePricing(f *testing.F) {
	for _, s := range []string{
		`gpt-4o=2.5/10,claude-opus-4=15/75`,
		``,
		`=`,
		`a=`,
		`a=1`,
		`a=1/`,
		`a=/2`,
		`a=1/2,`,
		`,,,`,
		`a=NaN/1`,
		`a=Inf/2`,
		`a=-1/2`,
		`a=1e400/1`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParsePricing(s) // must not panic
		if err != nil {
			return
		}
		for model, pr := range p {
			if isBadRate(pr.InputPerMTok) || isBadRate(pr.OutputPerMTok) {
				t.Fatalf("ParsePricing(%q) accepted bad rate for %q: %+v", s, model, pr)
			}
		}
	})
}

func isBadRate(r float64) bool {
	return r < 0 || math.IsNaN(r) || math.IsInf(r, 0)
}
