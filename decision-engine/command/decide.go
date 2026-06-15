// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"crypto/rand"
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

// DecideHandler executes published flows. It reads the flow registry read model
// for the version to run, evaluates it with the pure core, and records the
// decision as an event stream (started -> node-evaluated… -> completed/failed).
type DecideHandler struct {
	log   eventlog.Log
	store store.Store
	now   func() time.Time
	newID func() string
	roll  func() int // A/B routing draw in [0,100); recorded via the chosen version+variant
}

// DecideOption customizes a DecideHandler (used by tests to make A/B routing
// deterministic).
type DecideOption func(*DecideHandler)

// WithRoll overrides the A/B routing draw (a value in [0,100)).
func WithRoll(roll func() int) DecideOption { return func(h *DecideHandler) { h.roll = roll } }

// NewDecideHandler builds a DecideHandler using the system clock and random id +
// routing sources. id generation, timing, and the routing draw are the only
// effects, and all are recorded (the chosen version and variant land in the
// DecisionStarted event, so replay is deterministic).
func NewDecideHandler(log eventlog.Log, st store.Store, opts ...DecideOption) *DecideHandler {
	h := &DecideHandler{
		log:   log,
		store: st,
		now:   func() time.Time { return time.Now().UTC() },
		newID: newID,
		roll:  rollPercent,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// rollPercent returns a near-uniform draw in [0,100) from a cryptographic source
// (avoids the weak-RNG SAST finding; routing is not security-sensitive). One byte
// is mapped to [0,99] via *100/256, so the conversion is a safe widening byte->int.
func rollPercent() int {
	var b [1]byte
	_, _ = rand.Read(b[:])
	return int(b[0]) * 100 / 256
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
	if !domain.ValidEnvironment(env) {
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
	versionNo, variant := h.resolveVersion(fv, env)
	version, ok := versionByNumber(fv, versionNo)
	if !ok {
		return DecideResult{}, fmt.Errorf("decision-engine: flow %q has no version %d", slug, versionNo)
	}

	decisionID := h.newID()
	start := h.now()
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return DecideResult{}, fmt.Errorf("decision-engine: marshal data: %w", err)
	}
	if err := h.emit(ctx, id, events.TypeDecisionStarted, events.DecisionStarted{
		DecisionID: decisionID, FlowID: fv.FlowID, Slug: slug,
		Version: version.Version, Environment: env, Variant: variant, Data: dataJSON,
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

// resolveVersion selects the version to run for an environment: the deployed
// champion (or the A/B challenger for ChallengerPct percent of traffic), falling
// back to the latest published version when nothing is deployed. It returns the
// version number and the variant; the choice is recorded so replay is stable.
func (h *DecideHandler) resolveVersion(fv flows.FlowView, env string) (int, string) {
	dep, ok := fv.Deployments[env]
	if !ok || dep.Version == 0 {
		return fv.Latest, "champion"
	}
	if dep.ChallengerVersion > 0 && h.roll() < dep.ChallengerPct {
		return dep.ChallengerVersion, "challenger"
	}
	return dep.Version, "champion"
}

func versionByNumber(fv flows.FlowView, n int) (flows.VersionView, bool) {
	for _, v := range fv.Versions {
		if v.Version == n {
			return v, true
		}
	}
	return flows.VersionView{}, false
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
