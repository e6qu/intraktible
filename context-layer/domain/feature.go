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
	AggCount         Aggregation = "count"          // number of matched events (no field)
	AggSum           Aggregation = "sum"            // Σ field
	AggAvg           Aggregation = "avg"            // mean field over events carrying it
	AggMin           Aggregation = "min"            // smallest field
	AggMax           Aggregation = "max"            // largest field
	AggLast          Aggregation = "last"           // field of the most recent matched event
	AggFirst         Aggregation = "first"          // field of the oldest matched event in-window
	AggCountDistinct Aggregation = "count_distinct" // number of distinct field values
)

var aggregations = map[Aggregation]bool{
	AggCount: true, AggSum: true, AggAvg: true, AggMin: true, AggMax: true,
	AggLast: true, AggFirst: true, AggCountDistinct: true,
}

// needsField reports whether an aggregation reads a data field (everything but count).
func (a Aggregation) needsField() bool { return a != AggCount }

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
	if !c.Aggregation.Valid() {
		return fmt.Errorf("context-layer: unknown aggregation %q (count|sum|avg|min|max|last|first|count_distinct)", string(c.Aggregation))
	}
	if c.Aggregation.needsField() && strings.TrimSpace(c.Field) == "" {
		return fmt.Errorf("context-layer: %s features require a field", c.Aggregation)
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

// Compute folds events into the feature's value as of asOf: it keeps events whose
// name matches and whose occurrence falls in (asOf-window, asOf], then applies the
// aggregation. Because the upper bound is asOf (not the wall clock), passing a past
// instant yields the POINT-IN-TIME value the feature had then — the basis for
// reproducing or backtesting a decision. A matching event missing the field
// contributes nothing (count still counts it); a present-but-non-numeric field fails
// loudly for numeric aggregations. count_distinct accepts any JSON value.
func Compute(spec FeatureSpec, events []FeatureInput, asOf time.Time) (float64, error) {
	res, err := ComputeDetailed(spec, events, asOf)
	if err != nil {
		return 0, err
	}
	return res.Value, nil
}

// FeatureResult is a computed value plus its lineage: how many events fed it and the
// oldest in-window event's instant (Expiry = that instant + window is when the value
// would next change absent new events — the basis for a correct materialized cache).
type FeatureResult struct {
	Value       float64
	EventCount  int
	OldestInWin time.Time
	HasInWindow bool
}

// ComputeDetailed is Compute plus the lineage a feature store needs to cache the
// value and know when it expires.
func ComputeDetailed(spec FeatureSpec, events []FeatureInput, asOf time.Time) (FeatureResult, error) {
	// Validate the aggregation as a PRECONDITION, not inside the per-event loop: with
	// zero matching events the loop body never runs, so an invalid aggregation would
	// otherwise silently return 0 instead of erroring (define-time validation already
	// guards this for stored specs; this hardens a hand-constructed one).
	if !spec.Aggregation.Valid() {
		return FeatureResult{}, fmt.Errorf("context-layer: unknown aggregation %q", string(spec.Aggregation))
	}
	cutoff := asOf.Add(-spec.Window)
	var res FeatureResult
	var sum, extreme float64
	var fieldCount int           // events carrying a present, valid field
	var last, first FeatureInput // most-recent / oldest matched event
	var lastSet, firstSet bool
	distinct := map[string]struct{}{}
	for _, ev := range events {
		if ev.EventName != spec.EventName {
			continue
		}
		// Window is (cutoff, asOf]: exclude an event on the cutoff instant (lower bound
		// exclusive) and any after asOf (point-in-time upper bound).
		if !ev.OccurredAt.After(cutoff) || ev.OccurredAt.After(asOf) {
			continue
		}
		res.EventCount++
		if !res.HasInWindow || ev.OccurredAt.Before(res.OldestInWin) {
			res.OldestInWin, res.HasInWindow = ev.OccurredAt, true
		}
		if !firstSet || ev.OccurredAt.Before(first.OccurredAt) {
			first, firstSet = ev, true
		}
		if !lastSet || !ev.OccurredAt.Before(last.OccurredAt) {
			last, lastSet = ev, true
		}
		if spec.Aggregation == AggCountDistinct {
			raw, ok, err := rawField(ev.Data, spec.Field)
			if err != nil {
				return FeatureResult{}, err
			}
			if ok {
				distinct[raw] = struct{}{}
			}
			continue
		}
		if !spec.Aggregation.needsField() {
			continue
		}
		v, ok, err := numericField(ev.Data, spec.Field)
		if err != nil {
			return FeatureResult{}, err
		}
		if !ok {
			continue
		}
		switch spec.Aggregation {
		case AggSum, AggAvg:
			sum += v
		case AggMin:
			if fieldCount == 0 || v < extreme {
				extreme = v
			}
		case AggMax:
			if fieldCount == 0 || v > extreme {
				extreme = v
			}
		}
		fieldCount++
	}
	switch spec.Aggregation {
	case AggCount:
		res.Value = float64(res.EventCount)
	case AggSum:
		res.Value = sum
	case AggAvg:
		if fieldCount > 0 {
			res.Value = sum / float64(fieldCount)
		}
	case AggMin, AggMax:
		res.Value = extreme // 0 when no event carried the field
	case AggCountDistinct:
		res.Value = float64(len(distinct))
	case AggLast:
		if v, err := fieldOrZero(last, lastSet, spec.Field); err != nil {
			return FeatureResult{}, err
		} else {
			res.Value = v
		}
	case AggFirst:
		if v, err := fieldOrZero(first, firstSet, spec.Field); err != nil {
			return FeatureResult{}, err
		} else {
			res.Value = v
		}
	}
	// An aggregation can overflow to ±Inf (or produce NaN) from extreme values; fail
	// loudly rather than letting a non-finite value flow into rule/expression
	// evaluation downstream, where it would silently corrupt comparisons.
	if math.IsInf(res.Value, 0) || math.IsNaN(res.Value) {
		return FeatureResult{}, fmt.Errorf("context-layer: feature %q aggregation overflowed to a non-finite value", string(spec.Aggregation))
	}
	return res, nil
}

// fieldOrZero reads the numeric field of a chosen event (last/first), returning 0 when
// no event matched or the field is absent.
func fieldOrZero(ev FeatureInput, set bool, field string) (float64, error) {
	if !set {
		return 0, nil
	}
	v, ok, err := numericField(ev.Data, field)
	if err != nil || !ok {
		return 0, err
	}
	return v, nil
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

// rawField reads a top-level key as its canonical JSON text, for count_distinct
// (which counts distinct values of any type, not just numbers). Absent → ok=false.
func rawField(data json.RawMessage, field string) (string, bool, error) {
	obj, err := toObject(data)
	if err != nil {
		return "", false, fmt.Errorf("context-layer: event data must be a JSON object: %w", err)
	}
	raw, ok := obj[field]
	if !ok {
		return "", false, nil
	}
	// Canonicalize so 1 and 1.0, or "x" spacing, collapse to one distinct value.
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false, fmt.Errorf("context-layer: field %q is not valid JSON: %w", field, err)
	}
	canon, err := json.Marshal(v)
	if err != nil {
		return "", false, err
	}
	return string(canon), true, nil
}
