// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"math"
	"testing"
)

func TestBucket(t *testing.T) {
	cases := map[float64]int{-0.5: 0, 0: 0, 0.05: 0, 0.1: 1, 0.55: 5, 0.999: 9, 1.0: 9, 1.5: 9}
	for p, want := range cases {
		if got := bucket(p); got != want {
			t.Errorf("bucket(%v) = %d, want %d", p, got, want)
		}
	}
}

func TestPSI(t *testing.T) {
	base := Histogram{10, 10, 10, 10, 10, 10, 10, 10, 10, 10}

	// Identical distribution → ~0.
	if psi, ok := PSI(base, base); !ok || math.Abs(psi) > 1e-9 {
		t.Fatalf("identical PSI = %v (ok=%v), want ~0", psi, ok)
	}

	// A big shift → a large PSI well past the 0.25 "significant" threshold.
	shifted := Histogram{0, 0, 0, 0, 0, 0, 0, 0, 0, 100}
	psi, ok := PSI(base, shifted)
	if !ok || psi <= 0.25 {
		t.Fatalf("shifted PSI = %v (ok=%v), want > 0.25", psi, ok)
	}

	// Empty either side → not computable.
	if _, ok := PSI(Histogram{}, base); ok {
		t.Fatal("empty baseline should be non-computable")
	}
}
