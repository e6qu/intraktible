// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/platform/entity"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
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
	Features(ctx context.Context, id identity.Identity, ref entity.Ref) (map[string]float64, error)
}

// EntityRef optionally points a decision at a Context Layer entity so its computed
// features are injected into the input under "features" (e.g. a Rule can test
// `features.txn_count_24h > 5`). An empty Type or ID means no features are added.
// It is the shared, branded entity.Ref so its (type, id) can't be transposed.
type EntityRef = entity.Ref

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
// bad feature is returned as an error so the decision fails loudly. ApprovedForServing
// is part of the contract — not an optional capability the decide path feels for —
// so the four-eyes gate can never be silently skipped by a provider that lacks it.
type ModelProvider interface {
	Predict(ctx context.Context, id identity.Identity, model string, features map[string]any) (json.RawMessage, error)
	// ApprovedForServing reports whether the model's current version has four-eyes
	// approval; the decide path refuses an unapproved model outside the sandbox.
	ApprovedForServing(ctx context.Context, id identity.Identity, model string) (bool, error)
}

// ConsentGate is the decide path's purpose-limitation hook: it records a subject's
// consent captured in the application input, and checks whether the subject has
// active consent for a purpose before a data pull (a Connect node) that requires it —
// FCRA-style permissible-purpose enforcement. The engine never imports the consent
// ledger; the composition root supplies the adapter. The subject is the decision's
// entity (ref.Key()), the same subject PII sealing and erasure key on.
type ConsentGate interface {
	HasConsent(ctx context.Context, id identity.Identity, subject, purpose string) (bool, error)
	RecordConsent(ctx context.Context, id identity.Identity, subject, purpose, basis string) error
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
	consent    ConsentGate
	tracer     trace.Tracer
	// evalTimeout bounds per-node expression/Code evaluation so a CPU-heavy
	// expression a flow author ships can't tie up the synchronous decide.
	evalTimeout time.Duration
}

// defaultEvalTimeout is the wall-clock budget for a single decide's expression and
// Code evaluation. Generous for legitimate flows, tight enough that a pathological
// expression fails loudly instead of hanging the synchronous decide.
const defaultEvalTimeout = 5 * time.Second

// DecideOption customizes a DecideHandler (used by tests to make A/B routing
// deterministic).
type DecideOption func(*DecideHandler)

// WithRoll overrides the A/B routing draw (a value in [0,100)).
func WithRoll(roll func() int) DecideOption { return func(h *DecideHandler) { h.roll = roll } }

// WithNow overrides the clock used to stamp recorded decision events
// (deterministic tests, the demo seeder).
func WithNow(now func() time.Time) DecideOption { return func(h *DecideHandler) { h.now = now } }

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

// WithConsent supplies the consent gate that captures consent from the input and
// enforces permissible purpose on Connect nodes that require it. Without it, a
// Connect node's requires_consent is enforced loudly at execution (the connector is
// never fetched) so consent is never silently skipped.
func WithConsent(g ConsentGate) DecideOption {
	return func(h *DecideHandler) { h.consent = g }
}

// WithEvalTimeout overrides the per-decide expression/Code evaluation budget. A
// non-positive value disables the deadline (the evaluators then rely only on their
// step/structure bounds). Configured at the composition root.
func WithEvalTimeout(d time.Duration) DecideOption {
	return func(h *DecideHandler) { h.evalTimeout = d }
}

// NewDecideHandler builds a DecideHandler using the system clock and random id +
// routing sources. id generation, timing, and the routing draw are the only
// effects, and all are recorded (the chosen version and variant land in the
// DecisionStarted event, so replay is deterministic).
func NewDecideHandler(log eventlog.Log, st store.Store, opts ...DecideOption) *DecideHandler {
	h := &DecideHandler{
		log:         log,
		store:       st,
		now:         func() time.Time { return time.Now().UTC() },
		newID:       newID,
		roll:        rollPercent,
		tracer:      telemetry.Tracer(),
		evalTimeout: defaultEvalTimeout,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// execute runs a graph under the handler's per-decide evaluation deadline. A
// non-positive timeout disables the deadline. obs may be nil (no per-node observer).
func (h *DecideHandler) execute(ctx context.Context, g events.Graph, data map[string]any, obs domain.NodeObserver) domain.Run {
	if h.evalTimeout <= 0 {
		return domain.ExecuteObserved(g, data, obs)
	}
	ctx, cancel := context.WithTimeout(ctx, h.evalTimeout)
	defer cancel()
	return domain.ExecuteContext(ctx, g, data, obs)
}

// rollPercent returns a near-uniform draw in [0,100) from a cryptographic source
// (avoids the weak-RNG SAST finding; routing is not security-sensitive). One byte
// is mapped to [0,99] via *100/256, so the conversion is a safe widening byte->int.
func rollPercent() int {
	var b [1]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("decision-engine: crypto/rand unavailable: " + err.Error())
	}
	return int(b[0]) * 100 / 256
}

// DecideResult is the decide response: the recorded decision id, the run status,
// the flow's output (on success), and the failure reason (on failure).
type DecideResult struct {
	DecisionID string
	Status     domain.RunStatus
	Output     map[string]any
	Error      string
	// Disposition is the operational policy's automated outcome (approve|decline|
	// refer), empty when no policy is bound to the flow. DispositionReason is the
	// matched rule's description (or why it referred). Typed (not bare string) so the
	// three string-ish result fields can't be transposed at a construction site.
	Disposition       policy.Disposition
	DispositionReason string
	// PreApprovalID links the grant when the decision was served instantly from a
	// pre-approval (the flow never ran); empty on an ordinary run.
	PreApprovalID string
}

// Decide runs the latest published version of the flow with the given slug in
// the given environment against data. A run that errors during evaluation is a
// recorded "failed" decision (returned with Status failed), not an API error;
// only infrastructure/lookup problems return an error.
func (h *DecideHandler) Decide(ctx context.Context, id identity.Identity, slug, env string, data map[string]any, ref EntityRef) (res DecideResult, err error) {
	if err := id.Valid(); err != nil {
		return DecideResult{}, err
	}
	if !domain.ValidEnvironment(env) {
		return DecideResult{}, fmt.Errorf("%w: invalid environment %q (sandbox|staging|production)", ErrBadRequest, env)
	}

	// One span per decision, the parent of the injector (I/O) and per-node spans
	// below. A no-op when tracing is disabled. The deferred end records the failure
	// reason — an infrastructure error, or a recorded "failed" decision outcome.
	ctx, span := h.tracer.Start(ctx, "engine.decide", trace.WithAttributes(
		attribute.String("flow.slug", slug),
		attribute.String("decision.environment", env),
	))
	defer func() {
		switch {
		case err != nil:
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		case res.Status == domain.StatusFailed:
			span.SetStatus(codes.Error, "decision failed: "+res.Error)
		}
		if res.DecisionID != "" {
			span.SetAttributes(attribute.String("decision.id", res.DecisionID))
		}
		span.End()
	}()
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
	// Outside the sandbox a decision only runs what change control made live: the
	// latest-published fallback would let an un-deployed (production: un-reviewed)
	// version decide real traffic. The sandbox keeps the fallback so a freshly
	// published flow is immediately test-runnable.
	if dep, deployed := fv.Deployments[env]; (!deployed || dep.Version == 0) && env != string(domain.EnvSandbox) {
		return DecideResult{}, fmt.Errorf("%w: flow %q has no %s deployment — deploy a version there first", ErrNotFound, slug, env)
	}
	versionNo, variantKind := h.resolveVersion(fv, env)
	variant := string(variantKind) // recorded on the wire as a plain string
	version, ok := versionByNumber(fv, versionNo)
	if !ok {
		return DecideResult{}, fmt.Errorf("%w: flow %q has no version %d", ErrNotFound, slug, versionNo)
	}

	// Pre-approval fast path: a valid pre-approval for the entity is honored
	// instantly — approve/decline with the stored terms, skipping the flow run.
	if !ref.Empty() {
		res, honored, err := h.honorPreApproval(ctx, id, fv, version.Version, env, variant, slug, ref, data)
		if err != nil {
			return DecideResult{}, err
		}
		if honored {
			return res, nil
		}
	}

	// A recorded decision outside the sandbox must serve four-eyes-approved models.
	data, err = h.prepare(ctx, id, env != string(domain.EnvSandbox), version, ref, data)
	if err != nil {
		if badProviderRef(err) {
			return DecideResult{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
		}
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
		EntityType: string(ref.Type), EntityID: string(ref.ID), Data: dataJSON,
	}); err != nil {
		return DecideResult{}, err
	}

	run := h.execute(ctx, version.Graph, data, spanObserver{ctx: ctx, tracer: h.tracer})
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
	switch run.Status {
	case domain.StatusFailed:
		terminalType = events.TypeDecisionFailed
		terminalPayload = events.DecisionFailed{
			DecisionID: decisionID, FlowID: fv.FlowID, Version: version.Version, Variant: variant,
			NodeID: run.FailedNode, Error: run.Err, DurationMS: dur,
		}
		result = DecideResult{DecisionID: decisionID, Status: domain.StatusFailed, Error: run.Err}
	case domain.StatusSuspended:
		// The flow paused at a durable human task. Persist the instance state so the
		// decision resumes deterministically when a reviewer acts; the case is opened
		// by emitEscalations below (the manual_review node is in run.Results).
		stateJSON, err := json.Marshal(run.Suspend)
		if err != nil {
			return DecideResult{}, fmt.Errorf("decision-engine: marshal suspend state: %w", err)
		}
		terminalType = events.TypeDecisionSuspended
		terminalPayload = events.DecisionSuspended{
			DecisionID: decisionID, FlowID: fv.FlowID, Version: version.Version, Variant: variant,
			NodeID: run.Suspend.NodeID, ResumeNode: run.Suspend.Resume, State: stateJSON, DurationMS: dur,
		}
		result = DecideResult{DecisionID: decisionID, Status: domain.StatusSuspended}
	default:
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
			Disposition: string(disp.disposition), DispositionCode: disp.code, DispositionReason: disp.reason,
			PolicyID: disp.policyID, PolicyVersion: disp.policyVersion,
		}
		result = DecideResult{
			DecisionID: decisionID, Status: domain.StatusCompleted, Output: run.Output,
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
	// A suspended decision has no terminal output yet, so there's nothing to compare.
	if run.Status != domain.StatusSuspended {
		if err := h.runShadow(ctx, id, fv, env, decisionID, version.Version, data, run); err != nil {
			return DecideResult{}, err
		}
	}
	return result, nil
}

// ResumeDecision un-pauses a decision suspended at a durable human task: it loads the
// captured instance state, injects the reviewer's outcome into the record, and runs
// the flow to a terminal (or another suspension). The recorded trace spans the pre-
// and post-pause nodes, so the resumed decision stays a single replayable history.
func (h *DecideHandler) ResumeDecision(ctx context.Context, id identity.Identity, decisionID string, outcome map[string]any) (DecideResult, error) {
	rec, ok, err := history.Read(ctx, h.store, id, decisionID)
	if err != nil {
		return DecideResult{}, err
	}
	if !ok {
		return DecideResult{}, fmt.Errorf("decision-engine: unknown decision %q", decisionID)
	}
	if rec.Status != "suspended" || len(rec.SuspendState) == 0 {
		return DecideResult{}, fmt.Errorf("decision-engine: decision %q is not suspended", decisionID)
	}
	var suspend domain.SuspendState
	if err := json.Unmarshal(rec.SuspendState, &suspend); err != nil {
		return DecideResult{}, fmt.Errorf("decision-engine: decode suspend state: %w", err)
	}
	fv, found, err := flows.Read(ctx, h.store, id, rec.FlowID)
	if err != nil {
		return DecideResult{}, err
	}
	if !found {
		return DecideResult{}, fmt.Errorf("decision-engine: flow %q not found", rec.FlowID)
	}
	graph, err := flows.GraphForVersion(fv, rec.Version)
	if err != nil {
		return DecideResult{}, err
	}

	if outcome == nil {
		outcome = map[string]any{}
	}
	// The reviewer's outcome merges into the decision context, so it carries the
	// same forgery surface as caller input: strip the engine-owned namespaces
	// (features/connect/ai/predict) and the accumulated compliance trail.
	stripReservedNamespaces(outcome)
	delete(outcome, "reason_codes")
	outcomeJSON, err := json.Marshal(outcome)
	if err != nil {
		return DecideResult{}, fmt.Errorf("decision-engine: marshal outcome: %w", err)
	}
	start := h.now()
	// The resume claim is keyed on the exact suspension being resumed (decision id +
	// suspend-state digest), so two concurrent resumes of one suspension contend and
	// exactly one commits, while a later re-suspension (new state) resumes freely.
	// The projection-status check above alone is TOCTOU: both racers can read
	// "suspended" before either's events apply.
	if err := h.emitUnique(ctx, id, events.TypeDecisionResumed, events.DecisionResumed{
		DecisionID: decisionID, Actor: id.Actor, Outcome: outcomeJSON,
	}, resumeClaim(decisionID, rec.SuspendState)); err != nil {
		if errors.Is(err, eventlog.ErrConflict) {
			return DecideResult{}, fmt.Errorf("decision-engine: decision %q is already being resumed", decisionID)
		}
		return DecideResult{}, err
	}

	run := domain.Resume(graph, suspend, outcome)
	ref := EntityRef{Type: entity.Type(rec.EntityType), ID: entity.ID(rec.EntityID)}
	for _, r := range run.Results {
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

	switch run.Status {
	case domain.StatusFailed:
		if err := h.emit(ctx, id, events.TypeDecisionFailed, events.DecisionFailed{
			DecisionID: decisionID, FlowID: rec.FlowID, Version: rec.Version, Variant: rec.Variant,
			NodeID: run.FailedNode, Error: run.Err, DurationMS: dur,
		}); err != nil {
			return DecideResult{}, err
		}
		if err := h.emitEscalations(ctx, id, decisionID, ref, rec.Data, run); err != nil {
			return DecideResult{}, err
		}
		return DecideResult{DecisionID: decisionID, Status: domain.StatusFailed, Error: run.Err}, nil
	case domain.StatusSuspended:
		stateJSON, err := json.Marshal(run.Suspend)
		if err != nil {
			return DecideResult{}, fmt.Errorf("decision-engine: marshal suspend state: %w", err)
		}
		if err := h.emit(ctx, id, events.TypeDecisionSuspended, events.DecisionSuspended{
			DecisionID: decisionID, FlowID: rec.FlowID, Version: rec.Version, Variant: rec.Variant,
			NodeID: run.Suspend.NodeID, ResumeNode: run.Suspend.Resume, State: stateJSON, DurationMS: dur,
		}); err != nil {
			return DecideResult{}, err
		}
		if err := h.emitEscalations(ctx, id, decisionID, ref, rec.Data, run); err != nil {
			return DecideResult{}, err
		}
		return DecideResult{DecisionID: decisionID, Status: domain.StatusSuspended}, nil
	default:
		outJSON, err := json.Marshal(run.Output)
		if err != nil {
			return DecideResult{}, fmt.Errorf("decision-engine: marshal output: %w", err)
		}
		outJSON, err = h.sealPII(ctx, id, ref, outJSON)
		if err != nil {
			return DecideResult{}, err
		}
		disp, err := h.applyPolicy(ctx, id, rec.Slug, run.Output)
		if err != nil {
			return DecideResult{}, err
		}
		if err := h.emit(ctx, id, events.TypeDecisionCompleted, events.DecisionCompleted{
			DecisionID: decisionID, FlowID: rec.FlowID, Version: rec.Version, Variant: rec.Variant,
			Output: outJSON, DurationMS: dur,
			Disposition: string(disp.disposition), DispositionCode: disp.code, DispositionReason: disp.reason,
			PolicyID: disp.policyID, PolicyVersion: disp.policyVersion,
		}); err != nil {
			return DecideResult{}, err
		}
		if err := h.emitEscalations(ctx, id, decisionID, ref, rec.Data, run); err != nil {
			return DecideResult{}, err
		}
		return DecideResult{
			DecisionID: decisionID, Status: domain.StatusCompleted, Output: run.Output,
			Disposition: disp.disposition, DispositionReason: disp.reason,
		}, nil
	}
}

// resumeClaim is the per-suspension unique key a resume's DecisionResumed event is
// appended under: the suspend-state digest pins it to one specific suspension.
func resumeClaim(decisionID string, state json.RawMessage) string {
	sum := sha256.Sum256(state)
	return "decision.resume\x00" + decisionID + "\x00" + hex.EncodeToString(sum[:8])
}

// badProviderRef matches — structurally, the providers never import this package —
// a pre-resolve failure caused by the flow referencing a connector, agent, or
// model the tenant never defined: fixable flow configuration, not a server fault.
func badProviderRef(err error) bool {
	var ref interface{ BadProviderRef() bool }
	return errors.As(err, &ref) && ref.BadProviderRef()
}

// prepare validates the caller's input against the version contract, strips the
// engine-owned namespaces, and resolves the feature/connector/AI/model injectors
// into the input — the augmented input the pure core executes. Shared by the
// recording Decide path and the record-free Preview path so both run identical
// input preparation.
func (h *DecideHandler) prepare(ctx context.Context, id identity.Identity, requireApproval bool, version flows.VersionView, ref EntityRef, data map[string]any) (map[string]any, error) {
	// Validate the caller's input against the version's contract before anything
	// is injected or recorded — a contract violation is a bad request, not a
	// recorded decision.
	if err := domain.ValidateInput(version.InputSchema, data); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadRequest, err)
	}

	// The features/connect/ai/predict top-level keys are engine-owned namespaces,
	// populated authoritatively by the injectors below. Strip any a caller supplied
	// before injection: otherwise, when a flow has no corresponding node (or no
	// provider is wired, so the injector is a no-op), the caller's value would pass
	// straight through and be read by Rule/Split/Scorecard expressions as if it were
	// engine-resolved — letting a request forge feature values or model scores the
	// flow author believes are trusted.
	stripReservedNamespaces(data)

	// Features and connector calls are resolved at decide time and merged into the
	// input (under "features" and "connect"); the augmented input is what gets
	// recorded and executed, so the run stays replay-stable from the recorded data
	// alone and the pure core never performs I/O.
	data, err := h.injectFeatures(ctx, id, ref, data)
	if err != nil {
		return nil, err
	}
	// Record any consent the caller (the bank/insurer/fintech) asserts in this
	// request — the consent it obtained from its own customer — under the decision's
	// subject, BEFORE the connectors that enforce it run.
	if err := h.captureConsent(ctx, id, ref, data); err != nil {
		return nil, err
	}
	data, err = h.injectConnectors(ctx, id, ref, version.Graph, data)
	if err != nil {
		return nil, err
	}
	data, err = h.injectAI(ctx, id, version.Graph, data)
	if err != nil {
		return nil, err
	}
	data, err = h.injectPredictions(ctx, id, requireApproval, version.Graph, data)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Preview runs the latest published version of the flow as Decide would —
// resolving the same version, validating input, running the injectors, executing
// the flow, and applying the operational policy — but records NOTHING: it emits no
// decision events, opens no case, and runs no shadow. It backs the builder's "Test
// decision", so an author can exercise a flow (and see the trace, disposition, and
// reason codes) without polluting history, metrics, or the audit log. The returned
// DecideResult has the same shape as Decide's, with an empty DecisionID (no
// decision was recorded). The pre-approval fast path is intentionally skipped:
// honoring one would record a decision, and a preview should exercise the flow.
func (h *DecideHandler) Preview(ctx context.Context, id identity.Identity, slug, env string, data map[string]any, ref EntityRef) (DecideResult, error) {
	if err := id.Valid(); err != nil {
		return DecideResult{}, err
	}
	if !domain.ValidEnvironment(env) {
		return DecideResult{}, fmt.Errorf("%w: invalid environment %q (sandbox|staging|production)", ErrBadRequest, env)
	}
	ctx, span := h.tracer.Start(ctx, "engine.preview", trace.WithAttributes(
		attribute.String("flow.slug", slug),
		attribute.String("decision.environment", env),
	))
	defer span.End()

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
	versionNo, _ := h.resolveVersion(fv, env)
	version, ok := versionByNumber(fv, versionNo)
	if !ok {
		return DecideResult{}, fmt.Errorf("%w: flow %q has no version %d", ErrNotFound, slug, versionNo)
	}

	// A preview records nothing — it is the author's test tool, exempt from the model
	// four-eyes gate so a model can be tried before it is approved.
	data, err = h.prepare(ctx, id, false, version, ref, data)
	if err != nil {
		if badProviderRef(err) {
			return DecideResult{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
		}
		return DecideResult{}, err
	}

	run := h.execute(ctx, version.Graph, data, spanObserver{ctx: ctx, tracer: h.tracer})
	if run.Status == domain.StatusFailed {
		return DecideResult{Status: domain.StatusFailed, Error: run.Err}, nil
	}
	// Apply the operational policy over the output, exactly as Decide does, so the
	// preview reflects the disposition the real decision would assign — but without
	// recording the decision the disposition would otherwise be attached to.
	disp, err := h.applyPolicy(ctx, id, slug, run.Output)
	if err != nil {
		return DecideResult{}, err
	}
	return DecideResult{
		Status: domain.StatusCompleted, Output: run.Output,
		Disposition: disp.disposition, DispositionReason: disp.reason,
	}, nil
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
		srun := h.execute(ctx, sv.Graph, data, nil)
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
	if h.sealer == nil || ref.Empty() {
		return doc, nil
	}
	return h.sealer.SealPII(ctx, id, ref.Key(), doc)
}

// reservedInputNamespaces are the top-level keys the engine populates from resolved
// features / connector responses / agent outputs / model predictions. They are
// engine-owned: a caller must not supply them (see stripReservedNamespaces).
// reason_codes is the accumulated adverse-action trail the Reason/manual_review
// nodes build and evalOutput always surfaces; stripping it here (as the Resume path
// already does, decide.go ~446) stops a caller from seeding a forged ECOA/Reg-B
// explanation into a recorded, regulated decision.
var reservedInputNamespaces = [...]string{"features", "connect", "ai", "predict", "reason_codes"}

// stripReservedNamespaces removes the engine-owned namespaces from caller input so
// only the injectors can populate them — making "caller forges a feature/score"
// non-representable rather than depending on a node being present or a provider
// being wired.
func stripReservedNamespaces(data map[string]any) {
	for _, k := range reservedInputNamespaces {
		delete(data, k)
	}
}

// spanObserver implements domain.NodeObserver, opening one tracing span per node
// as the pure core walks the graph. It is the adapter that keeps domain.Execute
// free of any telemetry import: the core calls the interface; the spans live here
// in the shell. Each node span is a child of the enclosing decide span (ctx).
type spanObserver struct {
	ctx    context.Context
	tracer trace.Tracer
}

func (o spanObserver) NodeStart(nodeID string, nodeType events.NodeType) func(error) {
	_, span := o.tracer.Start(o.ctx, "engine.node."+string(nodeType), trace.WithAttributes(
		attribute.String("node.id", nodeID),
		attribute.String("node.type", string(nodeType)),
	))
	return func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// injectFeatures returns data augmented with a "features" map of the referenced
// entity's computed feature values. It is a no-op when no provider is configured
// or the reference is empty; a provider error fails the decision loudly.
func (h *DecideHandler) injectFeatures(ctx context.Context, id identity.Identity, ref EntityRef, data map[string]any) (map[string]any, error) {
	if h.features == nil || ref.Empty() {
		return data, nil
	}
	ctx, span := h.tracer.Start(ctx, "engine.features", trace.WithAttributes(
		attribute.String("entity.type", string(ref.Type)),
	))
	feats, err := h.features.Features(ctx, id, ref)
	span.End()
	if err != nil {
		return nil, fmt.Errorf("decision-engine: features for %s: %w", ref.Key(), err)
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
func (h *DecideHandler) injectConnectors(ctx context.Context, id identity.Identity, ref EntityRef, graph events.Graph, data map[string]any) (map[string]any, error) {
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
		if err := h.enforceConsent(ctx, id, ref, sp); err != nil {
			return nil, err
		}
		callCtx, span := h.tracer.Start(ctx, "engine.connector", trace.WithAttributes(
			attribute.String("connector.name", sp.Connector),
			attribute.String("node.id", sp.NodeID),
		))
		resp, err := h.connectors.Fetch(callCtx, id, sp.Connector, params)
		span.End()
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

// enforceConsent gates a Connect node that declares requires_consent: the decision's
// subject must have active consent for that purpose before the connector is fetched
// (FCRA permissible purpose). It fails LOUD — never silently fetches — when the
// purpose is required but no consent gate is wired, the decision has no subject, or
// the subject has not consented; the connector's data is never pulled.
func (h *DecideHandler) enforceConsent(ctx context.Context, id identity.Identity, ref EntityRef, sp domain.ConnectSpec) error {
	if sp.RequiresConsent == "" {
		return nil
	}
	if h.consent == nil {
		return fmt.Errorf("decision-engine: connect node %q requires consent for %q but no consent gate is configured", sp.NodeID, sp.RequiresConsent)
	}
	if ref.Empty() {
		return fmt.Errorf("decision-engine: connect node %q requires consent for %q but the decision has no subject (entity ref)", sp.NodeID, sp.RequiresConsent)
	}
	ok, err := h.consent.HasConsent(ctx, id, ref.Key(), sp.RequiresConsent)
	if err != nil {
		return fmt.Errorf("decision-engine: connect node %q consent check: %w", sp.NodeID, err)
	}
	if !ok {
		return fmt.Errorf("decision-engine: connect node %q — subject %q has no active consent for %q (no permissible purpose)", sp.NodeID, ref.Key(), sp.RequiresConsent)
	}
	return nil
}

// consentInput is the consent a caller asserts in a decision request: the purposes
// their customer consented to, under a lawful basis. It is the bank/insurer/fintech
// passing through the consent it obtained in its own onboarding — intraktible's
// users are those businesses, never the end customer.
type consentInput struct {
	Purposes []string `json:"purposes"`
	Basis    string   `json:"basis"`
}

// captureConsent records the consent asserted in the request's "consent" block under
// the decision's subject, so the ledger carries an auditable record of the permissible
// purpose the caller relied on. No block, no gate, or no subject → nothing to record.
// A malformed block fails loud rather than silently dropping a compliance assertion.
func (h *DecideHandler) captureConsent(ctx context.Context, id identity.Identity, ref EntityRef, data map[string]any) error {
	raw, present := data["consent"]
	if !present || h.consent == nil || ref.Empty() {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("decision-engine: marshal consent input: %w", err)
	}
	var in consentInput
	if err := json.Unmarshal(b, &in); err != nil {
		return fmt.Errorf("%w: malformed consent block: %w", ErrBadRequest, err)
	}
	for _, purpose := range in.Purposes {
		if err := h.consent.RecordConsent(ctx, id, ref.Key(), purpose, in.Basis); err != nil {
			return fmt.Errorf("decision-engine: record consent for %q: %w", purpose, err)
		}
	}
	return nil
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
		callCtx, span := h.tracer.Start(ctx, "engine.ai", trace.WithAttributes(
			attribute.String("agent.name", sp.Agent),
			attribute.String("node.id", sp.NodeID),
		))
		resp, err := h.agentsP.RunAgent(callCtx, id, sp.Agent, prompt)
		span.End()
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
func (h *DecideHandler) injectPredictions(ctx context.Context, id identity.Identity, requireApproval bool, graph events.Graph, data map[string]any) (map[string]any, error) {
	specs, err := domain.PredictSpecs(graph)
	if err != nil {
		return nil, err
	}
	if h.models == nil || len(specs) == 0 {
		return data, nil
	}
	resolved := make(map[string]any, len(specs))
	for _, sp := range specs {
		// A recorded non-sandbox decision may only serve a model whose current version
		// has four-eyes approval — the model equivalent of "production decide refuses an
		// un-deployed flow version". The sandbox and previews (which record nothing) are
		// exempt so authors can test a model before it is approved.
		if requireApproval {
			approved, err := h.models.ApprovedForServing(ctx, id, sp.Model)
			if err != nil {
				return nil, fmt.Errorf("decision-engine: predict node %q (model %q): %w", sp.NodeID, sp.Model, err)
			}
			if !approved {
				return nil, fmt.Errorf("decision-engine: predict node %q model %q is not approved for serving (needs four-eyes approval)", sp.NodeID, sp.Model)
			}
		}
		callCtx, span := h.tracer.Start(ctx, "engine.predict", trace.WithAttributes(
			attribute.String("model.name", sp.Model),
			attribute.String("node.id", sp.NodeID),
		))
		resp, err := h.models.Predict(callCtx, id, sp.Model, data)
		span.End()
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
	pa, ok, err := preapproval.ActiveFor(ctx, h.store, id, ref, h.now())
	if err != nil || !ok {
		return DecideResult{}, false, err
	}
	// A grant bound to a flow is honored only when that flow is the one deciding —
	// a credit-line pre-approval must not short-circuit an unrelated fraud screen.
	if pa.FlowSlug != "" && pa.FlowSlug != slug {
		return DecideResult{}, false, nil
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
		Variant: variant, EntityType: string(ref.Type), EntityID: string(ref.ID), Data: dataJSON,
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
		PreApprovalID: pa.PreApprovalID, EntityType: string(ref.Type), EntityID: string(ref.ID), DecisionID: decisionID,
	}); err != nil {
		return DecideResult{}, false, err
	}
	return DecideResult{
		DecisionID: decisionID, Status: domain.StatusCompleted, Output: terms,
		Disposition: policy.Disposition(pa.Disposition), DispositionReason: "pre-approval honored",
		PreApprovalID: pa.PreApprovalID,
	}, true, nil
}

// dispositionResult is the policy outcome the decide path records on a completed
// decision (internal; flattened onto DecisionCompleted + DecideResult).
type dispositionResult struct {
	disposition   policy.Disposition
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
		res.disposition, res.reason = policy.Refer, "policy: "+applyErr.Error()
	} else {
		res.disposition, res.code, res.reason = out.Disposition, out.Code, out.Description
	}
	return res, nil
}

// resolveVersion selects the version to run for an environment: the deployed
// champion (or the A/B challenger for ChallengerPct percent of traffic), falling
// back to the latest published version when nothing is deployed. It returns the
// version number and the variant; the choice is recorded so replay is stable.
func (h *DecideHandler) resolveVersion(fv flows.FlowView, env string) (int, domain.Variant) {
	dep, ok := fv.Deployments[env]
	if !ok || dep.Version == 0 {
		return fv.Latest, domain.VariantChampion
	}
	if dep.ChallengerVersion > 0 && h.roll() < dep.ChallengerPct {
		return dep.ChallengerVersion, domain.VariantChallenger
	}
	return dep.Version, domain.VariantChampion
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

// emitUnique is emit with a tenant-global uniqueness claim (eventlog.Envelope.Unique):
// a second append under the same key fails with eventlog.ErrConflict.
func (h *DecideHandler) emitUnique(ctx context.Context, id identity.Identity, typ string, payload any, unique string) error {
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
		Unique:    unique,
	})
	return err
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
