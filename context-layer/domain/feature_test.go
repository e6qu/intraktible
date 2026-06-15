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
