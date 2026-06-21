// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Aggregation names how a feature folds its matched events into a value. It is a
// named type (not a bare string) so an unknown aggregation is caught at the
// boundary — Valid() is the single source of truth, replacing the hand-rolled
// switch defaults that previously let a bad value silently compute 0. JSON
// marshaling is identical to a plain string.
type Aggregation string

// Feature aggregations.
const (
	AggCount Aggregation = "count"
	AggSum   Aggregation = "sum"
)

var aggregations = map[Aggregation]bool{AggCount: true, AggSum: true}

// Valid reports whether a is a known aggregation.
func (a Aggregation) Valid() bool { return aggregations[a] }

// DefineFeature defines a windowed signal computed from an entity's event stream
// (e.g. "count of transaction events in the last 24h"). Re-defining the same
// (entity_type, name) overwrites the definition.
type DefineFeature struct {
	Name        string
	EntityType  string
	EventName   string
	Aggregation Aggregation
	Field       string // required for sum: the numeric top-level data key to add up
	WindowHours int
}

// Validate enforces a complete, computable definition.
func (c DefineFeature) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("context-layer: feature name is required")
	}
	if strings.TrimSpace(c.EntityType) == "" {
		return errors.New("context-layer: entity_type is required")
	}
	if strings.TrimSpace(c.EventName) == "" {
		return errors.New("context-layer: event_name is required")
	}
	switch c.Aggregation {
	case AggCount:
	case AggSum:
		if strings.TrimSpace(c.Field) == "" {
			return errors.New("context-layer: sum features require a field")
		}
	default:
		return fmt.Errorf("context-layer: unknown aggregation %q (count|sum)", string(c.Aggregation))
	}
	if c.WindowHours <= 0 {
		return fmt.Errorf("context-layer: window_hours must be > 0, got %d", c.WindowHours)
	}
	return nil
}

// FeatureSpec is the pure, resolved shape the engine folds events against.
type FeatureSpec struct {
	EventName   string
	Aggregation Aggregation
	Field       string
	Window      time.Duration
}

// FeatureInput is the minimal event shape the engine reads.
type FeatureInput struct {
	EventName  string
	Data       json.RawMessage
	OccurredAt time.Time
}

// Compute folds events into the feature's value as of now: it keeps events whose
// name matches and whose occurrence falls in (now-window, now], then counts them
// or sums spec.Field. A matching event missing the field contributes nothing; a
// present-but-non-numeric field fails loudly.
func Compute(spec FeatureSpec, events []FeatureInput, now time.Time) (float64, error) {
	// Validate the aggregation as a PRECONDITION, not inside the per-event loop: with
	// zero matching events the loop body never runs, so an invalid aggregation would
	// otherwise silently return 0 instead of erroring (define-time validation already
	// guards this for stored specs; this hardens a hand-constructed one).
	if !spec.Aggregation.Valid() {
		return 0, fmt.Errorf("context-layer: unknown aggregation %q", string(spec.Aggregation))
	}
	cutoff := now.Add(-spec.Window)
	var total float64
	for _, ev := range events {
		if ev.EventName != spec.EventName {
			continue
		}
		// Window is (cutoff, now]: exclude an event landing exactly on the cutoff
		// instant (lower bound is exclusive, per the doc) and any after now.
		if !ev.OccurredAt.After(cutoff) || ev.OccurredAt.After(now) {
			continue
		}
		switch spec.Aggregation {
		case AggCount:
			total++
		case AggSum:
			v, ok, err := numericField(ev.Data, spec.Field)
			if err != nil {
				return 0, err
			}
			if ok {
				total += v
			}
		default:
			return 0, fmt.Errorf("context-layer: unknown aggregation %q", string(spec.Aggregation))
		}
	}
	// A summed feature can overflow to ±Inf (or produce NaN) from extreme values;
	// fail loudly rather than letting a non-finite value flow into rule/expression
	// evaluation downstream, where it would silently corrupt comparisons.
	if math.IsInf(total, 0) || math.IsNaN(total) {
		return 0, fmt.Errorf("context-layer: feature %q aggregation overflowed to a non-finite value", string(spec.Aggregation))
	}
	return total, nil
}

// numericField reads a numeric top-level key from a JSON object. Absent reports
// ok=false (no contribution); present-but-non-numeric is an error.
func numericField(data json.RawMessage, field string) (float64, bool, error) {
	obj, err := toObject(data)
	if err != nil {
		return 0, false, fmt.Errorf("context-layer: event data must be a JSON object: %w", err)
	}
	raw, ok := obj[field]
	if !ok {
		return 0, false, nil
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false, fmt.Errorf("context-layer: field %q is not numeric: %w", field, err)
	}
	return v, true, nil
}
