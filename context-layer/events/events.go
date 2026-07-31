// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events defines the Context Layer's event payloads: entities are
// recorded (and patched) via EntityRecorded; custom events about an entity are
// recorded via EventRecorded. These are the raw signals the feature engine will
// later aggregate into windowed counts/sums.
package events

import (
	"encoding/json"
	"time"
)

// StreamContext is the Context Layer's event stream.
const StreamContext = "context"

// Context Layer event types.
const (
	TypeEntityRecorded   = "context.entity_recorded"
	TypeEventRecorded    = "context.event_recorded"
	TypeEventRetracted   = "context.event_retracted"
	TypeFeatureDefined   = "context.feature_defined"
	TypeConnectorDefined = "context.connector_defined"
	TypeConnectorFetched = "context.connector_fetched"
)

// EntityRecorded records (or patches) a custom entity's attributes.
type EntityRecorded struct {
	EntityType     string          `json:"entity_type"`
	EntityID       string          `json:"entity_id"`
	Attributes     json.RawMessage `json:"attributes,omitempty"`
	SchemaEvidence SchemaEvidence  `json:"schema_evidence,omitempty"`
}

// EventRecorded records a custom event about an entity. OccurredAt is recorded in
// the payload (filled by the command when the caller omits it) so projections and
// the feature engine read a stable value on replay.
type EventRecorded struct {
	EntityType        string          `json:"entity_type"`
	EntityID          string          `json:"entity_id"`
	EventName         string          `json:"event_name"`
	EventID           string          `json:"event_id,omitempty"`
	SupersedesEventID string          `json:"supersedes_event_id,omitempty"`
	Data              json.RawMessage `json:"data,omitempty"`
	OccurredAt        time.Time       `json:"occurred_at"`
	ReceivedAt        time.Time       `json:"received_at,omitempty"`
	SchemaEvidence    SchemaEvidence  `json:"schema_evidence,omitempty"`
}

// EventRetracted removes a source signal from effective folds without deleting
// or rewriting its immutable record.
type EventRetracted struct {
	EventID     string    `json:"event_id"`
	EntityType  string    `json:"entity_type"`
	EntityID    string    `json:"entity_id"`
	EventName   string    `json:"event_name"`
	Reason      string    `json:"reason"`
	RetractedAt time.Time `json:"retracted_at"`
}

// QualityViolation is a replay-stable source quality finding produced under the
// exact active schema at ingestion time.
type QualityViolation struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// SchemaEvidence pins the schema and policy result that admitted a source
// record. An empty value is an explicitly ungoverned source with no active
// schema; once a source has an active schema, the Context service requires its
// exact version and records this evidence.
type SchemaEvidence struct {
	Version              int                `json:"version,omitempty"`
	Hash                 string             `json:"hash,omitempty"`
	Action               string             `json:"action,omitempty"`
	Violations           []QualityViolation `json:"violations,omitempty"`
	EvaluatedAt          time.Time          `json:"evaluated_at,omitempty"`
	PolicyApprovalID     string             `json:"policy_approval_id,omitempty"`
	PolicyApprovedBy     string             `json:"policy_approved_by,omitempty"`
	PolicyApprovedAt     time.Time          `json:"policy_approved_at,omitempty"`
	PolicyApprovalReason string             `json:"policy_approval_reason,omitempty"`
	UniqueClaim          string             `json:"unique_claim,omitempty"`
}

// FeatureDefined defines (or redefines) a windowed feature over an entity type's
// event stream.
type FeatureDefined struct {
	Name        string `json:"name"`
	EntityType  string `json:"entity_type"`
	EventName   string `json:"event_name"`
	Aggregation string `json:"aggregation"`
	Field       string `json:"field,omitempty"`
	WindowHours int    `json:"window_hours"`
}

// ConnectorDefined registers (or redefines) a named external connector.
type ConnectorDefined struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// ConnectorFetched records one connector invocation and its result, so a fetch is
// auditable and the recorded response — not a re-fetch — is what replay reads.
type ConnectorFetched struct {
	FetchID   string          `json:"fetch_id"`
	Connector string          `json:"connector"`
	Params    json.RawMessage `json:"params,omitempty"`
	Response  json.RawMessage `json:"response"`
	At        time.Time       `json:"at"`
}
