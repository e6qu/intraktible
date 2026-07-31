// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func schemaFixture() SchemaSpec {
	return SchemaSpec{
		Ref:           SchemaRef{Kind: SchemaKindEntity, EntityType: "applicant"},
		OwnerTeam:     "risk-data",
		Purposes:      []string{"underwriting"},
		Compatibility: CompatibilityBackward,
		Fields: []FieldSpec{
			{Name: "income", Type: FieldNumber, Required: true, Classification: ClassificationConfidential},
			{Name: "region", Type: FieldString, Classification: ClassificationInternal},
		},
		Quality: QualityContract{Action: QualityBlock, CompletenessMin: 0.5},
	}
}

func TestValidateDocumentReturnsStableViolations(t *testing.T) {
	t.Parallel()
	spec := schemaFixture()
	got, err := ValidateDocument(spec, json.RawMessage(`{"income":"high","unknown":1}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Violation{
		{Code: "type", Field: "income", Message: `field "income" must be number`},
		{Code: "additional_property", Field: "unknown", Message: `undeclared field "unknown" is not allowed`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("violations = %#v, want %#v", got, want)
	}
}

func TestCompatibilityBreaksRejectsRequiredAddition(t *testing.T) {
	t.Parallel()
	previous := schemaFixture()
	next := previous
	next.Fields = append(append([]FieldSpec(nil), previous.Fields...), FieldSpec{
		Name: "age", Type: FieldInteger, Required: true, Classification: ClassificationRestricted,
	})
	got := CompatibilityBreaks(previous, next)
	want := []string{`backward: new field "age" is required`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("breaks = %#v, want %#v", got, want)
	}
}

func TestCompatibilityBreaksDetectsValidityRelationshipAndQualityChanges(t *testing.T) {
	t.Parallel()
	previous := schemaFixture()
	maximum := 100.0
	next := previous
	next.Compatibility = CompatibilityFull
	next.Fields = append([]FieldSpec(nil), previous.Fields...)
	next.Fields[0].Maximum = &maximum
	next.Relationships = []Relationship{{
		Field: "region", TargetEntityType: "region",
	}}
	next.Quality.FreshnessSeconds = 60
	got := CompatibilityBreaks(previous, next)
	want := []string{
		`backward: field "income" validity or governance contract changed`,
		"backward: quality contract changed",
		"backward: relationship contract changed",
		`forward: field "income" validity or governance contract changed`,
		"forward: quality contract changed",
		"forward: relationship contract changed",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("breaks = %#v, want %#v", got, want)
	}
}

func TestHashSchemaIsDeterministic(t *testing.T) {
	t.Parallel()
	spec := schemaFixture()
	first, err := HashSchema(spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashSchema(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("hashes = %q and %q", first, second)
	}
}

func TestValidateDocumentEnforcesNullEnumRangePatternLengthAndRelationshipPresence(t *testing.T) {
	t.Parallel()
	minimum, maximum := 0.0, 1.0
	minLength, maxLength := 2, 4
	spec := SchemaSpec{
		Ref:       SchemaRef{Kind: SchemaKindEntity, EntityType: "applicant"},
		OwnerTeam: "risk-data", Purposes: []string{"underwriting"},
		Compatibility: CompatibilityBackward,
		Fields: []FieldSpec{
			{
				Name: "status", Type: FieldString, Required: true,
				Classification: ClassificationInternal,
				Enum:           []json.RawMessage{json.RawMessage(`"active"`), json.RawMessage(`"inactive"`)},
			},
			{
				Name: "score", Type: FieldNumber, Classification: ClassificationInternal,
				Minimum: &minimum, Maximum: &maximum,
			},
			{
				Name: "code", Type: FieldString, Classification: ClassificationInternal,
				Pattern: `^[A-Z]+$`, MinLength: &minLength, MaxLength: &maxLength,
			},
			{
				Name: "parent_id", Type: FieldString, Nullable: true,
				Classification: ClassificationConfidential,
			},
		},
		Relationships: []Relationship{{
			Field: "parent_id", TargetEntityType: "applicant", Required: true,
		}},
		Quality: QualityContract{Action: QualityRefer},
	}
	violations, err := ValidateDocument(
		spec, json.RawMessage(`{"status":"paused","score":2,"code":"x","parent_id":null}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, violation := range violations {
		codes = append(codes, violation.Code)
	}
	want := []string{"enum", "maximum", "min_length", "pattern", "relationship_required"}
	if !reflect.DeepEqual(codes, want) {
		t.Fatalf("violation codes = %v, want %v", codes, want)
	}
}

func TestEntityUniqueTupleAndRelationshipsAreCanonical(t *testing.T) {
	t.Parallel()
	spec := schemaFixture()
	spec.Fields[0].Identifier = true
	spec.Quality.UniqueFields = []string{"income"}
	spec.Fields = append(spec.Fields, FieldSpec{
		Name: "parent_id", Type: FieldString, Classification: ClassificationInternal,
	})
	spec.Relationships = []Relationship{{
		Field: "parent_id", TargetEntityType: "applicant",
	}}
	first, present, err := UniqueTupleHash(
		spec, json.RawMessage(`{"income":42,"parent_id":"parent-1","region":"EU"}`),
	)
	if err != nil || !present {
		t.Fatalf("first unique tuple = %q/%v, err %v", first, present, err)
	}
	second, present, err := UniqueTupleHash(
		spec, json.RawMessage(`{"region":"EU","parent_id":"parent-1","income":42}`),
	)
	if err != nil || !present || first != second {
		t.Fatalf("second unique tuple = %q/%v, err %v; first %q", second, present, err, first)
	}
	relationships, err := RelationshipValues(
		spec, json.RawMessage(`{"income":42,"parent_id":"parent-1"}`),
	)
	if err != nil || !reflect.DeepEqual(relationships, []RelationshipValue{{
		Field: "parent_id", TargetEntityType: "applicant", TargetEntityID: "parent-1",
	}}) {
		t.Fatalf("relationships = %+v, err %v", relationships, err)
	}
}

func TestUniqueFieldsAreRequiredEntityOnly(t *testing.T) {
	t.Parallel()
	spec := schemaFixture()
	spec.Quality.UniqueFields = []string{"region"}
	if err := spec.Validate(); err == nil {
		t.Fatal("optional unique field should be rejected")
	}
	spec.Fields[1].Required = true
	spec.Ref = SchemaRef{
		Kind: SchemaKindEvent, EntityType: "applicant", EventName: "application",
	}
	if err := spec.Validate(); err == nil {
		t.Fatal("event unique_fields should be rejected in favor of event_id")
	}
}

func TestApprovedStaleRequiresAnEventFreshnessContract(t *testing.T) {
	t.Parallel()
	spec := schemaFixture()
	spec.Quality.Action = QualityApprovedStale
	if err := spec.Validate(); err == nil ||
		!strings.Contains(err.Error(), "only valid for event schemas") {
		t.Fatalf("entity approved_stale validation = %v", err)
	}
	spec.Ref = SchemaRef{
		Kind: SchemaKindEvent, EntityType: "applicant", EventName: "application",
	}
	if err := spec.Validate(); err == nil ||
		!strings.Contains(err.Error(), "freshness contract") {
		t.Fatalf("event without freshness validation = %v", err)
	}
	spec.Quality.FreshnessSeconds = 60
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
}
