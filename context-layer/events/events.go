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
	TypeEntityRecorded = "context.entity_recorded"
	TypeEventRecorded  = "context.event_recorded"
	TypeFeatureDefined = "context.feature_defined"
)

// EntityRecorded records (or patches) a custom entity's attributes.
type EntityRecorded struct {
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Attributes json.RawMessage `json:"attributes,omitempty"`
}

// EventRecorded records a custom event about an entity. OccurredAt is recorded in
// the payload (filled by the command when the caller omits it) so projections and
// the feature engine read a stable value on replay.
type EventRecorded struct {
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	EventName  string          `json:"event_name"`
	Data       json.RawMessage `json:"data,omitempty"`
	OccurredAt time.Time       `json:"occurred_at"`
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
