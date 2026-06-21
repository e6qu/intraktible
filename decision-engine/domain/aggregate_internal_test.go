// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"math"
	"testing"
)

// FuzzAggregateValues asserts the decision-table COLLECT reducer is robust and
// order-stable: it must never panic, and for sum/min/max the result must not
// depend on the order of the inputs (a NaN would otherwise make min/max
// order-dependent). A non-finite input or overflow is rejected, never returned.
func FuzzAggregateValues(f *testing.F) {
	f.Add("sum", `[1,2,3]`)
	f.Add("min", `[3.5,1.0,2.0]`)
	f.Add("max", `[1e308,1e308]`)
	f.Add("count", `["a",2,true]`)
	f.Add("sum", `[1e308,1e308,1e308]`)
	f.Fuzz(func(t *testing.T, agg, valsJSON string) {
		if !json.Valid([]byte(valsJSON)) {
			return
		}
		var vals []any
		if err := json.Unmarshal([]byte(valsJSON), &vals); err != nil {
			return
		}
		got, err := aggregateValues(agg, vals)
		if err != nil {
			return // non-numeric / non-finite / unknown agg — loud, not a crash
		}
		// For the order-sensitive numeric reducers, the result must be invariant
		// under input order, and finite on success.
		if agg == "sum" || agg == "min" || agg == "max" {
			rev := make([]any, len(vals))
			for i, v := range vals {
				rev[len(vals)-1-i] = v
			}
			got2, err2 := aggregateValues(agg, rev)
			if err2 != nil {
				t.Fatalf("%s succeeded forward but errored reversed: %v", agg, err2)
			}
			fg, _ := toFloat(got)
			fr, _ := toFloat(got2)
			if fg != fr {
				t.Fatalf("%s is order-dependent: %v vs %v", agg, got, got2)
			}
			if math.IsNaN(fg) || math.IsInf(fg, 0) {
				t.Fatalf("%s returned a non-finite result %v", agg, got)
			}
		}
	})
}
