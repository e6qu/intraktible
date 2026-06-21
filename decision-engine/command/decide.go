// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Decide-path error taxonomy. A caller (the HTTP layer) distinguishes a client
// mistake from a missing resource from an infrastructure failure by errors.Is
// against these sentinels, instead of mapping everything to one status code. An
// unwrapped error is an infrastructure failure (HTTP 500).
var (
	// ErrBadRequest is a malformed request: a bad environment, or input that
	// violates the flow's contract. (HTTP 400.)
	ErrBadRequest = errors.New("decision-engine: bad request")
	// ErrNotFound is a missing addressable resource: an unknown flow slug, or a
	// flow with no such (or no published) version. (HTTP 404.)
	ErrNotFound = errors.New("decision-engine: not found")
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

// AgentProvider runs a named Agent Manager agent against a prompt and returns its
// output as JSON. The port lives here so the engine never imports the Agent
// Manager; a failed run is returned as an error so the decision fails loudly.
type AgentProvider interface {
	RunAgent(ctx context.Context, id identity.Identity, agent, prompt string) (json.RawMessage, error)
}

// ModelProvider evaluates a named predictive model from the registry over the
// decision input and returns its prediction as JSON. The port lives here so the
// engine never imports the models registry's command surface; a missing model or a
// bad feature is returned as an error so the decision fails loudly.
type ModelProvider interface {
	Predict(ctx context.Context, id identity.Identity, model string, features map[string]any) (json.RawMessage, error)
}

// PIISealer crypto-shreds the configured PII fields of a recorded decision under
// the subject (the referenced entity), so a later erasure of that subject makes
// the recorded input/output PII unrecoverable. The port lives here so the engine
// imports neither the erasure vault nor the privacy config directly; the
// composition root supplies the adapter.
type PIISealer interface {
	SealPII(ctx context.Context, id identity.Identity, subject string, doc json.RawMessage) (json.RawMessage, error)
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
	agentsP    AgentProvider
	models     ModelProvider
	sealer     PIISealer
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

// WithAgents supplies the agent provider that pre-resolves a flow's AI nodes at
// decide time. Without it, a flow containing AI nodes fails loudly.
func WithAgents(p AgentProvider) DecideOption {
	return func(h *DecideHandler) { h.agentsP = p }
}

// WithModels supplies the model provider that pre-resolves a flow's Predict nodes
// at decide time. Without it, a flow containing Predict nodes fails loudly.
func WithModels(p ModelProvider) DecideOption {
	return func(h *DecideHandler) { h.models = p }
}

// WithPIISealer supplies the sealer that crypto-shreds a recorded decision's PII
// fields under the referenced entity subject. Without it (or without an entity
// ref), decisions are recorded in the clear as before.
func WithPIISealer(s PIISealer) DecideOption {
	return func(h *DecideHandler) { h.sealer = s }
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
	// Disposition is the operational policy's automated outcome (approve|decline|
	// refer), empty when no policy is bound to the flow. DispositionReason is the
	// matched rule's description (or why it referred).
	Disposition       string
	DispositionReason string
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
		return DecideResult{}, fmt.Errorf("%w: invalid environment %q (sandbox|staging|production)", ErrBadRequest, env)
	}
	fv, ok, err := flows.BySlug(ctx, h.store, id, slug)
	if err != nil {
		return DecideResult{}, err
	}
	if !ok {
		return DecideResult{}, fmt.Errorf("%w: unknown flow %q", ErrNotFound, slug)
	}
	if len(fv.Versions) == 0 {
		return DecideResult{}, fmt.Errorf("%w: flow %q has no published version", ErrNotFound, slug)
	}
	versionNo, variant := h.resolveVersion(fv, env)
	version, ok := versionByNumber(fv, versionNo)
	if !ok {
		return DecideResult{}, fmt.Errorf("%w: flow %q has no version %d", ErrNotFound, slug, versionNo)
	}

	// Pre-approval fast path: a valid pre-approval for the entity is honored
	// instantly — approve/decline with the stored terms, skipping the flow run.
	if ref.Type != "" && ref.ID != "" {
		res, honored, err := h.honorPreApproval(ctx, id, fv, version.Version, env, variant, slug, ref, data)
		if err != nil {
			return DecideResult{}, err
		}
		if honored {
			return res, nil
		}
	}

	// Validate the caller's input against the version's contract before anything
	// is injected or recorded — a contract violation is a bad request, not a
	// recorded decision.
	if err := domain.ValidateInput(version.InputSchema, data); err != nil {
		return DecideResult{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
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
	data, err = h.injectAI(ctx, id, version.Graph, data)
	if err != nil {
		return DecideResult{}, err
	}
	data, err = h.injectPredictions(ctx, id, version.Graph, data)
	if err != nil {
		return DecideResult{}, err
	}

	decisionID := h.newID()
	start := h.now()
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return DecideResult{}, fmt.Errorf("decision-engine: marshal data: %w", err)
	}
	// Seal the recorded input's PII under the entity subject (a no-op without a
	// sealer or entity ref). Execution already ran on the plaintext `data` map, so
	// sealing the recorded copy never affects the decision.
	dataJSON, err = h.sealPII(ctx, id, ref, dataJSON)
	if err != nil {
		return DecideResult{}, err
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
		// Seal PII in each node's output too — node outputs echo input-derived PII
		// (assignment/rule/table targets, code merges, manual_review fields), so an
		// unsealed trace would survive a crypto-shred erasure that the input/output
		// sealing makes unrecoverable.
		nodeOut, err := h.sealPII(ctx, id, ref, r.Output)
		if err != nil {
			return DecideResult{}, err
		}
		if err := h.emit(ctx, id, events.TypeNodeEvaluated, events.NodeEvaluated{
			DecisionID: decisionID, NodeID: r.NodeID, NodeType: r.Type, Output: nodeOut,
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
		result = DecideResult{DecisionID: decisionID, Status: string(domain.StatusFailed), Error: run.Err}
	} else {
		outJSON, err := json.Marshal(run.Output)
		if err != nil {
			return DecideResult{}, fmt.Errorf("decision-engine: marshal output: %w", err)
		}
		outJSON, err = h.sealPII(ctx, id, ref, outJSON)
		if err != nil {
			return DecideResult{}, err
		}
		// Operational policy: assign a disposition over the output. A store error is
		// fatal; a missing policy yields no disposition; a policy eval error refers
		// (routes to a human) rather than failing an otherwise-completed decision.
		disp, err := h.applyPolicy(ctx, id, slug, run.Output)
		if err != nil {
			return DecideResult{}, err
		}
		terminalType = events.TypeDecisionCompleted
		terminalPayload = events.DecisionCompleted{
			DecisionID: decisionID, FlowID: fv.FlowID, Version: version.Version, Variant: variant,
			Output: outJSON, DurationMS: dur,
			Disposition: disp.disposition, DispositionCode: disp.code, DispositionReason: disp.reason,
			PolicyID: disp.policyID, PolicyVersion: disp.policyVersion,
		}
		result = DecideResult{
			DecisionID: decisionID, Status: string(domain.StatusCompleted), Output: run.Output,
			Disposition: disp.disposition, DispositionReason: disp.reason,
		}
	}
	if err := h.emit(ctx, id, terminalType, terminalPayload); err != nil {
		return DecideResult{}, err
	}
	// A manual_review node that ran escalates to a case (consumed by the Case Manager).
	if err := h.emitEscalations(ctx, id, decisionID, ref, dataJSON, run); err != nil {
		return DecideResult{}, err
	}
	// A shadow version, if configured for this environment, is evaluated over the
	// same input for divergence analysis — its outcome never affects the result.
	if err := h.runShadow(ctx, id, fv, env, decisionID, version.Version, data, run); err != nil {
		return DecideResult{}, err
	}
	return result, nil
}

// runShadow evaluates the environment's shadow version (if any) over the same
// input as the live decision and records the comparison. The shadow reuses the
// live input (so its features/connector/AI context match the request); a shadow
// node needing input the live graph did not inject simply fails in the shadow
// run and is recorded as such. The shadow's outcome never affects the caller's
// result; only a failure to record the comparison event is returned.
func (h *DecideHandler) runShadow(ctx context.Context, id identity.Identity, fv flows.FlowView, env, decisionID string, liveVersion int, data map[string]any, live domain.Run) error {
	shadowVer := fv.Shadows[env]
	if shadowVer == 0 || shadowVer == liveVersion {
		return nil
	}
	ev := events.ShadowEvaluated{
		DecisionID: decisionID, FlowID: fv.FlowID, Environment: env,
		LiveVersion: liveVersion, ShadowVersion: shadowVer, LiveStatus: string(live.Status),
	}
	sv, ok := versionByNumber(fv, shadowVer)
	if !ok {
		ev.ShadowError = fmt.Sprintf("shadow version %d not found", shadowVer)
	} else {
		srun := domain.Execute(sv.Graph, data)
		ev.ShadowStatus = string(srun.Status)
		if srun.Status == domain.StatusFailed {
			ev.ShadowError = srun.Err
		}
		ev.Matched = live.Status == domain.StatusCompleted &&
			srun.Status == domain.StatusCompleted &&
			reflect.DeepEqual(live.Output, srun.Output)
	}
	return h.emit(ctx, id, events.TypeShadowEvaluated, ev)
}

// sealPII crypto-shreds the configured PII fields of a recorded document under
// the referenced entity subject. A no-op without a sealer or an entity reference.
func (h *DecideHandler) sealPII(ctx context.Context, id identity.Identity, ref EntityRef, doc json.RawMessage) (json.RawMessage, error) {
	if h.sealer == nil || ref.Type == "" || ref.ID == "" {
		return doc, nil
	}
	return h.sealer.SealPII(ctx, id, ref.Type+"/"+ref.ID, doc)
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

// injectAI pre-resolves a flow's AI nodes: it runs each named agent (with the
// node's literal prompt, or the current input serialized when none is set) and
// injects the outputs under "ai" (keyed by each node's output). As with
// connectors, this is the only I/O, keeping domain.Execute pure; without a
// provider it is a no-op and any AI node fails loudly during execution.
func (h *DecideHandler) injectAI(ctx context.Context, id identity.Identity, graph events.Graph, data map[string]any) (map[string]any, error) {
	specs, err := domain.AISpecs(graph)
	if err != nil {
		return nil, err
	}
	if h.agentsP == nil || len(specs) == 0 {
		return data, nil
	}
	resolved := make(map[string]any, len(specs))
	for _, sp := range specs {
		prompt := sp.Prompt
		if prompt == "" {
			b, err := json.Marshal(data)
			if err != nil {
				return nil, fmt.Errorf("decision-engine: marshal ai prompt: %w", err)
			}
			prompt = string(b)
		}
		resp, err := h.agentsP.RunAgent(ctx, id, sp.Agent, prompt)
		if err != nil {
			return nil, fmt.Errorf("decision-engine: ai node %q (agent %q): %w", sp.NodeID, sp.Agent, err)
		}
		var v any
		if err := json.Unmarshal(resp, &v); err != nil {
			return nil, fmt.Errorf("decision-engine: ai node %q response: %w", sp.NodeID, err)
		}
		resolved[sp.Output] = v
	}
	out := make(map[string]any, len(data)+1)
	for k, v := range data {
		out[k] = v
	}
	out["ai"] = resolved
	return out, nil
}

// injectPredictions pre-resolves a flow's Predict nodes: it evaluates each named
// model over the current input (so a model can read features/connect/ai resolved
// above) and injects the predictions under "predict" (keyed by each node's output).
// Evaluation is the only "effect"; doing it here keeps domain.Execute pure. Without
// a provider it is a no-op and any Predict node fails loudly during execution.
func (h *DecideHandler) injectPredictions(ctx context.Context, id identity.Identity, graph events.Graph, data map[string]any) (map[string]any, error) {
	specs, err := domain.PredictSpecs(graph)
	if err != nil {
		return nil, err
	}
	if h.models == nil || len(specs) == 0 {
		return data, nil
	}
	resolved := make(map[string]any, len(specs))
	for _, sp := range specs {
		resp, err := h.models.Predict(ctx, id, sp.Model, data)
		if err != nil {
			return nil, fmt.Errorf("decision-engine: predict node %q (model %q): %w", sp.NodeID, sp.Model, err)
		}
		// Tag the prediction with the model name so the read side can attribute it
		// (drift monitoring); downstream still reads predict.<output>.{score,probability}.
		// "model" is a RESERVED attribution key: it is set authoritatively from the
		// node's configured model and intentionally overrides any "model" field a
		// provider returns — drift attribution must not be spoofable by model output.
		v := map[string]any{}
		if err := json.Unmarshal(resp, &v); err != nil {
			return nil, fmt.Errorf("decision-engine: predict node %q response: %w", sp.NodeID, err)
		}
		v["model"] = sp.Model
		resolved[sp.Output] = v
	}
	out := make(map[string]any, len(data)+1)
	for k, v := range data {
		out[k] = v
	}
	out["predict"] = resolved
	return out, nil
}

func (h *DecideHandler) emitEscalations(ctx context.Context, id identity.Identity, decisionID string, ref EntityRef, dataJSON json.RawMessage, run domain.Run) error {
	for _, res := range run.Results {
		if res.Type != events.NodeManualReview {
			continue
		}
		// Seal the node output before extracting the case labels: company_name /
		// case_type are input-derived, so if a tenant configures them as PII they must
		// be crypto-shred-erasable like every other recorded surface. Reading them from
		// the SEALED output (vs the raw run output) keeps the escalation event
		// consistent with NodeEvaluated — a sealed field becomes a placeholder, never
		// surviving in cleartext.
		sealedOut, err := h.sealPII(ctx, id, ref, res.Output)
		if err != nil {
			return err
		}
		var out struct {
			CompanyName json.RawMessage `json:"company_name"`
			CaseType    json.RawMessage `json:"case_type"`
			SLADays     int             `json:"sla_days"`
		}
		if err := json.Unmarshal(sealedOut, &out); err != nil {
			return fmt.Errorf("decision-engine: decode manual_review output: %w", err)
		}
		if err := h.emit(ctx, id, events.TypeManualReviewRequested, events.ManualReviewRequested{
			CaseID: h.newID(), DecisionID: decisionID, NodeID: res.NodeID,
			CompanyName: labelFromSealed(out.CompanyName), CaseType: labelFromSealed(out.CaseType),
			SLADays: out.SLADays, Context: dataJSON,
		}); err != nil {
			return err
		}
	}
	return nil
}

// labelFromSealed extracts a manual_review case label from the SEALED node output: a
// plain JSON string passes through, but a value sealed into a PII envelope (an
// object) becomes a "[sealed]" placeholder, so cleartext PII never lands in the
// escalation event (and the label stays a display string for the Case Manager).
func labelFromSealed(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return "[sealed]"
}

// honorPreApproval serves a decision instantly from a valid pre-approval for the
// entity: it records a completed decision whose output is the stored terms and
// whose disposition is the pre-approval's (skipping the flow), plus a
// PreApprovalHonored effect. It returns honored=false when there is none.
func (h *DecideHandler) honorPreApproval(ctx context.Context, id identity.Identity, fv flows.FlowView, version int, env, variant, slug string, ref EntityRef, data map[string]any) (DecideResult, bool, error) {
	pa, ok, err := preapproval.ActiveFor(ctx, h.store, id, ref.Type, ref.ID, h.now())
	if err != nil || !ok {
		return DecideResult{}, false, err
	}
	terms := map[string]any{}
	if len(pa.Terms) > 0 {
		if err := json.Unmarshal(pa.Terms, &terms); err != nil {
			return DecideResult{}, false, fmt.Errorf("decision-engine: pre-approval terms: %w", err)
		}
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return DecideResult{}, false, fmt.Errorf("decision-engine: marshal data: %w", err)
	}
	// Seal recorded PII under the entity subject, same as the normal decide path —
	// the fast path always has an entity ref, so skipping it would leave
	// pre-approved entities' decision PII un-erasable.
	dataJSON, err = h.sealPII(ctx, id, ref, dataJSON)
	if err != nil {
		return DecideResult{}, false, err
	}
	outJSON, err := json.Marshal(terms)
	if err != nil {
		return DecideResult{}, false, fmt.Errorf("decision-engine: marshal terms: %w", err)
	}
	outJSON, err = h.sealPII(ctx, id, ref, outJSON)
	if err != nil {
		return DecideResult{}, false, err
	}
	decisionID := h.newID()
	if err := h.emit(ctx, id, events.TypeDecisionStarted, events.DecisionStarted{
		DecisionID: decisionID, FlowID: fv.FlowID, Slug: slug, Version: version, Environment: env,
		Variant: variant, EntityType: ref.Type, EntityID: ref.ID, Data: dataJSON,
	}); err != nil {
		return DecideResult{}, false, err
	}
	if err := h.emit(ctx, id, events.TypeDecisionCompleted, events.DecisionCompleted{
		DecisionID: decisionID, FlowID: fv.FlowID, Version: version, Variant: variant,
		Output: outJSON, DurationMS: 0,
		Disposition: pa.Disposition, DispositionReason: "pre-approval honored", PreApprovalID: pa.PreApprovalID,
	}); err != nil {
		return DecideResult{}, false, err
	}
	if err := h.appendStream(ctx, id, preapproval.StreamPreApprovals, preapproval.TypeHonored, preapproval.Honored{
		PreApprovalID: pa.PreApprovalID, EntityType: ref.Type, EntityID: ref.ID, DecisionID: decisionID,
	}); err != nil {
		return DecideResult{}, false, err
	}
	return DecideResult{
		DecisionID: decisionID, Status: string(domain.StatusCompleted), Output: terms,
		Disposition: pa.Disposition, DispositionReason: "pre-approval honored",
	}, true, nil
}

// dispositionResult is the policy outcome the decide path records on a completed
// decision (internal; flattened onto DecisionCompleted + DecideResult).
type dispositionResult struct {
	disposition   string
	code          string
	reason        string
	policyID      string
	policyVersion int
}

// applyPolicy resolves the active policy for the flow and assigns a disposition
// over the output. No policy bound → empty disposition; a policy evaluation error
// → refer (with the error as the reason) so a completed decision is never failed
// by a policy problem; only a store error is returned.
func (h *DecideHandler) applyPolicy(ctx context.Context, id identity.Identity, slug string, output map[string]any) (dispositionResult, error) {
	pv, ver, ok, err := policy.ActiveForFlow(ctx, h.store, id, slug)
	if err != nil {
		return dispositionResult{}, err
	}
	if !ok {
		return dispositionResult{}, nil
	}
	res := dispositionResult{policyID: pv.PolicyID, policyVersion: ver.Version}
	// A policy that cannot evaluate (e.g. references a field the output lacks)
	// refers to a human rather than failing the completed decision.
	if out, applyErr := ver.Spec.Apply(output); applyErr != nil {
		res.disposition, res.reason = string(policy.Refer), "policy: "+applyErr.Error()
	} else {
		res.disposition, res.code, res.reason = string(out.Disposition), out.Code, out.Description
	}
	return res, nil
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
	return h.appendStream(ctx, id, events.StreamDecisions, typ, payload)
}

// appendStream marshals and appends a payload to a named stream (decision events
// go to StreamDecisions; a honored pre-approval also writes to its own stream).
func (h *DecideHandler) appendStream(ctx context.Context, id identity.Identity, stream, typ string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("decision-engine: marshal %s: %w", typ, err)
	}
	_, err = h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    stream,
		Type:      typ,
		Time:      h.now(),
		Payload:   b,
	})
	return err
}
