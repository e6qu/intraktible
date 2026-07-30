// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func enterpriseType() CaseTypeDefinition {
	return CaseTypeDefinition{
		Key: "enhanced_due_diligence", Name: "Enhanced due diligence",
		InitialState: "intake",
		Fields: []FieldDefinition{
			{Key: "country", Label: "Country", Kind: FieldString, Required: true},
			{Key: "amount", Label: "Amount", Kind: FieldNumber},
		},
		Transitions: []Transition{
			{From: "intake", To: "investigating", Roles: []string{"operator"}},
			{From: "investigating", To: "resolved", Roles: []string{"operator", "approver"}},
		},
		Dispositions: []DispositionDefinition{{
			Key: "clear", Label: "Clear", ReasonCodes: []string{"verified"},
			TerminalState: "resolved",
		}},
		Priorities: []Priority{PriorityNormal, PriorityHigh},
		Calendar: ServiceCalendar{
			Timezone: "UTC", Weekdays: []int{1, 2, 3, 4, 5},
			StartHour: 9, EndHour: 17, SLAHours: 8, Escalation: 2,
		},
		Evidence: []EvidenceRequirement{{
			Key: "registry_extract", Label: "Registry extract",
			Kinds: []string{"attachment"}, Required: true,
		}},
		Layouts: []RoleLayout{{Role: "operator", Sections: []string{"summary", "evidence"}, Editable: []string{"country"}}},
	}
}

func TestCaseTypeDefinitionAndContext(t *testing.T) {
	definition := enterpriseType()
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := definition.ValidateContext(json.RawMessage(`{"country":"RO","amount":42}`)); err != nil {
		t.Fatal(err)
	}
	if err := definition.ValidateContext(json.RawMessage(`{"amount":42}`)); err == nil {
		t.Fatal("missing required field was accepted")
	}
	if err := definition.ValidateContext(json.RawMessage(`{"country":42}`)); err == nil {
		t.Fatal("wrong field kind was accepted")
	}
	if !definition.CanTransition("intake", "investigating", "operator") {
		t.Fatal("configured transition should be permitted")
	}
	if definition.CanTransition("intake", "resolved", "operator") {
		t.Fatal("unconfigured transition should be refused")
	}
}

func TestBusinessDeadlineSkipsClosedHoursAndWeekend(t *testing.T) {
	definition := enterpriseType()
	start := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC) // Friday
	deadline, err := BusinessDeadline(start, definition.Calendar, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// One Friday hour plus seven Monday hours.
	want := time.Date(2026, 8, 3, 15, 0, 0, 0, time.UTC)
	if !deadline.Equal(want) {
		t.Fatalf("deadline = %s, want %s", deadline, want)
	}
}

func TestRouteIsDeterministicCapacityAndConflictAware(t *testing.T) {
	queue := QueueDefinition{
		Key: "edd", Name: "EDD", CaseTypes: []string{"enhanced_due_diligence"},
		Priorities: []Priority{PriorityHigh}, Jurisdictions: []string{"eu"},
		RequiredSkills: []string{"aml"}, Capacity: 100,
		ConflictContextKeys: []string{"customer_id"},
	}
	input := RoutingInput{
		CaseID: "case-1", CaseType: "enhanced_due_diligence",
		Priority: PriorityHigh, Jurisdiction: "eu",
		Context: map[string]any{"customer_id": "customer-9"},
		Queues:  []QueueDefinition{queue},
		Reviewers: []ReviewerProfile{
			{Actor: "zoe", Skills: []string{"aml"}, Jurisdictions: []string{"eu"}, Capacity: 2, Active: true},
			{Actor: "amy", Skills: []string{"aml"}, Jurisdictions: []string{"eu"}, Capacity: 2, Active: true, Conflicts: []string{"customer-9"}},
			{Actor: "bob", Skills: []string{"aml"}, Jurisdictions: []string{"eu"}, Capacity: 1, Active: true},
		},
		OpenByActor: map[string]int{"zoe": 0, "amy": 0, "bob": 1},
	}
	got, err := Route(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Queue != "edd" || got.Assignee != "zoe" {
		t.Fatalf("route = %+v, want edd/zoe", got)
	}
	again, err := Route(input)
	if err != nil || again != got {
		t.Fatalf("repeat route = %+v, %v; want stable %+v", again, err, got)
	}
}
