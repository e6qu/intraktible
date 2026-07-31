// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func validDatasetSpec() DatasetSpec {
	return DatasetSpec{
		Name: "risk", OwnerTeam: "risk-science", EntityType: "applicant",
		Features: []string{"income"},
		Label: LabelSpec{
			EventName: "outcome", Field: "bad", Kind: LabelBinary,
			PositiveValue: "true", HorizonHours: 24,
		},
		Purpose:            "model-development",
		ConsentRequirement: ConsentRequirement{Mode: ConsentNotRequired},
		RetentionDays:      30,
		Partitions:         PartitionSpec{TrainBPS: 7000, ValidationBPS: 1500, TestBPS: 1500},
	}
}

func TestPopulationRulesAndConsentPolicyAreExplicit(t *testing.T) {
	t.Parallel()
	spec := validDatasetSpec()
	spec.InclusionRules = []PopulationRule{{
		Name: "adult", Field: "age", Operator: PopulationGTE,
		Value: json.RawMessage(`18`), Reason: "product eligibility",
	}}
	spec.ExclusionRules = []PopulationRule{{
		Name: "test-account", Field: "environment", Operator: PopulationEquals,
		Value: json.RawMessage(`"test"`), Reason: "synthetic source record",
	}}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	eligible, reasons, err := PopulationDecision(
		json.RawMessage(`{"age":30,"environment":"production"}`),
		spec.InclusionRules, spec.ExclusionRules,
	)
	if err != nil || !eligible || len(reasons) != 0 {
		t.Fatalf("eligible=%v reasons=%v err=%v", eligible, reasons, err)
	}
	eligible, reasons, err = PopulationDecision(
		json.RawMessage(`{"age":17,"environment":"test"}`),
		spec.InclusionRules, spec.ExclusionRules,
	)
	if err != nil || eligible || len(reasons) != 2 ||
		reasons[0] != "adult" || reasons[1] != "test-account" {
		t.Fatalf("excluded=%v reasons=%v err=%v", !eligible, reasons, err)
	}

	spec.ConsentRequirement = ConsentRequirement{Mode: ConsentActive}
	if err := spec.Validate(); err == nil {
		t.Fatal("active consent policy without purpose was accepted")
	}
	spec.ConsentRequirement.Purpose = "model-development"
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestHashRowsIsOrderIndependent(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	left := DatasetRow{EntityID: "a", Features: map[string]float64{"x": 1}, ObservationAt: at, KnowledgeAt: at}
	right := DatasetRow{EntityID: "b", Features: map[string]float64{"x": 2}, ObservationAt: at, KnowledgeAt: at}
	first, _, err := HashRows([]DatasetRow{left, right})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := HashRows([]DatasetRow{right, left})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hashes differ: %s != %s", first, second)
	}
}

func TestNumericLabelPreservesCensoredDistinction(t *testing.T) {
	t.Parallel()
	spec := LabelSpec{Kind: LabelBinary, PositiveValue: `"yes"`}
	got, err := NumericLabel(spec, json.RawMessage(`"yes"`))
	if err != nil || got != 1 {
		t.Fatalf("label = %v, %v", got, err)
	}
}
