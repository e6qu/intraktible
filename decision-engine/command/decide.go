// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// validEnvironments are the decide environments (mirrors API-key scopes).
var validEnvironments = map[string]bool{"sandbox": true, "production": true}

// DecideHandler executes published flows. It reads the flow registry read model
// for the version to run, evaluates it with the pure core, and records the
// decision as an event stream (started -> node-evaluated… -> completed/failed).
type DecideHandler struct {
	log   eventlog.Log
	store store.Store
	now   func() time.Time
	newID func() string
}

// NewDecideHandler builds a DecideHandler using the system clock and a random id
// source. id generation and timing are the only effects, and both are recorded.
func NewDecideHandler(log eventlog.Log, st store.Store) *DecideHandler {
	return &DecideHandler{
		log:   log,
		store: st,
		now:   func() time.Time { return time.Now().UTC() },
		newID: newID,
	}
}

// DecideResult is the decide response: the recorded decision id, the run status,
// the flow's output (on success), and the failure reason (on failure).
type DecideResult struct {
	DecisionID string
	Status     string
	Output     map[string]any
	Error      string
}

// Decide runs the latest published version of the flow with the given slug in
// the given environment against data. A run that errors during evaluation is a
// recorded "failed" decision (returned with Status failed), not an API error;
// only infrastructure/lookup problems return an error.
func (h *DecideHandler) Decide(ctx context.Context, id identity.Identity, slug, env string, data map[string]any) (DecideResult, error) {
	if err := id.Valid(); err != nil {
		return DecideResult{}, err
	}
	if !validEnvironments[env] {
		return DecideResult{}, fmt.Errorf("decision-engine: invalid environment %q (sandbox|production)", env)
	}
	fv, ok, err := flows.BySlug(ctx, h.store, id, slug)
	if err != nil {
		return DecideResult{}, err
	}
	if !ok {
		return DecideResult{}, fmt.Errorf("decision-engine: unknown flow %q", slug)
	}
	if len(fv.Versions) == 0 {
		return DecideResult{}, fmt.Errorf("decision-engine: flow %q has no published version", slug)
	}
	version := fv.Versions[len(fv.Versions)-1] // MVP: latest; env-pinning/A-B later

	decisionID := h.newID()
	start := h.now()
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return DecideResult{}, fmt.Errorf("decision-engine: marshal data: %w", err)
	}
	if err := h.emit(ctx, id, events.TypeDecisionStarted, events.DecisionStarted{
		DecisionID: decisionID, FlowID: fv.FlowID, Slug: slug,
		Version: version.Version, Environment: env, Data: dataJSON,
	}); err != nil {
		return DecideResult{}, err
	}

	run := domain.Execute(version.Graph, data)
	for _, r := range run.Results {
		if err := h.emit(ctx, id, events.TypeNodeEvaluated, events.NodeEvaluated{
			DecisionID: decisionID, NodeID: r.NodeID, NodeType: r.Type, Output: r.Output,
		}); err != nil {
			return DecideResult{}, err
		}
	}

	dur := h.now().Sub(start).Milliseconds()
	if run.Status == domain.StatusFailed {
		if err := h.emit(ctx, id, events.TypeDecisionFailed, events.DecisionFailed{
			DecisionID: decisionID, NodeID: run.FailedNode, Error: run.Err, DurationMS: dur,
		}); err != nil {
			return DecideResult{}, err
		}
		return DecideResult{DecisionID: decisionID, Status: domain.StatusFailed, Error: run.Err}, nil
	}

	outJSON, err := json.Marshal(run.Output)
	if err != nil {
		return DecideResult{}, fmt.Errorf("decision-engine: marshal output: %w", err)
	}
	if err := h.emit(ctx, id, events.TypeDecisionCompleted, events.DecisionCompleted{
		DecisionID: decisionID, Output: outJSON, DurationMS: dur,
	}); err != nil {
		return DecideResult{}, err
	}
	return DecideResult{DecisionID: decisionID, Status: domain.StatusCompleted, Output: run.Output}, nil
}

func (h *DecideHandler) emit(ctx context.Context, id identity.Identity, typ string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("decision-engine: marshal %s: %w", typ, err)
	}
	_, err = h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamDecisions,
		Type:      typ,
		Time:      h.now(),
		Payload:   b,
	})
	return err
}
