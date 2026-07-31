// SPDX-License-Identifier: AGPL-3.0-or-later

// Package stats holds shared statistical primitives used across components.
package stats

import "math"

// Wilson returns the 95% Wilson score confidence interval for a binomial
// proportion; an empty sample yields the zero interval.
func Wilson(successes, total int) (lower, upper float64) {
	if total == 0 {
		return 0, 0
	}
	const z = 1.959963984540054
	n := float64(total)
	p := float64(successes) / n
	denominator := 1 + z*z/n
	center := (p + z*z/(2*n)) / denominator
	margin := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n)) / denominator
	return math.Max(0, center-margin), math.Min(1, center+margin)
}
