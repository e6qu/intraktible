// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/context-layer/domain"
)

func TestDefineFeatureValidate(t *testing.T) {
	ok := []domain.DefineFeature{
		{Name: "txn_count_24h", EntityType: "customer", EventName: "transaction", Aggregation: "count", WindowHours: 24},
		{Name: "txn_sum_7d", EntityType: "customer", EventName: "transaction", Aggregation: "sum", Field: "amount", WindowHours: 168},
	}
	for i, c := range ok {
		if err := c.Validate(); err != nil {
			t.Fatalf("valid %d rejected: %v", i, err)
		}
	}
	bad := []domain.DefineFeature{
		{EntityType: "customer", EventName: "t", Aggregation: "count", WindowHours: 24},             // no name
		{Name: "f", EntityType: "customer", EventName: "t", Aggregation: "median", WindowHours: 24}, // bad agg
		{Name: "f", EntityType: "customer", EventName: "t", Aggregation: "sum", WindowHours: 24},    // sum w/o field
		{Name: "f", EntityType: "customer", EventName: "t", Aggregation: "count", WindowHours: 0},   // bad window
		{Name: "f", EntityType: "customer", Aggregation: "count", WindowHours: 24},                  // no event
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("bad %d accepted: %+v", i, c)
		}
	}
}

func TestComputeCount(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	spec := domain.FeatureSpec{EventName: "transaction", Aggregation: domain.AggCount, Window: 24 * time.Hour}
	events := []domain.FeatureInput{
		{EventName: "transaction", OccurredAt: now.Add(-1 * time.Hour)},  // in window
		{EventName: "transaction", OccurredAt: now.Add(-23 * time.Hour)}, // in window
		{EventName: "transaction", OccurredAt: now.Add(-30 * time.Hour)}, // out of window
		{EventName: "login", OccurredAt: now.Add(-1 * time.Hour)},        // wrong event
		{EventName: "transaction", OccurredAt: now.Add(1 * time.Hour)},   // future, excluded
	}
	v, err := domain.Compute(spec, events, now)
	if err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Fatalf("count = %v, want 2", v)
	}
}

func TestComputeSum(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	spec := domain.FeatureSpec{EventName: "transaction", Aggregation: domain.AggSum, Field: "amount", Window: 24 * time.Hour}
	events := []domain.FeatureInput{
		{EventName: "transaction", Data: json.RawMessage(`{"amount":100}`), OccurredAt: now.Add(-1 * time.Hour)},
		{EventName: "transaction", Data: json.RawMessage(`{"amount":50.5}`), OccurredAt: now.Add(-2 * time.Hour)},
		{EventName: "transaction", Data: json.RawMessage(`{"other":1}`), OccurredAt: now.Add(-3 * time.Hour)}, // field absent -> 0
		{EventName: "transaction", Data: json.RawMessage(`{"amount":999}`), OccurredAt: now.Add(-48 * time.Hour)},
	}
	v, err := domain.Compute(spec, events, now)
	if err != nil {
		t.Fatal(err)
	}
	if v != 150.5 {
		t.Fatalf("sum = %v, want 150.5", v)
	}
}

func TestComputeSumNonNumericFails(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	spec := domain.FeatureSpec{EventName: "transaction", Aggregation: domain.AggSum, Field: "amount", Window: 24 * time.Hour}
	events := []domain.FeatureInput{
		{EventName: "transaction", Data: json.RawMessage(`{"amount":"lots"}`), OccurredAt: now.Add(-1 * time.Hour)},
	}
	if _, err := domain.Compute(spec, events, now); err == nil {
		t.Fatal("non-numeric field should fail loudly")
	}
}

// An invalid aggregation must error even when NO events match (the check is a
// precondition, not buried in the per-event loop where empty input would skip it).
func TestComputeRejectsUnknownAggregationOnEmptyInput(t *testing.T) {
	spec := domain.FeatureSpec{EventName: "transaction", Aggregation: "median", Window: 24 * time.Hour}
	if _, err := domain.Compute(spec, nil, time.Now()); err == nil {
		t.Fatal("an unknown aggregation must error even with no matching events")
	}
}

func TestComputeAggregations(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	evs := []domain.FeatureInput{
		{EventName: "txn", Data: json.RawMessage(`{"amount":100,"cur":"USD"}`), OccurredAt: now.Add(-1 * time.Hour)}, // newest
		{EventName: "txn", Data: json.RawMessage(`{"amount":40,"cur":"EUR"}`), OccurredAt: now.Add(-5 * time.Hour)},
		{EventName: "txn", Data: json.RawMessage(`{"amount":10,"cur":"USD"}`), OccurredAt: now.Add(-10 * time.Hour)},  // oldest in-window
		{EventName: "txn", Data: json.RawMessage(`{"amount":999,"cur":"GBP"}`), OccurredAt: now.Add(-48 * time.Hour)}, // out of window
	}
	spec := func(a domain.Aggregation) domain.FeatureSpec {
		return domain.FeatureSpec{EventName: "txn", Aggregation: a, Field: "amount", Window: 24 * time.Hour}
	}
	cases := []struct {
		agg  domain.Aggregation
		want float64
	}{
		{domain.AggAvg, 50}, // (100+40+10)/3
		{domain.AggMin, 10},
		{domain.AggMax, 100},
		{domain.AggLast, 100}, // most recent event's amount
		{domain.AggFirst, 10}, // oldest in-window event's amount
	}
	for _, c := range cases {
		got, err := domain.Compute(spec(c.agg), evs, now)
		if err != nil {
			t.Fatalf("%s: %v", c.agg, err)
		}
		if got != c.want {
			t.Fatalf("%s = %v, want %v", c.agg, got, c.want)
		}
	}
	// count_distinct over the "cur" field: USD, EUR (GBP is out of window) = 2.
	cd := domain.FeatureSpec{EventName: "txn", Aggregation: domain.AggCountDistinct, Field: "cur", Window: 24 * time.Hour}
	if got, err := domain.Compute(cd, evs, now); err != nil || got != 2 {
		t.Fatalf("count_distinct = %v (err %v), want 2", got, err)
	}
}

// Point-in-time: computing as of a past instant sees only the events that had
// occurred by then, so a feature is reproducible for any historical decision.
func TestComputePointInTime(t *testing.T) {
	base := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	spec := domain.FeatureSpec{EventName: "txn", Aggregation: domain.AggCount, Window: 24 * time.Hour}
	evs := []domain.FeatureInput{
		{EventName: "txn", OccurredAt: base.Add(-2 * time.Hour)},
		{EventName: "txn", OccurredAt: base.Add(-1 * time.Hour)},
		{EventName: "txn", OccurredAt: base.Add(30 * time.Minute)}, // after the as-of instant
	}
	// As of `base`, only the two earlier events count; the later one is "in the future".
	if v, _ := domain.Compute(spec, evs, base); v != 2 {
		t.Fatalf("as-of count = %v, want 2 (future event excluded)", v)
	}
	// An hour later, all three are in-window and past.
	if v, _ := domain.Compute(spec, evs, base.Add(time.Hour)); v != 3 {
		t.Fatalf("later count = %v, want 3", v)
	}
}

// ComputeDetailed exposes the lineage a materialized cache needs: event count and the
// oldest in-window instant (whose +window is when the value next changes).
func TestComputeDetailedLineage(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	oldest := now.Add(-20 * time.Hour)
	spec := domain.FeatureSpec{EventName: "txn", Aggregation: domain.AggCount, Window: 24 * time.Hour}
	evs := []domain.FeatureInput{
		{EventName: "txn", OccurredAt: now.Add(-1 * time.Hour)},
		{EventName: "txn", OccurredAt: oldest},
	}
	res, err := domain.ComputeDetailed(spec, evs, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Value != 2 || res.EventCount != 2 || !res.HasInWindow || !res.OldestInWin.Equal(oldest) {
		t.Fatalf("lineage = %+v", res)
	}
}
