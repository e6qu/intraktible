// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/e6qu/intraktible/context-layer/domain"
)

// FuzzFeatureCompute asserts the feature fold is robust over arbitrary aggregation,
// field name, and event data: Compute must never panic, and on success it must
// return a finite value — an overflowed/NaN sum is an error, never a poisoned
// feature that would silently corrupt downstream rule evaluation.
func FuzzFeatureCompute(f *testing.F) {
	f.Add("count", "amount", `[{"amount":1.5},{"amount":2}]`)
	f.Add("sum", "amount", `[{"amount":1e308},{"amount":1e308}]`)
	f.Add("sum", "amount", `[{"amount":"not-a-number"}]`)
	f.Add("median", "x", `[{"x":1}]`)
	f.Add("sum", "missing", `[{"other":1}]`)
	now := time.Unix(1_700_000_000, 0).UTC()
	f.Fuzz(func(t *testing.T, agg, field, eventsJSON string) {
		if !json.Valid([]byte(eventsJSON)) {
			return
		}
		var rows []json.RawMessage
		if err := json.Unmarshal([]byte(eventsJSON), &rows); err != nil {
			return
		}
		evs := make([]domain.FeatureInput, 0, len(rows))
		for _, r := range rows {
			evs = append(evs, domain.FeatureInput{EventName: "e", Data: r, OccurredAt: now})
		}
		spec := domain.FeatureSpec{
			EventName:   "e",
			Aggregation: domain.Aggregation(agg),
			Field:       field,
			Window:      time.Hour,
		}
		v, err := domain.Compute(spec, evs, now)
		if err != nil {
			return // bad aggregation / non-numeric field / overflow — all loud, not a crash
		}
		if math.IsInf(v, 0) || math.IsNaN(v) {
			t.Fatalf("Compute returned a non-finite value %v with no error", v)
		}
	})
}
