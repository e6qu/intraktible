// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// CompatibilityStatus classifies whether an exact-pin consumer can move from
// one immutable component contract to another without coordinated changes.
type CompatibilityStatus string

const (
	CompatibilityInitial      CompatibilityStatus = "initial"
	CompatibilityCompatible   CompatibilityStatus = "compatible"
	CompatibilityIncompatible CompatibilityStatus = "incompatible"
)

// CompatibilityIssue is one machine-readable reason an automatic upgrade is
// blocked. Path uses JSON-Schema coordinates rather than graph node IDs.
type CompatibilityIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// CompatibilityReport is computed by the server from adjacent immutable
// contracts. It is evidence, not a caller assertion.
type CompatibilityReport struct {
	FromVersion int                  `json:"from_version,omitempty"`
	ToVersion   int                  `json:"to_version"`
	Status      CompatibilityStatus  `json:"status"`
	Issues      []CompatibilityIssue `json:"issues,omitempty"`
}

type contractSchema struct {
	Types                []string                   `json:"-"`
	Properties           map[string]json.RawMessage `json:"properties,omitempty"`
	Required             []string                   `json:"required,omitempty"`
	AdditionalProperties *bool                      `json:"additionalProperties,omitempty"`
	Default              json.RawMessage            `json:"default,omitempty"`
	Raw                  map[string]json.RawMessage `json:"-"`
}

// AssessComponentCompatibility verifies the substitutability rule used for an
// automatic exact-pin upgrade: the new component must accept every old input
// and must continue to guarantee every old output.
func AssessComponentCompatibility(
	fromVersion, toVersion int,
	oldInput, oldOutput, newInput, newOutput json.RawMessage,
) (CompatibilityReport, error) {
	report := CompatibilityReport{
		FromVersion: fromVersion,
		ToVersion:   toVersion,
		Status:      CompatibilityCompatible,
	}
	inputIssues, err := compareSchema("input_schema", oldInput, newInput, schemaInput)
	if err != nil {
		return CompatibilityReport{}, err
	}
	outputIssues, err := compareSchema("output_schema", oldOutput, newOutput, schemaOutput)
	if err != nil {
		return CompatibilityReport{}, err
	}
	report.Issues = make([]CompatibilityIssue, 0, len(inputIssues)+len(outputIssues))
	report.Issues = append(report.Issues, inputIssues...)
	report.Issues = append(report.Issues, outputIssues...)
	sort.Slice(report.Issues, func(i, j int) bool {
		if report.Issues[i].Path == report.Issues[j].Path {
			return report.Issues[i].Code < report.Issues[j].Code
		}
		return report.Issues[i].Path < report.Issues[j].Path
	})
	if len(report.Issues) > 0 {
		report.Status = CompatibilityIncompatible
	}
	return report, nil
}

type schemaDirection int

const (
	schemaInput schemaDirection = iota
	schemaOutput
)

func compareSchema(
	path string,
	oldRaw, newRaw json.RawMessage,
	direction schemaDirection,
) ([]CompatibilityIssue, error) {
	oldSchema, err := parseContractSchema(path, oldRaw)
	if err != nil {
		return nil, err
	}
	newSchema, err := parseContractSchema(path, newRaw)
	if err != nil {
		return nil, err
	}
	return compareContractNode(path, oldSchema, newSchema, direction), nil
}

func parseContractSchema(path string, raw json.RawMessage) (contractSchema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return contractSchema{}, fmt.Errorf("authoring: %s: %w", path, err)
	}
	schema := contractSchema{Raw: object}
	if value := object["type"]; len(value) > 0 {
		if err := json.Unmarshal(value, &schema.Types); err != nil {
			var single string
			if singleErr := json.Unmarshal(value, &single); singleErr != nil {
				return contractSchema{}, fmt.Errorf(
					"authoring: %s type must be a string or string array",
					path,
				)
			}
			schema.Types = []string{single}
		}
		sort.Strings(schema.Types)
	}
	if value := object["properties"]; len(value) > 0 {
		if err := json.Unmarshal(value, &schema.Properties); err != nil {
			return contractSchema{}, fmt.Errorf("authoring: %s properties: %w", path, err)
		}
	}
	if value := object["required"]; len(value) > 0 {
		if err := json.Unmarshal(value, &schema.Required); err != nil {
			return contractSchema{}, fmt.Errorf("authoring: %s required: %w", path, err)
		}
	}
	if value := object["additionalProperties"]; len(value) > 0 {
		var allowed bool
		if err := json.Unmarshal(value, &allowed); err == nil {
			schema.AdditionalProperties = &allowed
		} else {
			return contractSchema{}, fmt.Errorf(
				"authoring: %s additionalProperties must be boolean for compatibility analysis",
				path,
			)
		}
	}
	schema.Default = object["default"]
	return schema, nil
}

func compareContractNode(
	path string,
	oldSchema, newSchema contractSchema,
	direction schemaDirection,
) []CompatibilityIssue {
	issues := make([]CompatibilityIssue, 0)
	if !typesSubstitutable(oldSchema.Types, newSchema.Types, direction) {
		issues = append(issues, CompatibilityIssue{
			Path: path, Code: "type_changed",
			Message: "the accepted or guaranteed JSON type changed",
		})
	}
	oldRequired, newRequired := stringSet(oldSchema.Required), stringSet(newSchema.Required)
	if direction == schemaInput {
		for name := range newRequired {
			if !oldRequired[name] {
				issues = append(issues, CompatibilityIssue{
					Path: path + ".properties." + name, Code: "new_required_input",
					Message: "a new required input would reject existing callers",
				})
			}
		}
	} else {
		for name := range oldRequired {
			if !newRequired[name] {
				issues = append(issues, CompatibilityIssue{
					Path: path + ".properties." + name, Code: "required_output_removed",
					Message: "an output previously guaranteed is no longer required",
				})
			}
		}
	}
	for name, oldPropertyRaw := range oldSchema.Properties {
		newPropertyRaw, exists := newSchema.Properties[name]
		if !exists {
			if direction == schemaOutput || additionalPropertiesClosed(newSchema) {
				issues = append(issues, CompatibilityIssue{
					Path: path + ".properties." + name, Code: "property_removed",
					Message: "a previously declared property is no longer available",
				})
			}
			continue
		}
		oldProperty, oldErr := parseContractSchema(path+".properties."+name, oldPropertyRaw)
		newProperty, newErr := parseContractSchema(path+".properties."+name, newPropertyRaw)
		if oldErr != nil || newErr != nil {
			issues = append(issues, CompatibilityIssue{
				Path: path + ".properties." + name, Code: "unsupported_property_schema",
				Message: "the property schema cannot be proven compatible",
			})
			continue
		}
		issues = append(
			issues,
			compareContractNode(path+".properties."+name, oldProperty, newProperty, direction)...,
		)
	}
	if direction == schemaInput && !additionalPropertiesClosed(oldSchema) &&
		additionalPropertiesClosed(newSchema) {
		issues = append(issues, CompatibilityIssue{
			Path: path + ".additionalProperties", Code: "input_closed",
			Message: "the new input contract rejects properties previously accepted",
		})
	}
	return issues
}

func typesSubstitutable(oldTypes, newTypes []string, direction schemaDirection) bool {
	if len(oldTypes) == 0 {
		return len(newTypes) == 0
	}
	if len(newTypes) == 0 {
		return direction == schemaInput
	}
	oldSet, newSet := stringSet(oldTypes), stringSet(newTypes)
	if direction == schemaInput {
		for value := range oldSet {
			if !newSet[value] {
				return false
			}
		}
		return true
	}
	for value := range newSet {
		if !oldSet[value] {
			return false
		}
	}
	return true
}

func additionalPropertiesClosed(schema contractSchema) bool {
	return schema.AdditionalProperties != nil && !*schema.AdditionalProperties
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}
