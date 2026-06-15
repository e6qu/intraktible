// SPDX-License-Identifier: AGPL-3.0-or-later

// Package domain is the Context Layer's functional core: pure types, validation,
// and the attribute-merge logic — no I/O.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RecordEntity upserts a custom entity (dynamic JSONB attributes). Re-recording
// the same (type, id) patches its attributes (top-level keys merge, latest wins).
type RecordEntity struct {
	EntityType string
	EntityID   string
	Attributes json.RawMessage
}

// Validate requires a type and id and, if present, JSON-object attributes.
func (c RecordEntity) Validate() error {
	if strings.TrimSpace(c.EntityType) == "" {
		return errors.New("context-layer: entity_type is required")
	}
	if strings.TrimSpace(c.EntityID) == "" {
		return errors.New("context-layer: entity_id is required")
	}
	return validJSONObject("attributes", c.Attributes)
}

// RecordEvent records a custom event about an entity. OccurredAt is optional; the
// shell fills it with the record time when zero (a recorded effect, replay-stable).
type RecordEvent struct {
	EntityType string
	EntityID   string
	EventName  string
	Data       json.RawMessage
	OccurredAt time.Time
}

// Validate requires the entity coordinates and an event name and, if present,
// JSON-object data.
func (c RecordEvent) Validate() error {
	if strings.TrimSpace(c.EntityType) == "" {
		return errors.New("context-layer: entity_type is required")
	}
	if strings.TrimSpace(c.EntityID) == "" {
		return errors.New("context-layer: entity_id is required")
	}
	if strings.TrimSpace(c.EventName) == "" {
		return errors.New("context-layer: event_name is required")
	}
	return validJSONObject("data", c.Data)
}

// validJSONObject reports an error unless raw is empty or a JSON object — scalars
// and arrays are rejected loudly so callers cannot smuggle non-object payloads.
func validJSONObject(field string, raw json.RawMessage) error {
	if _, err := toObject(raw); err != nil {
		return fmt.Errorf("context-layer: %s must be a JSON object: %w", field, err)
	}
	return nil
}

// MergeAttributes shallow-merges patch over base at the top level: patch keys win,
// base keys absent from patch are retained. Both must be JSON objects or empty.
func MergeAttributes(base, patch json.RawMessage) (json.RawMessage, error) {
	merged, err := toObject(base)
	if err != nil {
		return nil, fmt.Errorf("context-layer: base attributes: %w", err)
	}
	p, err := toObject(patch)
	if err != nil {
		return nil, fmt.Errorf("context-layer: patch attributes: %w", err)
	}
	for k, v := range p {
		merged[k] = v
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("context-layer: marshal merged attributes: %w", err)
	}
	return out, nil
}

// toObject decodes raw into a key→raw map; empty/null is an empty object.
func toObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
