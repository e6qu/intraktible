// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// LabelKind identifies the supervised target representation.
type LabelKind string

const (
	LabelBinary     LabelKind = "binary"
	LabelContinuous LabelKind = "continuous"
	LabelMulticlass LabelKind = "multiclass"
)

// LabelSpec declares how a point-in-time observation is joined to a later
// outcome event. Missing labels remain censored instead of being coerced to 0.
type LabelSpec struct {
	EventName     string    `json:"event_name"`
	Field         string    `json:"field"`
	Kind          LabelKind `json:"kind"`
	PositiveValue string    `json:"positive_value,omitempty"`
	HorizonHours  int       `json:"horizon_hours"`
}

// PartitionSpec assigns deterministic basis-point ranges.
type PartitionSpec struct {
	TrainBPS      int `json:"train_bps"`
	ValidationBPS int `json:"validation_bps"`
	TestBPS       int `json:"test_bps"`
}

// ConsentMode declares whether snapshot inclusion must be backed by the shared
// purpose-limitation ledger at the knowledge cutoff.
type ConsentMode string

const (
	ConsentNotRequired ConsentMode = "not_required"
	ConsentActive      ConsentMode = "active"
)

// ConsentRequirement makes lawful-basis handling explicit rather than silently
// assuming that model development may reuse every source subject.
type ConsentRequirement struct {
	Mode    ConsentMode `json:"mode"`
	Purpose string      `json:"purpose,omitempty"`
}

// PopulationOperator is one deterministic top-level attribute predicate.
type PopulationOperator string

const (
	PopulationEquals    PopulationOperator = "equals"
	PopulationNotEquals PopulationOperator = "not_equals"
	PopulationIn        PopulationOperator = "in"
	PopulationNotIn     PopulationOperator = "not_in"
	PopulationExists    PopulationOperator = "exists"
	PopulationNotExists PopulationOperator = "not_exists"
	PopulationGT        PopulationOperator = "gt"
	PopulationGTE       PopulationOperator = "gte"
	PopulationLT        PopulationOperator = "lt"
	PopulationLTE       PopulationOperator = "lte"
)

// PopulationRule is a named, auditable inclusion or exclusion predicate.
type PopulationRule struct {
	Name     string             `json:"name"`
	Field    string             `json:"field"`
	Operator PopulationOperator `json:"operator"`
	Value    json.RawMessage    `json:"value,omitempty"`
	Reason   string             `json:"reason"`
}

// DatasetSpec is one immutable dataset-definition version.
type DatasetSpec struct {
	Name               string             `json:"name"`
	Description        string             `json:"description,omitempty"`
	OwnerTeam          string             `json:"owner_team"`
	EntityType         string             `json:"entity_type"`
	Features           []string           `json:"features"`
	Label              LabelSpec          `json:"label"`
	SegmentFields      []string           `json:"segment_fields,omitempty"`
	InclusionRules     []PopulationRule   `json:"inclusion_rules,omitempty"`
	ExclusionRules     []PopulationRule   `json:"exclusion_rules,omitempty"`
	Purpose            string             `json:"purpose"`
	ConsentRequirement ConsentRequirement `json:"consent_requirement"`
	RetentionDays      int                `json:"retention_days"`
	Partitions         PartitionSpec      `json:"partitions"`
}

// Validate checks a dataset definition.
func (spec DatasetSpec) Validate() error {
	switch {
	case strings.TrimSpace(spec.Name) == "":
		return errors.New("modeling: dataset name is required")
	case strings.Contains(spec.Name, "/"):
		return errors.New("modeling: dataset name must not contain '/'")
	case strings.TrimSpace(spec.OwnerTeam) == "":
		return errors.New("modeling: dataset owner_team is required")
	case strings.TrimSpace(spec.EntityType) == "":
		return errors.New("modeling: dataset entity_type is required")
	case strings.TrimSpace(spec.Purpose) == "":
		return errors.New("modeling: dataset purpose is required")
	case spec.RetentionDays <= 0 || spec.RetentionDays > 3650:
		return errors.New("modeling: dataset retention_days must be between 1 and 3650")
	case len(spec.Features) == 0:
		return errors.New("modeling: dataset requires at least one feature")
	}
	seen := map[string]bool{}
	for _, feature := range spec.Features {
		if strings.TrimSpace(feature) == "" {
			return errors.New("modeling: dataset feature names must not be blank")
		}
		if seen[feature] {
			return fmt.Errorf("modeling: duplicate dataset feature %q", feature)
		}
		seen[feature] = true
	}
	switch spec.Label.Kind {
	case LabelBinary, LabelContinuous, LabelMulticlass:
	default:
		return fmt.Errorf("modeling: unsupported label kind %q", spec.Label.Kind)
	}
	if strings.TrimSpace(spec.Label.EventName) == "" || strings.TrimSpace(spec.Label.Field) == "" {
		return errors.New("modeling: label event_name and field are required")
	}
	if spec.Label.Kind == LabelBinary && strings.TrimSpace(spec.Label.PositiveValue) == "" {
		return errors.New("modeling: binary label positive_value is required")
	}
	if spec.Label.HorizonHours <= 0 {
		return errors.New("modeling: label horizon_hours must be positive")
	}
	segments := map[string]bool{}
	for _, field := range spec.SegmentFields {
		if strings.TrimSpace(field) == "" {
			return errors.New("modeling: segment fields must not be blank")
		}
		if segments[field] {
			return fmt.Errorf("modeling: duplicate segment field %q", field)
		}
		segments[field] = true
	}
	switch spec.ConsentRequirement.Mode {
	case ConsentNotRequired:
		if strings.TrimSpace(spec.ConsentRequirement.Purpose) != "" {
			return errors.New("modeling: not_required consent mode must not set a purpose")
		}
	case ConsentActive:
		if strings.TrimSpace(spec.ConsentRequirement.Purpose) == "" {
			return errors.New("modeling: active consent mode requires a purpose")
		}
	default:
		return fmt.Errorf(
			"modeling: unsupported consent mode %q", spec.ConsentRequirement.Mode,
		)
	}
	ruleNames := map[string]bool{}
	for _, group := range [][]PopulationRule{spec.InclusionRules, spec.ExclusionRules} {
		for _, rule := range group {
			if err := rule.validate(); err != nil {
				return err
			}
			if ruleNames[rule.Name] {
				return fmt.Errorf("modeling: duplicate population rule %q", rule.Name)
			}
			ruleNames[rule.Name] = true
		}
	}
	if spec.Partitions.TrainBPS < 0 || spec.Partitions.ValidationBPS < 0 ||
		spec.Partitions.TestBPS < 0 ||
		spec.Partitions.TrainBPS+spec.Partitions.ValidationBPS+spec.Partitions.TestBPS != 10_000 {
		return errors.New("modeling: partition basis points must be non-negative and total 10000")
	}
	return nil
}

func (rule PopulationRule) validate() error {
	if strings.TrimSpace(rule.Name) == "" || strings.TrimSpace(rule.Field) == "" ||
		strings.TrimSpace(rule.Reason) == "" {
		return errors.New("modeling: population rule name, field, and reason are required")
	}
	switch rule.Operator {
	case PopulationExists, PopulationNotExists:
		if len(rule.Value) != 0 {
			return fmt.Errorf(
				"modeling: population rule %q operator %s must not set value",
				rule.Name, rule.Operator,
			)
		}
	case PopulationIn, PopulationNotIn:
		var values []any
		if len(rule.Value) == 0 || json.Unmarshal(rule.Value, &values) != nil || len(values) == 0 {
			return fmt.Errorf(
				"modeling: population rule %q operator %s requires a non-empty JSON array",
				rule.Name, rule.Operator,
			)
		}
	case PopulationEquals, PopulationNotEquals:
		var value any
		if len(rule.Value) == 0 || json.Unmarshal(rule.Value, &value) != nil {
			return fmt.Errorf("modeling: population rule %q requires a JSON value", rule.Name)
		}
	case PopulationGT, PopulationGTE, PopulationLT, PopulationLTE:
		var value float64
		if len(rule.Value) == 0 || json.Unmarshal(rule.Value, &value) != nil ||
			math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("modeling: population rule %q requires a finite number", rule.Name)
		}
	default:
		return fmt.Errorf(
			"modeling: population rule %q has unsupported operator %q",
			rule.Name, rule.Operator,
		)
	}
	return nil
}

// PopulationDecision evaluates immutable dataset population rules against one
// point-in-time entity state. Inclusion rules must all match; any matching
// exclusion rule rejects the subject. Returned rule names are safe audit
// evidence and never contain source values.
func PopulationDecision(
	attributes json.RawMessage,
	inclusionRules []PopulationRule,
	exclusionRules []PopulationRule,
) (bool, []string, error) {
	var document map[string]any
	if err := json.Unmarshal(attributes, &document); err != nil {
		return false, nil, fmt.Errorf("modeling: decode population attributes: %w", err)
	}
	reasons := make([]string, 0)
	for _, rule := range inclusionRules {
		matches, err := rule.matches(document)
		if err != nil {
			return false, nil, err
		}
		if !matches {
			reasons = append(reasons, rule.Name)
		}
	}
	for _, rule := range exclusionRules {
		matches, err := rule.matches(document)
		if err != nil {
			return false, nil, err
		}
		if matches {
			reasons = append(reasons, rule.Name)
		}
	}
	sort.Strings(reasons)
	return len(reasons) == 0, reasons, nil
}

func (rule PopulationRule) matches(document map[string]any) (bool, error) {
	actual, exists := document[rule.Field]
	switch rule.Operator {
	case PopulationExists:
		return exists, nil
	case PopulationNotExists:
		return !exists, nil
	}
	if !exists {
		return false, nil
	}
	var expected any
	if err := json.Unmarshal(rule.Value, &expected); err != nil {
		return false, fmt.Errorf("modeling: decode population rule %q: %w", rule.Name, err)
	}
	switch rule.Operator {
	case PopulationEquals, PopulationNotEquals:
		equal := canonicalValue(actual) == canonicalValue(expected)
		if rule.Operator == PopulationNotEquals {
			equal = !equal
		}
		return equal, nil
	case PopulationIn, PopulationNotIn:
		values, ok := expected.([]any)
		if !ok {
			return false, fmt.Errorf("modeling: population rule %q value is not an array", rule.Name)
		}
		found := false
		for _, candidate := range values {
			if canonicalValue(actual) == canonicalValue(candidate) {
				found = true
				break
			}
		}
		if rule.Operator == PopulationNotIn {
			found = !found
		}
		return found, nil
	case PopulationGT, PopulationGTE, PopulationLT, PopulationLTE:
		actualNumber, actualOK := actual.(float64)
		expectedNumber, expectedOK := expected.(float64)
		if !actualOK || !expectedOK {
			return false, nil
		}
		switch rule.Operator {
		case PopulationGT:
			return actualNumber > expectedNumber, nil
		case PopulationGTE:
			return actualNumber >= expectedNumber, nil
		case PopulationLT:
			return actualNumber < expectedNumber, nil
		default:
			return actualNumber <= expectedNumber, nil
		}
	default:
		return false, fmt.Errorf(
			"modeling: unsupported population operator %q", rule.Operator,
		)
	}
}

func canonicalValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("modeling: JSON-decoded population value is not marshalable: " + err.Error())
	}
	return string(encoded)
}

// SnapshotRequest pins the two temporal cutoffs. ObservationAt is feature/event
// time; KnowledgeAt is the latest receipt/correction time the snapshot may use.
type SnapshotRequest struct {
	DatasetName    string    `json:"dataset_name"`
	Version        int       `json:"version"`
	ObservationAt  time.Time `json:"observation_at"`
	KnowledgeAt    time.Time `json:"knowledge_at"`
	IdempotencyKey string    `json:"idempotency_key"`
}

// Validate checks snapshot cutoffs and replay identity.
func (request SnapshotRequest) Validate() error {
	switch {
	case strings.TrimSpace(request.DatasetName) == "":
		return errors.New("modeling: dataset_name is required")
	case request.Version <= 0:
		return errors.New("modeling: dataset version must be positive")
	case request.ObservationAt.IsZero() || request.KnowledgeAt.IsZero():
		return errors.New("modeling: observation_at and knowledge_at are required")
	case request.KnowledgeAt.Before(request.ObservationAt):
		return errors.New("modeling: knowledge_at must not be before observation_at")
	case len(strings.TrimSpace(request.IdempotencyKey)) < 8:
		return errors.New("modeling: idempotency_key must be at least 8 characters")
	default:
		return nil
	}
}

// DatasetRow is one point-in-time entity observation.
type DatasetRow struct {
	EntityID      string             `json:"entity_id"`
	Features      map[string]float64 `json:"features"`
	Label         json.RawMessage    `json:"label,omitempty"`
	LabelPresent  bool               `json:"label_present"`
	Censored      bool               `json:"censored"`
	Segments      map[string]string  `json:"segments,omitempty"`
	Partition     string             `json:"partition"`
	ObservationAt time.Time          `json:"observation_at"`
	KnowledgeAt   time.Time          `json:"knowledge_at"`
}

// SnapshotManifest is the immutable, publish-after-write dataset artifact.
type SnapshotManifest struct {
	SnapshotID              string           `json:"snapshot_id"`
	DatasetName             string           `json:"dataset_name"`
	DatasetVersion          int              `json:"dataset_version"`
	DatasetHash             string           `json:"dataset_hash"`
	RowsHash                string           `json:"rows_hash"`
	RowCount                int              `json:"row_count"`
	LabelledCount           int              `json:"labelled_count"`
	CensoredCount           int              `json:"censored_count"`
	CandidateCount          int              `json:"candidate_count"`
	PopulationExcludedCount int              `json:"population_excluded_count"`
	ConsentExcludedCount    int              `json:"consent_excluded_count"`
	QualityFindingCount     int              `json:"quality_finding_count"`
	FeatureCompleteness     float64          `json:"feature_completeness"`
	PartitionCounts         map[string]int   `json:"partition_counts"`
	FeatureVersions         map[string]int   `json:"feature_versions"`
	SchemaVersions          map[string][]int `json:"schema_versions"`
	ObservationAt           time.Time        `json:"observation_at"`
	KnowledgeAt             time.Time        `json:"knowledge_at"`
	StorageRef              string           `json:"storage_ref"`
	Purpose                 string           `json:"purpose"`
	ExpiresAt               time.Time        `json:"expires_at"`
	PublishedAt             time.Time        `json:"published_at"`
}

// PartitionFor deterministically assigns an entity to a configured partition.
func PartitionFor(entityID string, partitions PartitionSpec) string {
	sum := sha256.Sum256([]byte(entityID))
	bucket := int(sum[0])<<8 | int(sum[1])
	bucket %= 10_000
	if bucket < partitions.TrainBPS {
		return "train"
	}
	if bucket < partitions.TrainBPS+partitions.ValidationBPS {
		return "validation"
	}
	return "test"
}

// HashRows canonicalizes map-containing rows and returns their content hash.
func HashRows(rows []DatasetRow) (string, []byte, error) {
	canonical := append([]DatasetRow(nil), rows...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].EntityID < canonical[j].EntityID })
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", nil, fmt.Errorf("modeling: marshal dataset rows: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), payload, nil
}

// NumericLabel converts a binary or continuous JSON label for the in-process
// trainer. Multiclass labels require a compatible external runtime.
func NumericLabel(spec LabelSpec, raw json.RawMessage) (float64, error) {
	switch spec.Kind {
	case LabelBinary:
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, fmt.Errorf("modeling: decode binary label: %w", err)
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			return 0, err
		}
		var positive any
		if err := json.Unmarshal([]byte(spec.PositiveValue), &positive); err != nil {
			positive = spec.PositiveValue
		}
		positiveCanonical, err := json.Marshal(positive)
		if err != nil {
			return 0, err
		}
		if bytes.Equal(canonical, positiveCanonical) {
			return 1, nil
		}
		return 0, nil
	case LabelContinuous:
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, fmt.Errorf("modeling: decode continuous label: %w", err)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, errors.New("modeling: continuous label must be finite")
		}
		return value, nil
	default:
		return 0, fmt.Errorf("modeling: label kind %q is not numeric", spec.Kind)
	}
}
