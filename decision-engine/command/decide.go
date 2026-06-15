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

// FeatureProvider computes a Context Layer entity's feature values (name->value)
// for a tenant. The Context Layer supplies the implementation; defining the port
// here keeps the decision engine (built earlier) from importing it.
type FeatureProvider interface {
	Features(ctx context.Context, id identity.Identity, entityType, entityID string) (map[string]float64, error)
}

// EntityRef optionally points a decision at a Context Layer entity so its computed
// features are injected into the input under "features" (e.g. a Rule can test
// `features.txn_count_24h > 5`). An empty Type or ID means no features are added.
type EntityRef struct {
	Type string
	ID   string
}

// ConnectorProvider invokes a named Context Layer connector with params and returns
// its JSON response. As with FeatureProvider, the port lives here so the engine
// never imports the (later-built) Context Layer.
type ConnectorProvider interface {
	Fetch(ctx context.Context, id identity.Identity, connector string, params json.RawMessage) (json.RawMessage, error)
}

// DecideHandler executes published flows. It reads the flow registry read model
// for the version to run, evaluates it with the pure core, and records the
// decision as an event stream (started -> node-evaluated… -> completed/failed).
type DecideHandler struct {
	log        eventlog.Log
	store      store.Store
	now        func() time.Time
	newID      func() string
	roll       func() int // A/B routing draw in [0,100); recorded via the chosen version+variant
	features   FeatureProvider
	connectors ConnectorProvider
}

// DecideOption customizes a DecideHandler (used by tests to make A/B routing
// deterministic).
type DecideOption func(*DecideHandler)

// WithRoll overrides the A/B routing draw (a value in [0,100)).
func WithRoll(roll func() int) DecideOption { return func(h *DecideHandler) { h.roll = roll } }

// WithFeatures supplies the feature provider that resolves an EntityRef's
// features at decide time. Without it, EntityRef is ignored.
func WithFeatures(p FeatureProvider) DecideOption { return func(h *DecideHandler) { h.features = p } }

// WithConnectors supplies the connector provider that pre-resolves a flow's
// Connect nodes at decide time. Without it, a flow containing Connect nodes fails
// loudly (it cannot reach any connector backend).
func WithConnectors(p ConnectorProvider) DecideOption {
	return func(h *DecideHandler) { h.connectors = p }
}

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
func (h *DecideHandler) Decide(ctx context.Context, id identity.Identity, slug, env string, data map[string]any, ref EntityRef) (DecideResult, error) {
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

	// Features and connector calls are resolved at decide time and merged into the
	// input (under "features" and "connect"); the augmented input is what gets
	// recorded and executed, so the run stays replay-stable from the recorded data
	// alone and the pure core never performs I/O.
	data, err = h.injectFeatures(ctx, id, ref, data)
	if err != nil {
		return DecideResult{}, err
	}
	data, err = h.injectConnectors(ctx, id, version.Graph, data)
	if err != nil {
		return DecideResult{}, err
	}

	decisionID := h.newID()
	start := h.now()
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return DecideResult{}, fmt.Errorf("decision-engine: marshal data: %w", err)
	}
	if err := h.emit(ctx, id, events.TypeDecisionStarted, events.DecisionStarted{
		DecisionID: decisionID, FlowID: fv.FlowID, Slug: slug,
		Version: version.Version, Environment: env, Variant: variant,
		EntityType: ref.Type, EntityID: ref.ID, Data: dataJSON,
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
	var terminalType string
	var terminalPayload any
	var result DecideResult
	if run.Status == domain.StatusFailed {
		terminalType = events.TypeDecisionFailed
		terminalPayload = events.DecisionFailed{
			DecisionID: decisionID, FlowID: fv.FlowID, Version: version.Version, Variant: variant,
			NodeID: run.FailedNode, Error: run.Err, DurationMS: dur,
		}
		result = DecideResult{DecisionID: decisionID, Status: domain.StatusFailed, Error: run.Err}
	} else {
		outJSON, err := json.Marshal(run.Output)
		if err != nil {
			return DecideResult{}, fmt.Errorf("decision-engine: marshal output: %w", err)
		}
		terminalType = events.TypeDecisionCompleted
		terminalPayload = events.DecisionCompleted{
			DecisionID: decisionID, FlowID: fv.FlowID, Version: version.Version, Variant: variant,
			Output: outJSON, DurationMS: dur,
		}
		result = DecideResult{DecisionID: decisionID, Status: domain.StatusCompleted, Output: run.Output}
	}
	if err := h.emit(ctx, id, terminalType, terminalPayload); err != nil {
		return DecideResult{}, err
	}
	// A manual_review node that ran escalates to a case (consumed by the Case Manager).
	if err := h.emitEscalations(ctx, id, decisionID, dataJSON, run); err != nil {
		return DecideResult{}, err
	}
	return result, nil
}

// injectFeatures returns data augmented with a "features" map of the referenced
// entity's computed feature values. It is a no-op when no provider is configured
// or the reference is empty; a provider error fails the decision loudly.
func (h *DecideHandler) injectFeatures(ctx context.Context, id identity.Identity, ref EntityRef, data map[string]any) (map[string]any, error) {
	if h.features == nil || ref.Type == "" || ref.ID == "" {
		return data, nil
	}
	feats, err := h.features.Features(ctx, id, ref.Type, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("decision-engine: features for %s/%s: %w", ref.Type, ref.ID, err)
	}
	out := make(map[string]any, len(data)+1)
	for k, v := range data {
		out[k] = v
	}
	fm := make(map[string]any, len(feats))
	for k, v := range feats {
		fm[k] = v
	}
	out["features"] = fm
	return out, nil
}

// injectConnectors pre-resolves a flow's Connect nodes: it invokes each named
// connector with the current input as params and injects the responses under
// "connect" (keyed by each node's output). The fetch is the only I/O; doing it
// here keeps domain.Execute pure. When no provider is set this is a no-op and any
// Connect node will fail loudly during execution.
func (h *DecideHandler) injectConnectors(ctx context.Context, id identity.Identity, graph events.Graph, data map[string]any) (map[string]any, error) {
	specs, err := domain.ConnectSpecs(graph)
	if err != nil {
		return nil, err
	}
	if h.connectors == nil || len(specs) == 0 {
		return data, nil
	}
	params, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("decision-engine: marshal connector params: %w", err)
	}
	resolved := make(map[string]any, len(specs))
	for _, sp := range specs {
		resp, err := h.connectors.Fetch(ctx, id, sp.Connector, params)
		if err != nil {
			return nil, fmt.Errorf("decision-engine: connect node %q (connector %q): %w", sp.NodeID, sp.Connector, err)
		}
		var v any
		if err := json.Unmarshal(resp, &v); err != nil {
			return nil, fmt.Errorf("decision-engine: connect node %q response: %w", sp.NodeID, err)
		}
		resolved[sp.Output] = v
	}
	out := make(map[string]any, len(data)+1)
	for k, v := range data {
		out[k] = v
	}
	out["connect"] = resolved
	return out, nil
}

func (h *DecideHandler) emitEscalations(ctx context.Context, id identity.Identity, decisionID string, dataJSON json.RawMessage, run domain.Run) error {
	for _, res := range run.Results {
		if res.Type != events.NodeManualReview {
			continue
		}
		var out struct {
			CompanyName string `json:"company_name"`
			CaseType    string `json:"case_type"`
			SLADays     int    `json:"sla_days"`
		}
		if err := json.Unmarshal(res.Output, &out); err != nil {
			return fmt.Errorf("decision-engine: decode manual_review output: %w", err)
		}
		if err := h.emit(ctx, id, events.TypeManualReviewRequested, events.ManualReviewRequested{
			CaseID: h.newID(), DecisionID: decisionID, NodeID: res.NodeID,
			CompanyName: out.CompanyName, CaseType: out.CaseType, SLADays: out.SLADays, Context: dataJSON,
		}); err != nil {
			return err
		}
	}
	return nil
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
