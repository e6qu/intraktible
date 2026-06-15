// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/context-layer/domain"
)

func TestRecordEntityValidate(t *testing.T) {
	if err := (domain.RecordEntity{EntityType: "customer", EntityID: "c1", Attributes: json.RawMessage(`{"tier":"gold"}`)}).Validate(); err != nil {
		t.Fatalf("valid entity rejected: %v", err)
	}
	bad := []domain.RecordEntity{
		{EntityID: "c1"},         // no type
		{EntityType: "customer"}, // no id
		{EntityType: "customer", EntityID: "c1", Attributes: []byte(`[1,2]`)},  // array, not object
		{EntityType: "customer", EntityID: "c1", Attributes: []byte(`"nope"`)}, // scalar
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d: expected error for %+v", i, c)
		}
	}
}

func TestRecordEventValidate(t *testing.T) {
	if err := (domain.RecordEvent{EntityType: "customer", EntityID: "c1", EventName: "transaction"}).Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	if err := (domain.RecordEvent{EntityType: "customer", EntityID: "c1"}).Validate(); err == nil {
		t.Fatal("event without name should be rejected")
	}
	if err := (domain.RecordEvent{EntityType: "customer", EntityID: "c1", EventName: "t", Data: []byte(`5`)}).Validate(); err == nil {
		t.Fatal("non-object data should be rejected")
	}
}

func TestMergeAttributes(t *testing.T) {
	merged, err := domain.MergeAttributes(
		json.RawMessage(`{"tier":"silver","country":"US"}`),
		json.RawMessage(`{"tier":"gold","kyc":true}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatal(err)
	}
	// patch wins on overlap, base retained where patch is silent, new keys added.
	if got["tier"] != "gold" || got["country"] != "US" || got["kyc"] != true {
		t.Fatalf("merged = %v", got)
	}
}

func TestMergeAttributesEmptyBase(t *testing.T) {
	merged, err := domain.MergeAttributes(nil, json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(merged) != `{"a":1}` {
		t.Fatalf("merged = %s", merged)
	}
}
