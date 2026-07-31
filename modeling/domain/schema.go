// SPDX-License-Identifier: AGPL-3.0-or-later

// Package domain contains the pure modeling and data-governance rules. It owns
// no clocks, logs, stores, workers, or network clients.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// SchemaKind identifies the source record a schema governs.
type SchemaKind string

const (
	SchemaKindEntity SchemaKind = "entity"
	SchemaKindEvent  SchemaKind = "event"
)

// FieldType is one supported JSON field type.
type FieldType string

const (
	FieldString  FieldType = "string"
	FieldNumber  FieldType = "number"
	FieldInteger FieldType = "integer"
	FieldBoolean FieldType = "boolean"
	FieldObject  FieldType = "object"
	FieldArray   FieldType = "array"
)

// Classification is a field's data-sensitivity tier.
type Classification string

const (
	ClassificationPublic       Classification = "public"
	ClassificationInternal     Classification = "internal"
	ClassificationConfidential Classification = "confidential"
	ClassificationRestricted   Classification = "restricted"
)

// CompatibilityMode controls which changes a new version may make relative to
// the latest approved version.
type CompatibilityMode string

const (
	CompatibilityBackward CompatibilityMode = "backward"
	CompatibilityForward  CompatibilityMode = "forward"
	CompatibilityFull     CompatibilityMode = "full"
	CompatibilityNone     CompatibilityMode = "none"
)

// QualityAction is the explicit ingestion behavior when a record violates its
// schema's quality contract.
type QualityAction string

const (
	QualityBlock         QualityAction = "block"
	QualityRefer         QualityAction = "refer"
	QualityWarn          QualityAction = "warn"
	QualityApprovedStale QualityAction = "approved_stale"
)

// SchemaRef uniquely identifies one governed source within a tenant.
type SchemaRef struct {
	Kind       SchemaKind `json:"kind"`
	EntityType string     `json:"entity_type"`
	EventName  string     `json:"event_name,omitempty"`
}

// Validate checks the source coordinates.
func (r SchemaRef) Validate() error {
	switch r.Kind {
	case SchemaKindEntity:
		if strings.TrimSpace(r.EventName) != "" {
			return errors.New("modeling: entity schema must not set event_name")
		}
	case SchemaKindEvent:
		if strings.TrimSpace(r.EventName) == "" {
			return errors.New("modeling: event schema requires event_name")
		}
	default:
		return fmt.Errorf("modeling: unsupported schema kind %q", r.Kind)
	}
	if strings.TrimSpace(r.EntityType) == "" {
		return errors.New("modeling: entity_type is required")
	}
	if strings.Contains(r.EntityType, "/") || strings.Contains(r.EventName, "/") {
		return errors.New("modeling: schema coordinates must not contain '/'")
	}
	return nil
}

// Key is a stable, tenant-local key for projections and unique claims.
func (r SchemaRef) Key() string {
	if r.Kind == SchemaKindEvent {
		return "event/" + r.EntityType + "/" + r.EventName
	}
	return "entity/" + r.EntityType
}

// FieldSpec declares one top-level JSON field.
type FieldSpec struct {
	Name           string            `json:"name"`
	Type           FieldType         `json:"type"`
	Required       bool              `json:"required,omitempty"`
	Nullable       bool              `json:"nullable,omitempty"`
	Identifier     bool              `json:"identifier,omitempty"`
	Classification Classification    `json:"classification"`
	Description    string            `json:"description,omitempty"`
	Enum           []json.RawMessage `json:"enum,omitempty"`
	Pattern        string            `json:"pattern,omitempty"`
	Minimum        *float64          `json:"minimum,omitempty"`
	Maximum        *float64          `json:"maximum,omitempty"`
	MinLength      *int              `json:"min_length,omitempty"`
	MaxLength      *int              `json:"max_length,omitempty"`
}

// Relationship declares a typed source-to-source relationship.
type Relationship struct {
	Field            string `json:"field"`
	TargetEntityType string `json:"target_entity_type"`
	Required         bool   `json:"required,omitempty"`
}

// QualityContract declares record-level quality expectations. CompletenessMin
// is the required ratio of populated declared fields. UniqueFields are enforced
// by the Context append boundary through durable tenant-scoped claims, while
// FreshnessSeconds is evaluated by the imperative quality monitor.
type QualityContract struct {
	Action           QualityAction `json:"action"`
	CompletenessMin  float64       `json:"completeness_min,omitempty"`
	UniqueFields     []string      `json:"unique_fields,omitempty"`
	FreshnessSeconds int64         `json:"freshness_seconds,omitempty"`
}

// SchemaSpec is one immutable version of a source contract.
type SchemaSpec struct {
	Ref                  SchemaRef         `json:"ref"`
	Description          string            `json:"description,omitempty"`
	OwnerTeam            string            `json:"owner_team"`
	Purposes             []string          `json:"purposes"`
	Compatibility        CompatibilityMode `json:"compatibility"`
	AdditionalProperties bool              `json:"additional_properties"`
	Fields               []FieldSpec       `json:"fields"`
	Relationships        []Relationship    `json:"relationships,omitempty"`
	Quality              QualityContract   `json:"quality"`
}

// Validate checks one schema definition without consulting external state.
func (s SchemaSpec) Validate() error {
	if err := s.Ref.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(s.OwnerTeam) == "" {
		return errors.New("modeling: owner_team is required")
	}
	if len(s.Purposes) == 0 {
		return errors.New("modeling: at least one permissible purpose is required")
	}
	seenPurpose := map[string]bool{}
	for _, purpose := range s.Purposes {
		purpose = strings.TrimSpace(purpose)
		if purpose == "" {
			return errors.New("modeling: purposes must not contain an empty value")
		}
		if seenPurpose[purpose] {
			return fmt.Errorf("modeling: duplicate purpose %q", purpose)
		}
		seenPurpose[purpose] = true
	}
	switch s.Compatibility {
	case CompatibilityBackward, CompatibilityForward, CompatibilityFull, CompatibilityNone:
	default:
		return fmt.Errorf("modeling: unsupported compatibility mode %q", s.Compatibility)
	}
	if len(s.Fields) == 0 {
		return errors.New("modeling: at least one field is required")
	}
	fields := make(map[string]FieldSpec, len(s.Fields))
	for _, field := range s.Fields {
		if err := field.validate(); err != nil {
			return err
		}
		if _, exists := fields[field.Name]; exists {
			return fmt.Errorf("modeling: duplicate field %q", field.Name)
		}
		fields[field.Name] = field
	}
	relationships := map[string]bool{}
	for _, relationship := range s.Relationships {
		if strings.TrimSpace(relationship.Field) == "" ||
			strings.TrimSpace(relationship.TargetEntityType) == "" {
			return errors.New("modeling: relationship field and target_entity_type are required")
		}
		if _, ok := fields[relationship.Field]; !ok {
			return fmt.Errorf("modeling: relationship field %q is not declared", relationship.Field)
		}
		if fields[relationship.Field].Type != FieldString {
			return fmt.Errorf("modeling: relationship field %q must be a string entity id", relationship.Field)
		}
		if relationships[relationship.Field] {
			return fmt.Errorf("modeling: duplicate relationship for field %q", relationship.Field)
		}
		relationships[relationship.Field] = true
	}
	switch s.Quality.Action {
	case QualityBlock, QualityRefer, QualityWarn, QualityApprovedStale:
	default:
		return fmt.Errorf("modeling: unsupported quality action %q", s.Quality.Action)
	}
	if math.IsNaN(s.Quality.CompletenessMin) || math.IsInf(s.Quality.CompletenessMin, 0) ||
		s.Quality.CompletenessMin < 0 || s.Quality.CompletenessMin > 1 {
		return errors.New("modeling: completeness_min must be finite and between 0 and 1")
	}
	if s.Quality.FreshnessSeconds < 0 {
		return errors.New("modeling: freshness_seconds must not be negative")
	}
	if s.Quality.Action == QualityApprovedStale &&
		(s.Ref.Kind != SchemaKindEvent || s.Quality.FreshnessSeconds == 0) {
		return errors.New(
			"modeling: approved_stale is only valid for event schemas with a freshness contract",
		)
	}
	unique := map[string]bool{}
	if s.Ref.Kind == SchemaKindEvent && len(s.Quality.UniqueFields) > 0 {
		return errors.New("modeling: event uniqueness uses event_id; unique_fields are entity-only")
	}
	for _, name := range s.Quality.UniqueFields {
		field, ok := fields[name]
		if !ok {
			return fmt.Errorf("modeling: unique field %q is not declared", name)
		}
		if !field.Required || field.Nullable {
			return fmt.Errorf("modeling: unique field %q must be required and non-nullable", name)
		}
		if unique[name] {
			return fmt.Errorf("modeling: duplicate unique field %q", name)
		}
		unique[name] = true
	}
	return nil
}

func (f FieldSpec) validate() error {
	if strings.TrimSpace(f.Name) == "" {
		return errors.New("modeling: field name is required")
	}
	if strings.Contains(f.Name, "/") {
		return fmt.Errorf("modeling: field %q must not contain '/'", f.Name)
	}
	switch f.Type {
	case FieldString, FieldNumber, FieldInteger, FieldBoolean, FieldObject, FieldArray:
	default:
		return fmt.Errorf("modeling: field %q has unsupported type %q", f.Name, f.Type)
	}
	switch f.Classification {
	case ClassificationPublic, ClassificationInternal, ClassificationConfidential, ClassificationRestricted:
	default:
		return fmt.Errorf("modeling: field %q has unsupported classification %q", f.Name, f.Classification)
	}
	if f.Pattern != "" {
		if f.Type != FieldString {
			return fmt.Errorf("modeling: field %q pattern is only valid for strings", f.Name)
		}
		if _, err := regexp.Compile(f.Pattern); err != nil {
			return fmt.Errorf("modeling: field %q has invalid pattern: %w", f.Name, err)
		}
	}
	if f.Minimum != nil || f.Maximum != nil {
		if f.Type != FieldNumber && f.Type != FieldInteger {
			return fmt.Errorf("modeling: field %q numeric bounds require a number or integer", f.Name)
		}
		if (f.Minimum != nil && (math.IsNaN(*f.Minimum) || math.IsInf(*f.Minimum, 0))) ||
			(f.Maximum != nil && (math.IsNaN(*f.Maximum) || math.IsInf(*f.Maximum, 0))) {
			return fmt.Errorf("modeling: field %q numeric bounds must be finite", f.Name)
		}
		if f.Minimum != nil && f.Maximum != nil && *f.Minimum > *f.Maximum {
			return fmt.Errorf("modeling: field %q minimum exceeds maximum", f.Name)
		}
	}
	if f.MinLength != nil || f.MaxLength != nil {
		if f.Type != FieldString && f.Type != FieldArray {
			return fmt.Errorf("modeling: field %q length bounds require a string or array", f.Name)
		}
		if (f.MinLength != nil && *f.MinLength < 0) || (f.MaxLength != nil && *f.MaxLength < 0) {
			return fmt.Errorf("modeling: field %q length bounds must not be negative", f.Name)
		}
		if f.MinLength != nil && f.MaxLength != nil && *f.MinLength > *f.MaxLength {
			return fmt.Errorf("modeling: field %q min_length exceeds max_length", f.Name)
		}
	}
	seenEnum := map[string]bool{}
	for _, candidate := range f.Enum {
		if !matchesType(candidate, f.Type) {
			return fmt.Errorf("modeling: field %q enum value does not match %s", f.Name, f.Type)
		}
		canonical, err := canonicalJSON(candidate)
		if err != nil {
			return fmt.Errorf("modeling: field %q enum value: %w", f.Name, err)
		}
		if seenEnum[canonical] {
			return fmt.Errorf("modeling: field %q has duplicate enum value", f.Name)
		}
		seenEnum[canonical] = true
	}
	return nil
}

// Violation is one deterministic schema or record-quality finding.
type Violation struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// ValidateDocument validates a top-level JSON object against a schema and
// returns every deterministic finding in stable field order.
func ValidateDocument(spec SchemaSpec, raw json.RawMessage) ([]Violation, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("modeling: source document must be a JSON object: %w", err)
	}
	declared := make(map[string]FieldSpec, len(spec.Fields))
	for _, field := range spec.Fields {
		declared[field.Name] = field
	}
	violations := make([]Violation, 0)
	populated := 0
	for _, field := range spec.Fields {
		value, exists := object[field.Name]
		if !exists {
			if field.Required {
				violations = append(violations, Violation{
					Code: "required", Field: field.Name,
					Message: fmt.Sprintf("required field %q is missing", field.Name),
				})
			}
			continue
		}
		if string(value) == "null" {
			if !field.Nullable {
				violations = append(violations, Violation{
					Code: "nullability", Field: field.Name,
					Message: fmt.Sprintf("field %q must not be null", field.Name),
				})
			}
			continue
		}
		populated++
		if !matchesType(value, field.Type) {
			violations = append(violations, Violation{
				Code: "type", Field: field.Name,
				Message: fmt.Sprintf("field %q must be %s", field.Name, field.Type),
			})
			continue
		}
		violations = append(violations, validateValue(field, value)...)
	}
	for _, relationship := range spec.Relationships {
		value, exists := object[relationship.Field]
		if relationship.Required && (!exists || string(value) == "null") &&
			!declared[relationship.Field].Required {
			violations = append(violations, Violation{
				Code: "relationship_required", Field: relationship.Field,
				Message: fmt.Sprintf("relationship field %q is required", relationship.Field),
			})
		}
	}
	if !spec.AdditionalProperties {
		extra := make([]string, 0)
		for name := range object {
			if _, ok := declared[name]; !ok {
				extra = append(extra, name)
			}
		}
		sort.Strings(extra)
		for _, name := range extra {
			violations = append(violations, Violation{
				Code: "additional_property", Field: name,
				Message: fmt.Sprintf("undeclared field %q is not allowed", name),
			})
		}
	}
	completeness := float64(populated) / float64(len(spec.Fields))
	if completeness < spec.Quality.CompletenessMin {
		violations = append(violations, Violation{
			Code: "completeness",
			Message: fmt.Sprintf("record completeness %.6f is below minimum %.6f",
				completeness, spec.Quality.CompletenessMin),
		})
	}
	return violations, nil
}

func validateValue(field FieldSpec, raw json.RawMessage) []Violation {
	violations := make([]Violation, 0)
	if len(field.Enum) > 0 {
		actual, _ := canonicalJSON(raw)
		matched := false
		for _, candidate := range field.Enum {
			expected, _ := canonicalJSON(candidate)
			if actual == expected {
				matched = true
				break
			}
		}
		if !matched {
			violations = append(violations, Violation{
				Code: "enum", Field: field.Name,
				Message: fmt.Sprintf("field %q is not an allowed value", field.Name),
			})
		}
	}
	switch field.Type {
	case FieldNumber, FieldInteger:
		var number float64
		_ = json.Unmarshal(raw, &number)
		if field.Minimum != nil && number < *field.Minimum {
			violations = append(violations, Violation{
				Code: "minimum", Field: field.Name,
				Message: fmt.Sprintf("field %q is below minimum %v", field.Name, *field.Minimum),
			})
		}
		if field.Maximum != nil && number > *field.Maximum {
			violations = append(violations, Violation{
				Code: "maximum", Field: field.Name,
				Message: fmt.Sprintf("field %q exceeds maximum %v", field.Name, *field.Maximum),
			})
		}
	case FieldString:
		var value string
		_ = json.Unmarshal(raw, &value)
		length := utf8.RuneCountInString(value)
		violations = append(violations, lengthViolations(field, length)...)
		if field.Pattern != "" && !regexp.MustCompile(field.Pattern).MatchString(value) {
			violations = append(violations, Violation{
				Code: "pattern", Field: field.Name,
				Message: fmt.Sprintf("field %q does not match its required pattern", field.Name),
			})
		}
	case FieldArray:
		var value []any
		_ = json.Unmarshal(raw, &value)
		violations = append(violations, lengthViolations(field, len(value))...)
	}
	return violations
}

func lengthViolations(field FieldSpec, length int) []Violation {
	violations := make([]Violation, 0, 2)
	if field.MinLength != nil && length < *field.MinLength {
		violations = append(violations, Violation{
			Code: "min_length", Field: field.Name,
			Message: fmt.Sprintf("field %q length is below %d", field.Name, *field.MinLength),
		})
	}
	if field.MaxLength != nil && length > *field.MaxLength {
		violations = append(violations, Violation{
			Code: "max_length", Field: field.Name,
			Message: fmt.Sprintf("field %q length exceeds %d", field.Name, *field.MaxLength),
		})
	}
	return violations
}

func canonicalJSON(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

// UniqueTupleHash derives a value-free deterministic hash for a complete
// entity uniqueness tuple. Missing or null members return present=false; their
// structural violations still follow the schema's declared quality action.
func UniqueTupleHash(spec SchemaSpec, raw json.RawMessage) (hash string, present bool, err error) {
	if len(spec.Quality.UniqueFields) == 0 {
		return "", false, nil
	}
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", false, fmt.Errorf("modeling: uniqueness document: %w", err)
	}
	names := append([]string(nil), spec.Quality.UniqueFields...)
	sort.Strings(names)
	type member struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	}
	tuple := make([]member, 0, len(names))
	for _, name := range names {
		value, ok := object[name]
		if !ok || string(value) == "null" {
			return "", false, nil
		}
		tuple = append(tuple, member{Name: name, Value: value})
	}
	canonical, err := json.Marshal(tuple)
	if err != nil {
		return "", false, fmt.Errorf("modeling: marshal uniqueness tuple: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), true, nil
}

// RelationshipValue is a concrete relationship target extracted from a valid
// source document. The append boundary checks it against durable entity facts.
type RelationshipValue struct {
	Field            string
	TargetEntityType string
	TargetEntityID   string
}

// RelationshipValues extracts present, string-valued relationship targets.
// Structural type/missing findings are returned by ValidateDocument.
func RelationshipValues(spec SchemaSpec, raw json.RawMessage) ([]RelationshipValue, error) {
	if len(spec.Relationships) == 0 {
		return nil, nil
	}
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("modeling: relationship document: %w", err)
	}
	values := make([]RelationshipValue, 0, len(spec.Relationships))
	for _, relationship := range spec.Relationships {
		rawValue, ok := object[relationship.Field]
		if !ok || string(rawValue) == "null" {
			continue
		}
		var target string
		if err := json.Unmarshal(rawValue, &target); err != nil {
			continue
		}
		values = append(values, RelationshipValue{
			Field: relationship.Field, TargetEntityType: relationship.TargetEntityType,
			TargetEntityID: target,
		})
	}
	return values, nil
}

func matchesType(raw json.RawMessage, want FieldType) bool {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	switch want {
	case FieldString:
		_, ok := value.(string)
		return ok
	case FieldNumber:
		_, ok := value.(float64)
		return ok
	case FieldInteger:
		number, ok := value.(float64)
		return ok && math.Trunc(number) == number
	case FieldBoolean:
		_, ok := value.(bool)
		return ok
	case FieldObject:
		_, ok := value.(map[string]any)
		return ok
	case FieldArray:
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

// CompatibilityBreaks returns stable, human-readable reasons why next is
// incompatible with previous under next's declared compatibility mode.
func CompatibilityBreaks(previous, next SchemaSpec) []string {
	if next.Compatibility == CompatibilityNone {
		return nil
	}
	oldFields := make(map[string]FieldSpec, len(previous.Fields))
	newFields := make(map[string]FieldSpec, len(next.Fields))
	for _, field := range previous.Fields {
		oldFields[field.Name] = field
	}
	for _, field := range next.Fields {
		newFields[field.Name] = field
	}
	breaks := make([]string, 0)
	if next.Compatibility == CompatibilityBackward || next.Compatibility == CompatibilityFull {
		for name, oldField := range oldFields {
			newField, ok := newFields[name]
			if !ok {
				breaks = append(breaks, fmt.Sprintf("backward: field %q was removed", name))
				continue
			}
			if oldField.Type != newField.Type {
				breaks = append(breaks, fmt.Sprintf("backward: field %q changed type from %s to %s", name, oldField.Type, newField.Type))
			} else if !sameFieldContract(oldField, newField) {
				breaks = append(breaks, fmt.Sprintf("backward: field %q validity or governance contract changed", name))
			}
		}
		for name, field := range newFields {
			if _, existed := oldFields[name]; !existed && field.Required {
				breaks = append(breaks, fmt.Sprintf("backward: new field %q is required", name))
			}
		}
		if previous.AdditionalProperties && !next.AdditionalProperties {
			breaks = append(breaks, "backward: additional properties are no longer accepted")
		}
		if !sameRelationships(previous.Relationships, next.Relationships) {
			breaks = append(breaks, "backward: relationship contract changed")
		}
		if !sameQualityContract(previous.Quality, next.Quality) {
			breaks = append(breaks, "backward: quality contract changed")
		}
	}
	if next.Compatibility == CompatibilityForward || next.Compatibility == CompatibilityFull {
		for name, newField := range newFields {
			oldField, ok := oldFields[name]
			if !ok {
				if !previous.AdditionalProperties {
					breaks = append(breaks, fmt.Sprintf("forward: field %q is unknown to the previous schema", name))
				}
				continue
			}
			if oldField.Type != newField.Type {
				breaks = append(breaks, fmt.Sprintf("forward: field %q changed type from %s to %s", name, oldField.Type, newField.Type))
			} else if !sameFieldContract(oldField, newField) {
				breaks = append(breaks, fmt.Sprintf("forward: field %q validity or governance contract changed", name))
			}
		}
		for name, field := range oldFields {
			if _, remains := newFields[name]; !remains && field.Required {
				breaks = append(breaks, fmt.Sprintf("forward: removed field %q was required", name))
			}
		}
		if !previous.AdditionalProperties && next.AdditionalProperties {
			breaks = append(breaks, "forward: additional properties may be emitted")
		}
		if !sameRelationships(previous.Relationships, next.Relationships) {
			breaks = append(breaks, "forward: relationship contract changed")
		}
		if !sameQualityContract(previous.Quality, next.Quality) {
			breaks = append(breaks, "forward: quality contract changed")
		}
	}
	sort.Strings(breaks)
	return breaks
}

func sameFieldContract(left, right FieldSpec) bool {
	return left.Required == right.Required &&
		left.Nullable == right.Nullable &&
		left.Identifier == right.Identifier &&
		left.Classification == right.Classification &&
		sameRawSet(left.Enum, right.Enum) &&
		left.Pattern == right.Pattern &&
		equalFloatPointer(left.Minimum, right.Minimum) &&
		equalFloatPointer(left.Maximum, right.Maximum) &&
		equalIntPointer(left.MinLength, right.MinLength) &&
		equalIntPointer(left.MaxLength, right.MaxLength)
}

func sameRawSet(left, right []json.RawMessage) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]int, len(left))
	for _, raw := range left {
		canonical, err := canonicalJSON(raw)
		if err != nil {
			return false
		}
		values[canonical]++
	}
	for _, raw := range right {
		canonical, err := canonicalJSON(raw)
		if err != nil || values[canonical] == 0 {
			return false
		}
		values[canonical]--
	}
	return true
}

func sameRelationships(left, right []Relationship) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]int, len(left))
	for _, relationship := range left {
		key := relationship.Field + "\x00" + relationship.TargetEntityType +
			fmt.Sprintf("\x00%t", relationship.Required)
		values[key]++
	}
	for _, relationship := range right {
		key := relationship.Field + "\x00" + relationship.TargetEntityType +
			fmt.Sprintf("\x00%t", relationship.Required)
		if values[key] == 0 {
			return false
		}
		values[key]--
	}
	return true
}

func sameQualityContract(left, right QualityContract) bool {
	if left.Action != right.Action ||
		left.CompletenessMin != right.CompletenessMin ||
		left.FreshnessSeconds != right.FreshnessSeconds ||
		len(left.UniqueFields) != len(right.UniqueFields) {
		return false
	}
	leftUnique := append([]string(nil), left.UniqueFields...)
	rightUnique := append([]string(nil), right.UniqueFields...)
	sort.Strings(leftUnique)
	sort.Strings(rightUnique)
	for index := range leftUnique {
		if leftUnique[index] != rightUnique[index] {
			return false
		}
	}
	return true
}

func equalFloatPointer(left, right *float64) bool {
	return (left == nil && right == nil) ||
		(left != nil && right != nil && *left == *right)
}

func equalIntPointer(left, right *int) bool {
	return (left == nil && right == nil) ||
		(left != nil && right != nil && *left == *right)
}

// HashSchema returns the deterministic SHA-256 hash of the immutable schema.
func HashSchema(spec SchemaSpec) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("modeling: marshal schema: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
