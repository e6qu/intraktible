// SPDX-License-Identifier: AGPL-3.0-or-later

// Package outcomes records decision-linked business actuals with immutable
// lineage and correction history. Treatment and model facts are derived from
// the recorded decision; callers provide only the observed business fact.
package outcomes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	Stream        = "decision.outcomes"
	Collection    = "decision_outcomes"
	TypeRecorded  = "decision.outcome.recorded"
	TypeCorrected = "decision.outcome.corrected"
)

// Kind is the statistical interpretation of a value.
type Kind string

const (
	KindBinary     Kind = "binary"
	KindContinuous Kind = "continuous"
	KindMulticlass Kind = "multiclass"
)

func (k Kind) Valid() bool {
	return k == KindBinary || k == KindContinuous || k == KindMulticlass
}

// Source identifies the upstream observation and its provenance.
type Source struct {
	System   string `json:"system"`
	RecordID string `json:"record_id"`
	Lineage  string `json:"lineage,omitempty"`
}

// PredictionFact is recovered from immutable Predict-node traces.
type PredictionFact struct {
	Model        string  `json:"model"`
	ModelVersion int     `json:"model_version"`
	NodeID       string  `json:"node_id"`
	Probability  float64 `json:"probability"`
}

// TreatmentFact is recovered from the reached-treatment exposure.
type TreatmentFact struct {
	ExperimentID string `json:"experiment_id"`
	Cohort       int    `json:"cohort"`
	ArmKey       string `json:"arm_key"`
	ArmName      string `json:"arm_name"`
	ArmKind      string `json:"arm_kind"`
	Version      int    `json:"version"`
}

type exposureLineage struct {
	ExperimentID string `json:"experiment_id"`
	Cohort       int    `json:"cohort"`
	ArmKey       string `json:"arm_key"`
	ArmName      string `json:"arm_name"`
	ArmKind      string `json:"arm_kind"`
	Version      int    `json:"version"`
}

// RecordCommand is the caller-authored observed fact.
type RecordCommand struct {
	DecisionID            string    `json:"decision_id"`
	Key                   string    `json:"key"`
	Kind                  Kind      `json:"kind"`
	Value                 float64   `json:"value"`
	Category              string    `json:"category,omitempty"`
	Censored              bool      `json:"censored,omitempty"`
	EventTime             time.Time `json:"event_time"`
	ObservationWindowDays int       `json:"observation_window_days,omitempty"`
	Source                Source    `json:"source"`
	LabelVersion          string    `json:"label_version"`
}

func (c RecordCommand) validate(now time.Time) error {
	switch {
	case strings.TrimSpace(c.DecisionID) == "":
		return fmt.Errorf("outcomes: decision_id is required")
	case strings.TrimSpace(c.Key) == "":
		return fmt.Errorf("outcomes: key is required")
	case len(c.Key) > 200:
		return fmt.Errorf("outcomes: key exceeds 200 bytes")
	case !c.Kind.Valid():
		return fmt.Errorf("outcomes: invalid kind %q", c.Kind)
	case !c.Censored && c.Kind != KindMulticlass && (math.IsNaN(c.Value) || math.IsInf(c.Value, 0)):
		return fmt.Errorf("outcomes: value must be finite")
	case !c.Censored && c.Kind == KindBinary && c.Value != 0 && c.Value != 1:
		return fmt.Errorf("outcomes: binary value must be 0 or 1")
	case !c.Censored && c.Kind == KindMulticlass && strings.TrimSpace(c.Category) == "":
		return fmt.Errorf("outcomes: multiclass category is required")
	case c.Kind != KindMulticlass && strings.TrimSpace(c.Category) != "":
		return fmt.Errorf("outcomes: category is only valid for multiclass outcomes")
	case c.Censored && c.ObservationWindowDays <= 0:
		return fmt.Errorf("outcomes: censored outcomes require observation_window_days")
	case c.EventTime.IsZero():
		return fmt.Errorf("outcomes: event_time is required")
	case c.EventTime.After(now.Add(5 * time.Minute)):
		return fmt.Errorf("outcomes: event_time is more than five minutes in the future")
	case c.ObservationWindowDays < 0 || c.ObservationWindowDays > 3650:
		return fmt.Errorf("outcomes: observation_window_days must be between 0 and 3650")
	case strings.TrimSpace(c.Source.System) == "":
		return fmt.Errorf("outcomes: source.system is required")
	case strings.TrimSpace(c.Source.RecordID) == "":
		return fmt.Errorf("outcomes: source.record_id is required")
	case strings.TrimSpace(c.LabelVersion) == "":
		return fmt.Errorf("outcomes: label_version is required")
	default:
		return nil
	}
}

// Recorded is the complete derived event payload.
type Recorded struct {
	OutcomeID             string           `json:"outcome_id"`
	Revision              int              `json:"revision"`
	DecisionID            string           `json:"decision_id"`
	Key                   string           `json:"key"`
	Kind                  Kind             `json:"kind"`
	Value                 float64          `json:"value"`
	Category              string           `json:"category,omitempty"`
	Censored              bool             `json:"censored,omitempty"`
	EventTime             time.Time        `json:"event_time"`
	ObservationWindowDays int              `json:"observation_window_days,omitempty"`
	Source                Source           `json:"source"`
	LabelVersion          string           `json:"label_version"`
	FlowID                string           `json:"flow_id"`
	FlowVersion           int              `json:"flow_version"`
	Environment           string           `json:"environment"`
	Treatment             *TreatmentFact   `json:"treatment,omitempty"`
	Predictions           []PredictionFact `json:"predictions,omitempty"`
	IdempotencyKeyHash    string           `json:"idempotency_key_hash"`
	RequestHash           string           `json:"request_hash"`
}

// Corrected retains the original linkage and appends a new observed revision.
type Corrected struct {
	OutcomeID             string    `json:"outcome_id"`
	Revision              int       `json:"revision"`
	Value                 float64   `json:"value"`
	Category              string    `json:"category,omitempty"`
	Censored              bool      `json:"censored,omitempty"`
	EventTime             time.Time `json:"event_time"`
	ObservationWindowDays int       `json:"observation_window_days,omitempty"`
	Source                Source    `json:"source"`
	LabelVersion          string    `json:"label_version"`
	Reason                string    `json:"reason"`
	IdempotencyKeyHash    string    `json:"idempotency_key_hash"`
	RequestHash           string    `json:"request_hash"`
}

// Revision is one immutable value in the correction chain.
type Revision struct {
	Revision              int       `json:"revision"`
	Value                 float64   `json:"value"`
	Category              string    `json:"category,omitempty"`
	Censored              bool      `json:"censored,omitempty"`
	EventTime             time.Time `json:"event_time"`
	ObservationWindowDays int       `json:"observation_window_days,omitempty"`
	Source                Source    `json:"source"`
	LabelVersion          string    `json:"label_version"`
	Reason                string    `json:"reason,omitempty"`
	RecordedBy            string    `json:"recorded_by"`
	RecordedAt            time.Time `json:"recorded_at"`
}

// View holds the current value plus its full audit chain.
type View struct {
	OutcomeID   string           `json:"outcome_id"`
	DecisionID  string           `json:"decision_id"`
	Key         string           `json:"key"`
	Kind        Kind             `json:"kind"`
	FlowID      string           `json:"flow_id"`
	FlowVersion int              `json:"flow_version"`
	Environment string           `json:"environment"`
	Treatment   *TreatmentFact   `json:"treatment,omitempty"`
	Predictions []PredictionFact `json:"predictions,omitempty"`
	Current     Revision         `json:"current"`
	History     []Revision       `json:"history"`
}

// Handler records outcomes using the event log as the aggregate source.
type Handler struct {
	log   eventlog.Log
	store store.Store
	now   func() time.Time
	newID func() string
	mu    sync.Mutex
}

func NewHandler(log eventlog.Log, st store.Store) *Handler {
	return &Handler{
		log: log, store: st,
		now: func() time.Time { return time.Now().UTC() },
		newID: func() string {
			var bytes [16]byte
			if _, err := io.ReadFull(rand.Reader, bytes[:]); err != nil {
				panic("outcomes: crypto/rand unavailable: " + err.Error())
			}
			return hex.EncodeToString(bytes[:])
		},
	}
}

func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// Record derives immutable lineage and enforces retry-safe idempotency.
func (h *Handler) Record(ctx context.Context, id identity.Identity, command RecordCommand, idempotencyKey string) (Recorded, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return Recorded{}, eventlog.Envelope{}, err
	}
	if err := command.validate(h.now()); err != nil {
		return Recorded{}, eventlog.Envelope{}, err
	}
	keyHash, err := requireIdempotency(idempotencyKey)
	if err != nil {
		return Recorded{}, eventlog.Envelope{}, err
	}
	requestHash, err := hashJSON(command)
	if err != nil {
		return Recorded{}, eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if prior, found, err := h.byIdempotency(ctx, id, keyHash); err != nil {
		return Recorded{}, eventlog.Envelope{}, err
	} else if found {
		if prior.RequestHash != requestHash {
			return Recorded{}, eventlog.Envelope{}, fmt.Errorf("outcomes: idempotency key was already used with a different request")
		}
		return prior, eventlog.Envelope{}, nil
	}
	if prior, found, err := h.byDecisionMetric(ctx, id, command.DecisionID, command.Key); err != nil {
		return Recorded{}, eventlog.Envelope{}, err
	} else if found {
		if prior.RequestHash == requestHash {
			return prior, eventlog.Envelope{}, nil
		}
		return Recorded{}, eventlog.Envelope{}, fmt.Errorf(
			"outcomes: decision %q already has metric %q; correct outcome %q instead",
			command.DecisionID, command.Key, prior.OutcomeID,
		)
	}
	decision, ok, err := history.Read(ctx, h.store, id, command.DecisionID)
	if err != nil {
		return Recorded{}, eventlog.Envelope{}, err
	}
	if !ok {
		return Recorded{}, eventlog.Envelope{}, fmt.Errorf("outcomes: unknown decision %q", command.DecisionID)
	}
	if decision.Status != "completed" {
		return Recorded{}, eventlog.Envelope{}, fmt.Errorf("outcomes: decision %q is %s, want completed", command.DecisionID, decision.Status)
	}
	predictions, err := h.predictions(ctx, id, command.DecisionID)
	if err != nil {
		return Recorded{}, eventlog.Envelope{}, err
	}
	var treatment *TreatmentFact
	exposure, found, err := store.GetDoc[exposureLineage](
		ctx, h.store, "decision_experiment_exposures",
		store.Key(id.Org, id.Workspace, command.DecisionID),
	)
	if err != nil {
		return Recorded{}, eventlog.Envelope{}, err
	}
	if found {
		treatment = &TreatmentFact{
			ExperimentID: exposure.ExperimentID, Cohort: exposure.Cohort,
			ArmKey: exposure.ArmKey, ArmName: exposure.ArmName,
			ArmKind: exposure.ArmKind, Version: exposure.Version,
		}
	}
	payload := Recorded{
		OutcomeID: h.newID(), Revision: 1,
		DecisionID: command.DecisionID, Key: command.Key, Kind: command.Kind,
		Value: command.Value, Category: strings.TrimSpace(command.Category),
		Censored: command.Censored, EventTime: command.EventTime.UTC(),
		ObservationWindowDays: command.ObservationWindowDays,
		Source:                command.Source, LabelVersion: command.LabelVersion,
		FlowID: decision.FlowID, FlowVersion: decision.Version, Environment: decision.Environment,
		Treatment: treatment, Predictions: predictions,
		IdempotencyKeyHash: keyHash, RequestHash: requestHash,
	}
	event, err := eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeRecorded, h.now(),
		payload, factClaim(command.DecisionID, command.Key),
	)
	if errors.Is(err, eventlog.ErrConflict) {
		prior, found, foldErr := h.byDecisionMetric(ctx, id, command.DecisionID, command.Key)
		if foldErr != nil {
			return Recorded{}, eventlog.Envelope{}, foldErr
		}
		if !found || prior.RequestHash != requestHash {
			return Recorded{}, eventlog.Envelope{}, fmt.Errorf("outcomes: conflicting idempotency claim")
		}
		return prior, eventlog.Envelope{}, nil
	}
	return payload, event, err
}

// Correct appends a new current value while preserving every prior revision.
func (h *Handler) Correct(ctx context.Context, id identity.Identity, outcomeID string, command RecordCommand, reason, idempotencyKey string) (Corrected, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return Corrected{}, eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Corrected{}, eventlog.Envelope{}, fmt.Errorf("outcomes: correction reason is required")
	}
	keyHash, err := requireIdempotency(idempotencyKey)
	if err != nil {
		return Corrected{}, eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < 8; attempt++ {
		original, revision, ok, err := h.foldOne(ctx, id, outcomeID)
		if err != nil {
			return Corrected{}, eventlog.Envelope{}, err
		}
		if !ok {
			return Corrected{}, eventlog.Envelope{}, fmt.Errorf("outcomes: unknown outcome %q", outcomeID)
		}
		// Linkage and kind are immutable; correction requests carry them to make a
		// mismatched replay fail instead of silently retargeting the fact.
		command.DecisionID, command.Key, command.Kind = original.DecisionID, original.Key, original.Kind
		if err := command.validate(h.now()); err != nil {
			return Corrected{}, eventlog.Envelope{}, err
		}
		hashInput := struct {
			Command RecordCommand `json:"command"`
			Reason  string        `json:"reason"`
		}{command, strings.TrimSpace(reason)}
		requestHash, err := hashJSON(hashInput)
		if err != nil {
			return Corrected{}, eventlog.Envelope{}, err
		}
		if prior, found, err := h.correctionByIdempotency(ctx, id, keyHash); err != nil {
			return Corrected{}, eventlog.Envelope{}, err
		} else if found {
			if prior.OutcomeID != outcomeID || prior.RequestHash != requestHash {
				return Corrected{}, eventlog.Envelope{}, fmt.Errorf("outcomes: idempotency key used with a different correction")
			}
			return prior, eventlog.Envelope{}, nil
		}
		payload := Corrected{
			OutcomeID: outcomeID, Revision: revision + 1,
			Value: command.Value, Category: strings.TrimSpace(command.Category),
			Censored: command.Censored, EventTime: command.EventTime.UTC(),
			ObservationWindowDays: command.ObservationWindowDays,
			Source:                command.Source, LabelVersion: command.LabelVersion,
			Reason: strings.TrimSpace(reason), IdempotencyKeyHash: keyHash, RequestHash: requestHash,
		}
		event, err := eventlog.AppendJSONUnique(
			ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, TypeCorrected, h.now(),
			payload, revisionClaim(outcomeID, payload.Revision),
		)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return payload, event, err
	}
	return Corrected{}, eventlog.Envelope{}, fmt.Errorf("outcomes: correction contention exceeded retry limit")
}

func (h *Handler) predictions(ctx context.Context, id identity.Identity, decisionID string) ([]PredictionFact, error) {
	decisionEvents, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamDecisions, 0)
	if err != nil {
		return nil, err
	}
	modelEvents, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamModels, 0)
	if err != nil {
		return nil, err
	}
	var facts []PredictionFact
	for _, decisionEvent := range decisionEvents {
		if decisionEvent.Type != events.TypeNodeEvaluated {
			continue
		}
		var node events.NodeEvaluated
		if err := json.Unmarshal(decisionEvent.Payload, &node); err != nil {
			return nil, fmt.Errorf("outcomes: decode node event seq %d: %w", decisionEvent.Seq, err)
		}
		if node.DecisionID != decisionID || node.NodeType != events.NodePredict {
			continue
		}
		versions := make(map[string]int)
		for _, modelEvent := range modelEvents {
			if modelEvent.Seq > decisionEvent.Seq {
				break
			}
			if modelEvent.Type == events.TypeModelDefined {
				var defined events.ModelDefined
				if err := json.Unmarshal(modelEvent.Payload, &defined); err != nil {
					return nil, fmt.Errorf("outcomes: decode model event seq %d: %w", modelEvent.Seq, err)
				}
				versions[defined.Name]++
			}
		}
		var outputs map[string]map[string]any
		if err := json.Unmarshal(node.Output, &outputs); err != nil {
			return nil, fmt.Errorf("outcomes: decode prediction node %q: %w", node.NodeID, err)
		}
		for _, output := range outputs {
			name, _ := output["model"].(string)
			probability, ok := number(output["probability"])
			if name == "" || !ok || probability < 0 || probability > 1 {
				continue
			}
			facts = append(facts, PredictionFact{
				Model: name, ModelVersion: versions[name], NodeID: node.NodeID, Probability: probability,
			})
		}
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].NodeID != facts[j].NodeID {
			return facts[i].NodeID < facts[j].NodeID
		}
		return facts[i].Model < facts[j].Model
	})
	return facts, nil
}

func (h *Handler) byIdempotency(ctx context.Context, id identity.Identity, keyHash string) (Recorded, bool, error) {
	return findEventPayload(ctx, h.log, id, TypeRecorded, "record",
		func(payload Recorded) bool { return payload.IdempotencyKeyHash == keyHash })
}

func (h *Handler) byDecisionMetric(ctx context.Context, id identity.Identity, decisionID, key string) (Recorded, bool, error) {
	return findEventPayload(ctx, h.log, id, TypeRecorded, "record",
		func(payload Recorded) bool {
			return payload.DecisionID == decisionID && payload.Key == key
		})
}

func (h *Handler) correctionByIdempotency(ctx context.Context, id identity.Identity, keyHash string) (Corrected, bool, error) {
	return findEventPayload(ctx, h.log, id, TypeCorrected, "correction",
		func(payload Corrected) bool { return payload.IdempotencyKeyHash == keyHash })
}

func findEventPayload[T any](
	ctx context.Context,
	log eventlog.Log,
	id identity.Identity,
	eventType, label string,
	matches func(T) bool,
) (T, bool, error) {
	var zero T
	events, err := log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		return zero, false, err
	}
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		var payload T
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return zero, false, fmt.Errorf("outcomes: decode %s seq %d: %w", label, event.Seq, err)
		}
		if matches(payload) {
			return payload, true, nil
		}
	}
	return zero, false, nil
}

func (h *Handler) foldOne(ctx context.Context, id identity.Identity, outcomeID string) (Recorded, int, bool, error) {
	events, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		return Recorded{}, 0, false, err
	}
	var original Recorded
	revision := 0
	for _, event := range events {
		switch event.Type {
		case TypeRecorded:
			var payload Recorded
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Recorded{}, 0, false, err
			}
			if payload.OutcomeID == outcomeID {
				original, revision = payload, 1
			}
		case TypeCorrected:
			var payload Corrected
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Recorded{}, 0, false, err
			}
			if payload.OutcomeID == outcomeID {
				revision = payload.Revision
			}
		}
	}
	return original, revision, revision > 0, nil
}

func requireIdempotency(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("outcomes: Idempotency-Key is required")
	}
	if len(key) > 256 {
		return "", fmt.Errorf("outcomes: Idempotency-Key exceeds 256 bytes")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]), nil
}

func factClaim(decisionID, key string) string {
	return "outcome.fact\x00" + decisionID + "\x00" + key
}

func revisionClaim(outcomeID string, revision int) string {
	return "outcome.revision\x00" + outcomeID + "\x00" + strconv.Itoa(revision)
}

func hashJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("outcomes: hash request: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func number(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	case json.Number:
		n, err := value.Float64()
		return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
	default:
		return 0, false
	}
}

// Projector builds the current value and correction history.
type Projector struct{}

func (Projector) Name() string          { return "decision_outcomes" }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	switch event.Type {
	case TypeRecorded:
		var payload Recorded
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("outcomes: decode record seq %d: %w", event.Seq, err)
		}
		revision := Revision{
			Revision: payload.Revision, Value: payload.Value, Category: payload.Category,
			Censored: payload.Censored, EventTime: payload.EventTime,
			ObservationWindowDays: payload.ObservationWindowDays,
			Source:                payload.Source, LabelVersion: payload.LabelVersion,
			RecordedBy: event.Actor, RecordedAt: event.Time,
		}
		return store.PutDoc(ctx, st, Collection, store.Key(event.Org, event.Workspace, payload.OutcomeID), View{
			OutcomeID: payload.OutcomeID, DecisionID: payload.DecisionID,
			Key: payload.Key, Kind: payload.Kind, FlowID: payload.FlowID,
			FlowVersion: payload.FlowVersion, Environment: payload.Environment,
			Treatment: payload.Treatment, Predictions: payload.Predictions,
			Current: revision, History: []Revision{revision},
		})
	case TypeCorrected:
		var payload Corrected
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("outcomes: decode correction seq %d: %w", event.Seq, err)
		}
		key := store.Key(event.Org, event.Workspace, payload.OutcomeID)
		view, ok, err := store.GetDoc[View](ctx, st, Collection, key)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("outcomes: correction for unknown outcome %q", payload.OutcomeID)
		}
		if payload.Revision != view.Current.Revision+1 {
			return fmt.Errorf("outcomes: correction revision %d follows %d", payload.Revision, view.Current.Revision)
		}
		revision := Revision{
			Revision: payload.Revision, Value: payload.Value, Category: payload.Category,
			Censored: payload.Censored, EventTime: payload.EventTime,
			ObservationWindowDays: payload.ObservationWindowDays,
			Source:                payload.Source, LabelVersion: payload.LabelVersion, Reason: payload.Reason,
			RecordedBy: event.Actor, RecordedAt: event.Time,
		}
		view.Current = revision
		view.History = append(view.History, revision)
		return store.PutDoc(ctx, st, Collection, key, view)
	default:
		return nil
	}
}

// Read returns one outcome.
func Read(ctx context.Context, st store.Store, id identity.Identity, outcomeID string) (View, bool, error) {
	return store.GetDoc[View](ctx, st, Collection, store.Key(id.Org, id.Workspace, outcomeID))
}

// List returns tenant outcomes, optionally filtered by decision or metric key.
func List(ctx context.Context, st store.Store, id identity.Identity, decisionID, key string) ([]View, error) {
	return store.QueryDocs(ctx, st, Collection, store.Key(id.Org, id.Workspace, ""),
		func(view View) bool {
			return (decisionID == "" || view.DecisionID == decisionID) &&
				(key == "" || view.Key == key)
		},
		func(a, b View) bool {
			if !a.Current.EventTime.Equal(b.Current.EventTime) {
				return a.Current.EventTime.After(b.Current.EventTime)
			}
			return a.OutcomeID < b.OutcomeID
		},
	)
}
