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
	"sort"
	"strings"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/platform/effect"
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
	// ErrInProgress means an idempotent duplicate found the original logical
	// decision but its invocation has not finalized yet.
	ErrInProgress = errors.New("decision-engine: invocation in progress")
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
	EffectDelivery(ctx context.Context, id identity.Identity, connector string) (effect.Delivery, error)
}

// AgentProvider runs a named Agent Manager agent against a prompt and returns its
// output as JSON. The port lives here so the engine never imports the Agent
// Manager; a failed run is returned as an error so the decision fails loudly.
type AgentProvider interface {
	RunAgent(ctx context.Context, id identity.Identity, agent, prompt string, version int) (json.RawMessage, error)
	EffectDelivery(ctx context.Context, id identity.Identity, agent string) (effect.Delivery, error)
}

// ModelProvider evaluates a named predictive model from the registry over the
// decision input and returns its prediction as JSON. The port lives here so the
// engine never imports the models registry's command surface; a missing model or a
// bad feature is returned as an error so the decision fails loudly. ApprovedForServing
// is part of the contract — not an optional capability the decide path feels for —
// so the four-eyes gate can never be silently skipped by a provider that lacks it.
type ModelProvider interface {
	Predict(ctx context.Context, id identity.Identity, model string, features map[string]any) (json.RawMessage, error)
	EffectDelivery(ctx context.Context, id identity.Identity, model string) (effect.Delivery, error)
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
	RecordConsent(ctx context.Context, id identity.Identity, subject, purpose, basis, unique string) error
}

// SharingGate is the decide path's GLBA opt-out hook: it reports whether a subject has
// opted out of having their NPI shared with nonaffiliated third parties, so a Connect
// node that shares NPI outward (shares_npi) is blocked before the share happens. It is
// the opt-out mirror of ConsentGate; the composition root supplies the adapter.
type SharingGate interface {
	HasOptedOut(ctx context.Context, id identity.Identity, subject string) (bool, error)
}

// PIISealer crypto-shreds the configured PII fields of a recorded decision under
// the subject (the referenced entity), so a later erasure of that subject makes
// the recorded input/output PII unrecoverable. The port lives here so the engine
// imports neither the erasure vault nor the privacy config directly; the
// composition root supplies the adapter.
type PIISealer interface {
	SealPII(ctx context.Context, id identity.Identity, subject string, doc json.RawMessage) (json.RawMessage, error)
	OpenPII(ctx context.Context, id identity.Identity, subject string, doc json.RawMessage) (json.RawMessage, error)
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
	sharing    SharingGate
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

// WithConnectors supplies the provider invoked when execution reaches a Connect
// node. Without it, a reached Connect node fails loudly.
func WithConnectors(p ConnectorProvider) DecideOption {
	return func(h *DecideHandler) { h.connectors = p }
}

// WithAgents supplies the provider invoked when execution reaches an AI node.
// Without it, a reached AI node fails loudly.
func WithAgents(p AgentProvider) DecideOption {
	return func(h *DecideHandler) { h.agentsP = p }
}

// WithModels supplies the provider invoked when execution reaches a Predict node.
// Without it, a reached Predict node fails loudly.
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

// WithSharing supplies the GLBA opt-out gate that blocks a Connect node marked
// shares_npi when the subject has opted out of NPI sharing. Without it, a shares_npi
// node fails loudly at execution (the share never happens) so an opt-out is never
// silently ignored.
func WithSharing(g SharingGate) DecideOption {
	return func(h *DecideHandler) { h.sharing = g }
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

// executeGraph drives the pure graph interpreter and performs only the effect
// requested at the graph position actually reached. A non-positive timeout
// disables the evaluation deadline. obs may be nil.
func (h *DecideHandler) executeGraph(
	ctx context.Context,
	id identity.Identity,
	g events.Graph,
	data map[string]any,
	ref EntityRef,
	requireModelApproval bool,
	requireAgentVersion bool,
	timeout time.Duration,
	mockData map[string]any,
	audit *effectAudit,
	obs domain.NodeObserver,
) (domain.Run, error) {
	return h.executeState(
		ctx, id, g, domain.StartExecution(g, data), ref,
		requireModelApproval, requireAgentVersion, timeout, mockData, audit, obs,
	)
}

func (h *DecideHandler) resumeGraph(
	ctx context.Context,
	id identity.Identity,
	g events.Graph,
	suspend domain.SuspendState,
	outcome map[string]any,
	ref EntityRef,
	requireModelApproval bool,
	requireAgentVersion bool,
	timeout time.Duration,
	mockData map[string]any,
	audit *effectAudit,
	obs domain.NodeObserver,
) (domain.Run, error) {
	state := domain.ResumeExecution(suspend, outcome)
	if state.NextNode == "" {
		return domain.Run{Status: domain.StatusCompleted, Output: state.Record}, nil
	}
	return h.executeState(
		ctx, id, g, state, ref, requireModelApproval, requireAgentVersion, timeout, mockData, audit, obs,
	)
}

func (h *DecideHandler) executeState(
	ctx context.Context,
	id identity.Identity,
	g events.Graph,
	state domain.ExecutionState,
	ref EntityRef,
	requireModelApproval bool,
	requireAgentVersion bool,
	timeout time.Duration,
	mockData map[string]any,
	audit *effectAudit,
	obs domain.NodeObserver,
) (domain.Run, error) {
	if timeout == 0 {
		timeout = h.evalTimeout
	}
	if timeout <= 0 {
		return h.driveExecution(
			ctx, id, g, state, ref, requireModelApproval, requireAgentVersion, mockData, audit, obs,
		)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return h.driveExecution(
		ctx, id, g, state, ref, requireModelApproval, requireAgentVersion, mockData, audit, obs,
	)
}

func (h *DecideHandler) driveExecution(
	ctx context.Context,
	id identity.Identity,
	g events.Graph,
	state domain.ExecutionState,
	ref EntityRef,
	requireModelApproval bool,
	requireAgentVersion bool,
	mockData map[string]any,
	audit *effectAudit,
	obs domain.NodeObserver,
) (domain.Run, error) {
	for {
		step := domain.AdvanceExecution(ctx, g, state, obs)
		if step.Run != nil {
			return *step.Run, nil
		}
		req := *step.Effect
		attempt := 1
		effectID := ""
		started := h.now()
		delivery, err := h.effectDelivery(ctx, id, req, mockData)
		if err != nil {
			return domain.Run{}, err
		}
		if audit != nil {
			effectID = effectIdentity(audit, req)
			if err := h.recordEffectRequested(ctx, id, *audit, step.State, req, effectID, attempt, delivery); err != nil {
				return domain.Run{}, err
			}
		}
		effectCtx := ctx
		if effectID != "" {
			effectCtx, err = effect.WithRequest(ctx, effect.Request{Key: effectID, Attempt: attempt})
			if err != nil {
				return domain.Run{}, err
			}
		}
		value, err := h.performEffect(
			effectCtx, id, req, ref, requireModelApproval, requireAgentVersion, mockData,
		)
		if err != nil {
			if audit != nil {
				if emitErr := h.recordEffectFailed(
					ctx, id, *audit, req, effectID, attempt, err,
					h.now().Sub(started).Milliseconds(),
				); emitErr != nil {
					return domain.Run{}, emitErr
				}
			}
			return domain.FailEffect(step.State, req, err), nil
		}
		if audit != nil {
			if err := h.recordEffectSucceeded(
				ctx, id, *audit, req, effectID, attempt, value,
				h.now().Sub(started).Milliseconds(),
			); err != nil {
				return domain.Run{}, err
			}
		}
		state, err = domain.ResolveEffect(g, step.State, req, value)
		if err != nil {
			return domain.FailEffect(step.State, req, err), nil
		}
	}
}

func (h *DecideHandler) effectDelivery(
	ctx context.Context,
	id identity.Identity,
	req domain.EffectRequest,
	mockData map[string]any,
) (effect.Delivery, error) {
	if _, mocked, err := mockedEffect(req, mockData); mocked || err != nil {
		return effect.ReplaySafe, err
	}
	var (
		delivery effect.Delivery
		err      error
	)
	switch req.Kind {
	case domain.EffectConnect:
		if h.connectors == nil {
			return effect.AtLeastOnce, nil
		}
		delivery, err = h.connectors.EffectDelivery(ctx, id, req.Reference)
	case domain.EffectAI:
		if h.agentsP == nil {
			return effect.AtLeastOnce, nil
		}
		delivery, err = h.agentsP.EffectDelivery(ctx, id, req.Reference)
	case domain.EffectPredict:
		if h.models == nil {
			return effect.AtLeastOnce, nil
		}
		delivery, err = h.models.EffectDelivery(ctx, id, req.Reference)
	default:
		return "", fmt.Errorf("decision-engine: unsupported effect kind %q at node %q", req.Kind, req.NodeID)
	}
	if err != nil {
		return "", providerReferenceError(req.Kind, req.Reference, err)
	}
	if !delivery.Valid() {
		return "", fmt.Errorf(
			"decision-engine: %s provider %q returned invalid effect delivery %q",
			req.Kind, req.Reference, delivery,
		)
	}
	return delivery, nil
}

// validateEffectReferences performs read-only registry validation before a
// recorded invocation is accepted. It never authorizes or executes an effect:
// EffectDelivery is the provider contract's metadata lookup. This preserves a
// caller-actionable 400 for a flow that names a missing connector, agent, or
// model without regressing the interpreter invariant that only a reached branch
// emits an effect request or performs provider I/O.
func (h *DecideHandler) validateEffectReferences(
	ctx context.Context,
	id identity.Identity,
	graph events.Graph,
) error {
	connectSpecs, err := domain.ConnectSpecs(graph)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	for _, spec := range connectSpecs {
		if h.connectors == nil {
			return fmt.Errorf("%w: connect node %q has no connector provider configured", ErrBadRequest, spec.NodeID)
		}
		if _, err := h.connectors.EffectDelivery(ctx, id, spec.Connector); err != nil {
			return providerReferenceError(domain.EffectConnect, spec.Connector, err)
		}
	}
	aiSpecs, err := domain.AISpecs(graph)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	for _, spec := range aiSpecs {
		if h.agentsP == nil {
			return fmt.Errorf("%w: ai node %q has no agent provider configured", ErrBadRequest, spec.NodeID)
		}
		if _, err := h.agentsP.EffectDelivery(ctx, id, spec.Agent); err != nil {
			return providerReferenceError(domain.EffectAI, spec.Agent, err)
		}
	}
	predictSpecs, err := domain.PredictSpecs(graph)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	for _, spec := range predictSpecs {
		if h.models == nil {
			return fmt.Errorf("%w: predict node %q has no model provider configured", ErrBadRequest, spec.NodeID)
		}
		if _, err := h.models.EffectDelivery(ctx, id, spec.Model); err != nil {
			return providerReferenceError(domain.EffectPredict, spec.Model, err)
		}
	}
	return nil
}

func providerReferenceError(kind domain.EffectKind, reference string, err error) error {
	wrapped := fmt.Errorf(
		"decision-engine: resolve %s provider semantics for %q: %w",
		kind, reference, err,
	)
	if badProviderRef(err) {
		return fmt.Errorf("%w: %w", ErrBadRequest, wrapped)
	}
	return wrapped
}

func (h *DecideHandler) performEffect(
	ctx context.Context,
	id identity.Identity,
	req domain.EffectRequest,
	ref EntityRef,
	requireModelApproval bool,
	requireAgentVersion bool,
	mockData map[string]any,
) (any, error) {
	if value, mocked, err := mockedEffect(req, mockData); mocked || err != nil {
		return value, err
	}
	switch req.Kind {
	case domain.EffectConnect:
		if h.connectors == nil {
			return nil, fmt.Errorf("decision-engine: connect node %q has no connector provider configured", req.NodeID)
		}
		spec := domain.ConnectSpec{
			NodeID: req.NodeID, Connector: req.Reference, Output: req.Output,
			RequiresConsent: req.RequiresConsent, SharesNPI: req.SharesNPI,
		}
		if err := h.enforceConsent(ctx, id, ref, spec); err != nil {
			return nil, err
		}
		if err := h.enforceSharing(ctx, id, ref, spec); err != nil {
			return nil, err
		}
		params, err := json.Marshal(req.Input)
		if err != nil {
			return nil, fmt.Errorf("decision-engine: connect node %q marshal params: %w", req.NodeID, err)
		}
		callCtx, span := h.tracer.Start(ctx, "engine.connector", trace.WithAttributes(
			attribute.String("connector.name", req.Reference),
			attribute.String("node.id", req.NodeID),
		))
		resp, err := h.connectors.Fetch(callCtx, id, req.Reference, params)
		span.End()
		if err != nil {
			return nil, fmt.Errorf("decision-engine: connect node %q (connector %q): %w", req.NodeID, req.Reference, err)
		}
		var value any
		if err := json.Unmarshal(resp, &value); err != nil {
			return nil, fmt.Errorf("decision-engine: connect node %q response: %w", req.NodeID, err)
		}
		return value, nil

	case domain.EffectAI:
		if requireAgentVersion && req.Version <= 0 {
			return nil, fmt.Errorf(
				"%w: ai node %q must pin an immutable agent version outside sandbox",
				ErrBadRequest, req.NodeID,
			)
		}
		if h.agentsP == nil {
			return nil, fmt.Errorf("decision-engine: ai node %q has no agent provider configured", req.NodeID)
		}
		prompt := req.Prompt
		if prompt == "" {
			payload, err := json.Marshal(req.Input)
			if err != nil {
				return nil, fmt.Errorf("decision-engine: ai node %q marshal prompt: %w", req.NodeID, err)
			}
			prompt = string(payload)
		}
		callCtx, span := h.tracer.Start(ctx, "engine.ai", trace.WithAttributes(
			attribute.String("agent.name", req.Reference),
			attribute.Int("agent.version", req.Version),
			attribute.String("node.id", req.NodeID),
		))
		resp, err := h.agentsP.RunAgent(callCtx, id, req.Reference, prompt, req.Version)
		span.End()
		if err != nil {
			return nil, fmt.Errorf("decision-engine: ai node %q (agent %q): %w", req.NodeID, req.Reference, err)
		}
		var value any
		if err := json.Unmarshal(resp, &value); err != nil {
			return nil, fmt.Errorf("decision-engine: ai node %q response: %w", req.NodeID, err)
		}
		return value, nil

	case domain.EffectPredict:
		if h.models == nil {
			return nil, fmt.Errorf("decision-engine: predict node %q has no model provider configured", req.NodeID)
		}
		if requireModelApproval {
			approved, err := h.models.ApprovedForServing(ctx, id, req.Reference)
			if err != nil {
				return nil, fmt.Errorf("decision-engine: predict node %q (model %q): %w", req.NodeID, req.Reference, err)
			}
			if !approved {
				return nil, fmt.Errorf(
					"decision-engine: predict node %q model %q is not approved for serving (needs four-eyes approval)",
					req.NodeID, req.Reference,
				)
			}
		}
		callCtx, span := h.tracer.Start(ctx, "engine.predict", trace.WithAttributes(
			attribute.String("model.name", req.Reference),
			attribute.String("node.id", req.NodeID),
		))
		resp, err := h.models.Predict(callCtx, id, req.Reference, cloneDecisionInput(req.Input))
		span.End()
		if err != nil {
			return nil, fmt.Errorf("decision-engine: predict node %q (model %q): %w", req.NodeID, req.Reference, err)
		}
		value := map[string]any{}
		if err := json.Unmarshal(resp, &value); err != nil {
			return nil, fmt.Errorf("decision-engine: predict node %q response: %w", req.NodeID, err)
		}
		value["model"] = req.Reference
		return value, nil
	default:
		return nil, fmt.Errorf("decision-engine: unsupported effect kind %q at node %q", req.Kind, req.NodeID)
	}
}

func mockedEffect(req domain.EffectRequest, mockData map[string]any) (any, bool, error) {
	if len(mockData) == 0 {
		return nil, false, nil
	}
	rawBucket, present := mockData[string(req.Kind)]
	if !present {
		return nil, false, nil
	}
	bucket, ok := rawBucket.(map[string]any)
	if !ok {
		return nil, true, fmt.Errorf(
			"%w: mock_data.%s must be an object",
			ErrBadRequest, req.Kind,
		)
	}
	value, present := bucket[req.Output]
	if !present {
		return nil, true, fmt.Errorf(
			"%w: mock_data.%s has no output %q for node %q",
			ErrBadRequest, req.Kind, req.Output, req.NodeID,
		)
	}
	if req.Kind == domain.EffectPredict {
		prediction, ok := value.(map[string]any)
		if !ok {
			return nil, true, fmt.Errorf(
				"%w: mock_data.predict.%s must be an object",
				ErrBadRequest, req.Output,
			)
		}
		cloned := cloneDecisionInput(prediction)
		cloned["model"] = req.Reference
		value = cloned
	}
	return value, true, nil
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
	// EventSeq is the final durable event whose projections make this result and
	// its immediate side effects (case escalation / shadow comparison) readable.
	// Preview leaves it zero because it records nothing.
	EventSeq uint64
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

// Invocation is the caller-controlled portion of one decision submission. Tracking
// fields are persisted and indexed; MockData is accepted only by record-free
// previews. The legacy Decide/Preview methods build the minimal invocation.
type Invocation struct {
	Data              map[string]any
	Entity            EntityRef
	IdempotencyKey    string
	BusinessReference string
	CorrelationID     string
	Metadata          json.RawMessage
	Control           events.ExecutionControl
	MockData          map[string]any
}

const (
	maxIdempotencyKeyBytes = 256
	maxReferenceBytes      = 256
	maxMetadataBytes       = 16 * 1024
	maxInvocationTimeoutMS = int64(120_000)
)

type effectAudit struct {
	decisionID string
	flowID     string
	scope      string
	version    int
	generation int
	ref        EntityRef
}

func effectIdentity(a *effectAudit, req domain.EffectRequest) string {
	generation := a.generation
	if generation <= 0 {
		generation = 1
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%d\x00%s\x00%d\x00%s\x00%s",
		a.decisionID, generation, a.scope, a.version, req.NodeID, req.Kind,
	)))
	return hex.EncodeToString(sum[:16])
}

func (h *DecideHandler) recordEffectRequested(
	ctx context.Context,
	id identity.Identity,
	audit effectAudit,
	state domain.ExecutionState,
	req domain.EffectRequest,
	effectID string,
	attempt int,
	delivery effect.Delivery,
) error {
	input, err := json.Marshal(req.Input)
	if err != nil {
		return fmt.Errorf("decision-engine: marshal effect %q input: %w", effectID, err)
	}
	inputSum := sha256.Sum256(input)
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("decision-engine: marshal effect %q state: %w", effectID, err)
	}
	stateJSON, err = h.sealPII(ctx, id, audit.ref, stateJSON)
	if err != nil {
		return err
	}
	_, err = h.emitEnvelopeUnique(ctx, id, events.TypeEffectRequested, events.DecisionEffectRequested{
		DecisionID: audit.decisionID, EffectID: effectID, Scope: audit.scope,
		FlowID: audit.flowID, Version: audit.version,
		NodeID: req.NodeID, Kind: string(req.Kind), Reference: req.Reference,
		ProviderVersion: req.Version, Output: req.Output,
		InputHash: hex.EncodeToString(inputSum[:]), Attempt: attempt,
		Delivery: string(delivery), State: stateJSON,
	}, fmt.Sprintf("decision.effect.request\x00%s\x00%d", effectID, attempt))
	return err
}

func (h *DecideHandler) recordEffectSucceeded(
	ctx context.Context,
	id identity.Identity,
	audit effectAudit,
	req domain.EffectRequest,
	effectID string,
	attempt int,
	value any,
	durationMS int64,
) error {
	output, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("decision-engine: marshal effect %q output: %w", effectID, err)
	}
	output, err = h.sealPII(ctx, id, audit.ref, output)
	if err != nil {
		return err
	}
	_, err = h.emitEnvelopeUnique(ctx, id, events.TypeEffectSucceeded, events.DecisionEffectSucceeded{
		DecisionID: audit.decisionID, EffectID: effectID,
		NodeID: req.NodeID, Kind: string(req.Kind), Attempt: attempt,
		Output: output, DurationMS: durationMS,
	}, fmt.Sprintf("decision.effect.outcome\x00%s\x00%d", effectID, attempt))
	return err
}

func (h *DecideHandler) recordEffectFailed(
	ctx context.Context,
	id identity.Identity,
	audit effectAudit,
	req domain.EffectRequest,
	effectID string,
	attempt int,
	cause error,
	durationMS int64,
) error {
	_, err := h.emitEnvelopeUnique(ctx, id, events.TypeEffectFailed, events.DecisionEffectFailed{
		DecisionID: audit.decisionID, EffectID: effectID,
		NodeID: req.NodeID, Kind: string(req.Kind), Attempt: attempt,
		Error: cause.Error(), DurationMS: durationMS,
	}, fmt.Sprintf("decision.effect.outcome\x00%s\x00%d", effectID, attempt))
	return err
}

func normalizeInvocation(slug, env string, inv Invocation, preview bool) (Invocation, error) {
	inv.IdempotencyKey = strings.TrimSpace(inv.IdempotencyKey)
	inv.BusinessReference = strings.TrimSpace(inv.BusinessReference)
	inv.CorrelationID = strings.TrimSpace(inv.CorrelationID)
	if len(inv.IdempotencyKey) > maxIdempotencyKeyBytes {
		return Invocation{}, fmt.Errorf("%w: idempotency key exceeds %d bytes", ErrBadRequest, maxIdempotencyKeyBytes)
	}
	if len(inv.BusinessReference) > maxReferenceBytes {
		return Invocation{}, fmt.Errorf("%w: business reference exceeds %d bytes", ErrBadRequest, maxReferenceBytes)
	}
	if len(inv.CorrelationID) > maxReferenceBytes {
		return Invocation{}, fmt.Errorf("%w: correlation id exceeds %d bytes", ErrBadRequest, maxReferenceBytes)
	}
	if (inv.Entity.Type == "") != (inv.Entity.ID == "") {
		return Invocation{}, fmt.Errorf("%w: entity_type and entity_id must be supplied together", ErrBadRequest)
	}
	if inv.Data == nil {
		inv.Data = map[string]any{}
	}
	if len(inv.Metadata) > maxMetadataBytes {
		return Invocation{}, fmt.Errorf("%w: metadata exceeds %d bytes", ErrBadRequest, maxMetadataBytes)
	}
	if len(inv.Metadata) > 0 {
		var metadata map[string]any
		if err := json.Unmarshal(inv.Metadata, &metadata); err != nil {
			return Invocation{}, fmt.Errorf("%w: metadata must be a JSON object: %w", ErrBadRequest, err)
		}
		canonical, err := json.Marshal(metadata)
		if err != nil {
			return Invocation{}, fmt.Errorf("%w: canonicalize metadata: %w", ErrBadRequest, err)
		}
		inv.Metadata = canonical
	}
	if inv.Control.TimeoutMS < 0 || inv.Control.TimeoutMS > maxInvocationTimeoutMS {
		return Invocation{}, fmt.Errorf(
			"%w: control.timeout_ms must be between 0 and %d",
			ErrBadRequest, maxInvocationTimeoutMS,
		)
	}
	if preview {
		if inv.IdempotencyKey != "" || inv.BusinessReference != "" || inv.CorrelationID != "" || len(inv.Metadata) > 0 {
			return Invocation{}, fmt.Errorf(
				"%w: preview does not persist idempotency or tracking fields",
				ErrBadRequest,
			)
		}
	} else if len(inv.MockData) > 0 {
		return Invocation{}, fmt.Errorf("%w: mock_data is allowed only for preview", ErrBadRequest)
	}
	for key := range inv.MockData {
		switch key {
		case "features", "connect", "ai", "predict":
		default:
			return Invocation{}, fmt.Errorf("%w: mock_data contains unsupported namespace %q", ErrBadRequest, key)
		}
	}
	_ = slug
	_ = env
	return inv, nil
}

func invocationRequestHash(slug, env string, inv Invocation) (string, error) {
	payload := struct {
		Slug              string                  `json:"slug"`
		Environment       string                  `json:"environment"`
		Data              map[string]any          `json:"data"`
		EntityType        string                  `json:"entity_type,omitempty"`
		EntityID          string                  `json:"entity_id,omitempty"`
		BusinessReference string                  `json:"business_reference,omitempty"`
		CorrelationID     string                  `json:"correlation_id,omitempty"`
		Metadata          json.RawMessage         `json:"metadata,omitempty"`
		Control           events.ExecutionControl `json:"control,omitempty"`
	}{
		Slug: slug, Environment: env, Data: inv.Data,
		EntityType: string(inv.Entity.Type), EntityID: string(inv.Entity.ID),
		BusinessReference: inv.BusinessReference, CorrelationID: inv.CorrelationID,
		Metadata: inv.Metadata, Control: inv.Control,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: hash decision request: %w", ErrBadRequest, err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func idempotencyKeyHash(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func idempotencyClaim(slug, env, keyHash string) string {
	return "decision.idempotency\x00" + slug + "\x00" + env + "\x00" + keyHash
}

func invocationTimeout(control events.ExecutionControl) time.Duration {
	if control.TimeoutMS == 0 {
		return 0
	}
	return time.Duration(control.TimeoutMS) * time.Millisecond
}

func (h *DecideHandler) appendDecisionStarted(
	ctx context.Context,
	id identity.Identity,
	payload events.DecisionStarted,
	keyHash string,
) (eventlog.Envelope, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal %s: %w", events.TypeDecisionStarted, err)
	}
	unique := ""
	if keyHash != "" {
		unique = idempotencyClaim(payload.Slug, payload.Environment, keyHash)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: events.StreamDecisions, Type: events.TypeDecisionStarted,
		Time: h.now(), Payload: encoded, Unique: unique,
	})
}

func (h *DecideHandler) awaitIdempotentResult(
	ctx context.Context,
	id identity.Identity,
	slug, env, keyHash, requestHash string,
) (DecideResult, bool, error) {
	const waitLimit = 30 * time.Second
	waitCtx, cancel := context.WithTimeout(ctx, waitLimit)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, found, finalized, err := h.foldIdempotentResult(
			waitCtx, id, slug, env, keyHash, requestHash,
		)
		if err != nil || (found && finalized) {
			return result, found, err
		}
		select {
		case <-waitCtx.Done():
			if found {
				return DecideResult{}, true, fmt.Errorf(
					"%w: idempotent decision for flow %q in %s has not finalized: %w",
					ErrInProgress, slug, env, waitCtx.Err(),
				)
			}
			return DecideResult{}, false, nil
		case <-ticker.C:
		}
	}
}

func (h *DecideHandler) foldIdempotentResult(
	ctx context.Context,
	id identity.Identity,
	slug, env, keyHash, requestHash string,
) (DecideResult, bool, bool, error) {
	all, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamDecisions, 0)
	if err != nil {
		return DecideResult{}, false, false, err
	}
	var (
		started events.DecisionStarted
		found   bool
		result  DecideResult
	)
	for _, envelope := range all {
		if envelope.Type != events.TypeDecisionStarted {
			continue
		}
		var candidate events.DecisionStarted
		if err := json.Unmarshal(envelope.Payload, &candidate); err != nil {
			return DecideResult{}, false, false, fmt.Errorf(
				"decision-engine: decode idempotent start seq %d: %w", envelope.Seq, err,
			)
		}
		if candidate.Slug == slug && candidate.Environment == env && candidate.IdempotencyKeyHash == keyHash {
			if candidate.RequestHash != requestHash {
				return DecideResult{}, true, false, fmt.Errorf(
					"%w: idempotency key was already used with a different request",
					ErrBadRequest,
				)
			}
			started, found = candidate, true
			result.DecisionID = candidate.DecisionID
			break
		}
	}
	if !found {
		return DecideResult{}, false, false, nil
	}

	var terminal bool
	for _, envelope := range all {
		switch envelope.Type {
		case events.TypeDecisionCompleted:
			var completed events.DecisionCompleted
			if err := json.Unmarshal(envelope.Payload, &completed); err != nil {
				return DecideResult{}, true, false, err
			}
			if completed.DecisionID != started.DecisionID {
				continue
			}
			output, err := h.openPII(ctx, id, EntityRef{
				Type: entity.Type(started.EntityType), ID: entity.ID(started.EntityID),
			}, completed.Output)
			if err != nil {
				return DecideResult{}, true, false, err
			}
			if err := json.Unmarshal(output, &result.Output); err != nil {
				return DecideResult{}, true, false, fmt.Errorf("decision-engine: decode idempotent output: %w", err)
			}
			result.Status, result.Disposition = domain.StatusCompleted, policy.Disposition(completed.Disposition)
			result.DispositionReason, result.PreApprovalID = completed.DispositionReason, completed.PreApprovalID
			terminal = true
		case events.TypeDecisionFailed:
			var failed events.DecisionFailed
			if err := json.Unmarshal(envelope.Payload, &failed); err != nil {
				return DecideResult{}, true, false, err
			}
			if failed.DecisionID == started.DecisionID {
				result.Status, result.Error, terminal = domain.StatusFailed, failed.Error, true
			}
		case events.TypeDecisionSuspended:
			var suspended events.DecisionSuspended
			if err := json.Unmarshal(envelope.Payload, &suspended); err != nil {
				return DecideResult{}, true, false, err
			}
			if suspended.DecisionID == started.DecisionID {
				result.Status, terminal = domain.StatusSuspended, true
			}
		case events.TypeDecisionFinalized:
			var finalized events.DecisionFinalized
			if err := json.Unmarshal(envelope.Payload, &finalized); err != nil {
				return DecideResult{}, true, false, err
			}
			if finalized.DecisionID == started.DecisionID {
				if !terminal {
					return DecideResult{}, true, false, fmt.Errorf(
						"decision-engine: finalized decision %q has no terminal event",
						started.DecisionID,
					)
				}
				result.EventSeq = max(finalized.ResultSeq, envelope.Seq)
				return result, true, true, nil
			}
		}
	}
	return result, true, false, nil
}

// Decide runs the active version of the flow with the given slug in the selected
// environment against data. Sandbox falls back to the latest published version;
// staging and production require an explicit deployment. A run that errors during
// evaluation is a recorded "failed" decision (returned with Status failed), not an
// API error; only infrastructure/lookup problems return an error.
func (h *DecideHandler) Decide(ctx context.Context, id identity.Identity, slug, env string, data map[string]any, ref EntityRef) (res DecideResult, err error) {
	return h.DecideWithInvocation(ctx, id, slug, env, Invocation{Data: data, Entity: ref})
}

// DecideWithInvocation is Decide with the complete idempotency, tracking, metadata,
// and execution-control contract used by the HTTP boundary and SDKs.
func (h *DecideHandler) DecideWithInvocation(
	ctx context.Context,
	id identity.Identity,
	slug, env string,
	inv Invocation,
) (res DecideResult, err error) {
	var activeDecisionID string
	defer func() {
		if err == nil || activeDecisionID == "" {
			return
		}
		if markerErr := h.markExecutionInterrupted(
			context.WithoutCancel(ctx),
			id,
			activeDecisionID,
			1,
			err,
		); markerErr != nil {
			err = errors.Join(err, markerErr)
		}
	}()
	if err := id.Valid(); err != nil {
		return DecideResult{}, err
	}
	if !domain.ValidEnvironment(env) {
		return DecideResult{}, fmt.Errorf("%w: invalid environment %q (sandbox|staging|production)", ErrBadRequest, env)
	}
	inv, err = normalizeInvocation(slug, env, inv, false)
	if err != nil {
		return DecideResult{}, err
	}
	data, ref := inv.Data, inv.Entity

	// One span per decision, the parent of effect (I/O) and per-node spans
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
	if err := requireDeployment(fv, slug, env); err != nil {
		return DecideResult{}, err
	}
	versionNo, variantKind := h.resolveVersion(fv, env)
	variant := string(variantKind) // recorded on the wire as a plain string
	version, ok := versionByNumber(fv, versionNo)
	if !ok {
		return DecideResult{}, fmt.Errorf("%w: flow %q has no version %d", ErrNotFound, slug, versionNo)
	}
	rawData := cloneDecisionInput(data)
	if err := domain.ValidateInput(version.InputSchema, rawData); err != nil {
		return DecideResult{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	stripReservedNamespaces(rawData)
	requestHash, err := invocationRequestHash(slug, env, Invocation{
		Data: rawData, Entity: ref,
		BusinessReference: inv.BusinessReference, CorrelationID: inv.CorrelationID,
		Metadata: inv.Metadata, Control: inv.Control,
	})
	if err != nil {
		return DecideResult{}, err
	}
	keyHash := idempotencyKeyHash(inv.IdempotencyKey)
	if keyHash != "" {
		previous, found, finalized, err := h.foldIdempotentResult(ctx, id, slug, env, keyHash, requestHash)
		if err != nil {
			return DecideResult{}, err
		}
		if found && finalized {
			return previous, nil
		}
		if found {
			previous, _, err = h.awaitIdempotentResult(ctx, id, slug, env, keyHash, requestHash)
			return previous, err
		}
	}

	// Pre-approval fast path: a valid pre-approval for the entity is honored
	// instantly — approve/decline with the stored terms, skipping the flow run.
	if !ref.Empty() {
		res, honored, err := h.honorPreApproval(
			ctx, id, fv, version.Version, env, variant, slug, ref, rawData,
			inv, requestHash, keyHash,
		)
		if err != nil {
			return DecideResult{}, err
		}
		if honored {
			return res, nil
		}
	}

	selectedPolicy, err := h.selectPolicy(ctx, id, slug, env != string(domain.EnvSandbox))
	if err != nil {
		return DecideResult{}, err
	}

	// A recorded decision outside sandbox must use both approved models and
	// immutable, explicitly-versioned agents. Preserve the caller input and the
	// authoritative feature snapshot separately: the live and shadow interpreters
	// each request only the dependencies their selected graph path reaches.
	governed := env != string(domain.EnvSandbox)
	if err := validateAgentVersions(version.Graph, governed); err != nil {
		return DecideResult{}, err
	}
	if err := h.validateEffectReferences(ctx, id, version.Graph); err != nil {
		return DecideResult{}, err
	}

	decisionID := h.newID()
	start := h.now()
	rawJSON, err := json.Marshal(rawData)
	if err != nil {
		return DecideResult{}, fmt.Errorf("decision-engine: marshal data: %w", err)
	}
	rawJSON, err = h.sealPII(ctx, id, ref, rawJSON)
	if err != nil {
		return DecideResult{}, err
	}
	metadataJSON, err := h.sealPII(ctx, id, ref, inv.Metadata)
	if err != nil {
		return DecideResult{}, err
	}
	if _, err := h.appendDecisionStarted(ctx, id, events.DecisionStarted{
		DecisionID: decisionID, FlowID: fv.FlowID, Slug: slug,
		Version: version.Version, Environment: env, Variant: variant,
		EntityType: string(ref.Type), EntityID: string(ref.ID), Data: rawJSON,
		IdempotencyKeyHash: keyHash, RequestHash: requestHash,
		BusinessReference: inv.BusinessReference, CorrelationID: inv.CorrelationID,
		Metadata: metadataJSON, Control: inv.Control,
		RecoveryAfter:           h.executionRecoveryAfter(inv.Control),
		PolicySelectionRecorded: true,
		PolicyID:                selectedPolicy.policyID, PolicyVersion: selectedPolicy.version,
	}, keyHash); err != nil {
		if errors.Is(err, eventlog.ErrConflict) && keyHash != "" {
			previous, found, waitErr := h.awaitIdempotentResult(ctx, id, slug, env, keyHash, requestHash)
			if waitErr != nil {
				return DecideResult{}, waitErr
			}
			if !found {
				return DecideResult{}, fmt.Errorf("decision-engine: idempotency claim exists without a matching decision")
			}
			return previous, nil
		}
		return DecideResult{}, err
	}
	activeDecisionID = decisionID

	baseData, prepareErr := h.injectFeatures(ctx, id, ref, cloneDecisionInput(rawData))
	if prepareErr == nil {
		prepareErr = h.captureConsent(ctx, id, ref, baseData, decisionID)
	}
	if prepareErr != nil {
		failedEvent, emitErr := h.emitEnvelopeUnique(ctx, id, events.TypeDecisionFailed, events.DecisionFailed{
			DecisionID: decisionID, FlowID: fv.FlowID, Version: version.Version, Variant: variant,
			NodeID: "prepare", Error: prepareErr.Error(), DurationMS: h.now().Sub(start).Milliseconds(),
		}, decisionTerminalClaim(decisionID, 1))
		if emitErr != nil {
			return DecideResult{}, emitErr
		}
		finalEvent, emitErr := h.emitEnvelopeUnique(ctx, id, events.TypeDecisionFinalized, events.DecisionFinalized{
			DecisionID: decisionID, ResultSeq: failedEvent.Seq, Generation: 1,
		}, decisionFinalizedClaim(decisionID, 1))
		if emitErr != nil {
			return DecideResult{}, emitErr
		}
		activeDecisionID = ""
		return DecideResult{
			DecisionID: decisionID, Status: domain.StatusFailed, Error: prepareErr.Error(),
			EventSeq: finalEvent.Seq,
		}, nil
	}
	data = cloneDecisionInput(baseData)
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return DecideResult{}, fmt.Errorf("decision-engine: marshal prepared context: %w", err)
	}
	dataJSON, err = h.sealPII(ctx, id, ref, dataJSON)
	if err != nil {
		return DecideResult{}, err
	}
	if err := h.emit(ctx, id, events.TypeContextPrepared, events.DecisionContextPrepared{
		DecisionID: decisionID, Data: dataJSON,
	}); err != nil {
		return DecideResult{}, err
	}

	run, err := h.executeGraph(
		ctx, id, version.Graph, data, ref, governed, governed,
		invocationTimeout(inv.Control),
		nil,
		&effectAudit{
			decisionID: decisionID, flowID: fv.FlowID, scope: "live",
			version: version.Version, generation: 1, ref: ref,
		},
		spanObserver{ctx: ctx, tracer: h.tracer},
	)
	if err != nil {
		return DecideResult{}, err
	}
	for _, r := range run.Results {
		// Seal PII in each node's output too — node outputs echo input-derived PII
		// (assignment/rule/table targets, code merges, manual_review fields), so an
		// unsealed trace would survive a crypto-shred erasure that the input/output
		// sealing makes unrecoverable.
		nodeOut, err := h.sealPII(ctx, id, ref, r.Output)
		if err != nil {
			return DecideResult{}, err
		}
		if err := h.emitUnique(ctx, id, events.TypeNodeEvaluated, events.NodeEvaluated{
			DecisionID: decisionID, NodeID: r.NodeID, NodeType: r.Type, Output: nodeOut,
		}, decisionNodeClaim(decisionID, 1, r.NodeID)); err != nil {
			return DecideResult{}, err
		}
	}

	dur := h.now().Sub(start).Milliseconds()
	var terminalType string
	var terminalPayload any
	var result DecideResult
	var suspendedCaseID string
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
		stateJSON, err = h.sealPII(ctx, id, ref, stateJSON)
		if err != nil {
			return DecideResult{}, err
		}
		suspendedCaseID = h.newID()
		terminalType = events.TypeDecisionSuspended
		terminalPayload = events.DecisionSuspended{
			DecisionID: decisionID, FlowID: fv.FlowID, Version: version.Version, Variant: variant,
			NodeID: run.Suspend.NodeID, ResumeNode: run.Suspend.Resume, CaseID: suspendedCaseID,
			State: stateJSON, DurationMS: dur,
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
		// Operational policy is part of the recorded decision, so an invalid
		// evaluation fails this logical run rather than manufacturing a referral.
		disp, policyErr := applyPolicy(selectedPolicy, run.Output)
		if policyErr != nil {
			run.Status, run.FailedNode, run.Err = domain.StatusFailed, "policy", policyErr.Error()
			terminalType = events.TypeDecisionFailed
			terminalPayload = events.DecisionFailed{
				DecisionID: decisionID, FlowID: fv.FlowID, Version: version.Version, Variant: variant,
				NodeID: "policy", Error: policyErr.Error(), DurationMS: dur,
			}
			result = DecideResult{
				DecisionID: decisionID, Status: domain.StatusFailed, Error: policyErr.Error(),
			}
			break
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
	terminalEvent, err := h.emitEnvelopeUnique(
		ctx, id, terminalType, terminalPayload,
		decisionTerminalClaim(decisionID, 1),
	)
	if err != nil {
		return DecideResult{}, err
	}
	// A manual_review node that ran escalates to a case (consumed by the Case Manager).
	escalationEvent, err := h.emitEscalations(
		ctx, id, decisionID, 1, ref, dataJSON, run, suspendedCaseID,
	)
	if err != nil {
		return DecideResult{}, err
	}
	// A shadow version, if configured for this environment, is evaluated over the
	// same caller input + feature snapshot for divergence analysis. It independently
	// resolves the candidate's dependencies; its outcome never affects the result.
	// A suspended decision has no terminal output yet, so there's nothing to compare.
	if run.Status != domain.StatusSuspended {
		shadowEvent, err := h.runShadow(
			ctx, id, fv, env, decisionID, version.Version, rawData, baseData, ref,
			variantKind, governed, selectedPolicy, run, invocationTimeout(inv.Control), 1, nil,
		)
		if err != nil {
			return DecideResult{}, err
		}
		result.EventSeq = shadowEvent.Seq
	}
	if escalationEvent.Seq > result.EventSeq {
		result.EventSeq = escalationEvent.Seq
	}
	if terminalEvent.Seq > result.EventSeq {
		result.EventSeq = terminalEvent.Seq
	}
	finalizedEvent, err := h.emitEnvelopeUnique(ctx, id, events.TypeDecisionFinalized, events.DecisionFinalized{
		DecisionID: decisionID, ResultSeq: result.EventSeq, Generation: 1,
	}, decisionFinalizedClaim(decisionID, 1))
	if err != nil {
		return DecideResult{}, err
	}
	result.EventSeq = finalizedEvent.Seq
	activeDecisionID = ""
	return result, nil
}

// ResumeDecision un-pauses a decision suspended at a durable human task: it loads the
// captured instance state, injects the reviewer's outcome into the record, and runs
// the flow to a terminal (or another suspension). The recorded trace spans the pre-
// and post-pause nodes, so the resumed decision stays a single replayable history.
func (h *DecideHandler) ResumeDecision(
	ctx context.Context,
	id identity.Identity,
	decisionID string,
	outcome map[string]any,
) (res DecideResult, err error) {
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
	if rec.CaseID == "" {
		return DecideResult{}, fmt.Errorf("decision-engine: suspended decision %q has no linked review case", decisionID)
	}
	ref := EntityRef{Type: entity.Type(rec.EntityType), ID: entity.ID(rec.EntityID)}
	suspendJSON, err := h.openPII(ctx, id, ref, rec.SuspendState)
	if err != nil {
		return DecideResult{}, err
	}
	var suspend domain.SuspendState
	if err := json.Unmarshal(suspendJSON, &suspend); err != nil {
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
	selectedPolicy, err := h.policyForResume(ctx, id, rec)
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
	outcomeJSON, err = h.sealPII(ctx, id, ref, outcomeJSON)
	if err != nil {
		return DecideResult{}, err
	}
	start := h.now()
	// The resume claim is keyed on the exact suspension being resumed (decision id +
	// suspend-state digest), so two concurrent resumes of one suspension contend and
	// exactly one commits, while a later re-suspension (new state) resumes freely.
	// The projection-status check above alone is TOCTOU: both racers can read
	// "suspended" before either's events apply.
	generation := rec.Generation + 1
	if err := h.emitUnique(ctx, id, events.TypeDecisionResumed, events.DecisionResumed{
		DecisionID: decisionID, CaseID: rec.CaseID, Actor: id.Actor, Outcome: outcomeJSON,
		RecoveryAfter: h.executionRecoveryAfter(rec.Control),
	}, resumeClaim(decisionID, rec.SuspendState)); err != nil {
		if errors.Is(err, eventlog.ErrConflict) {
			return DecideResult{}, fmt.Errorf("decision-engine: decision %q is already being resumed", decisionID)
		}
		return DecideResult{}, err
	}
	defer func() {
		if err == nil {
			return
		}
		if markerErr := h.markExecutionInterrupted(
			context.WithoutCancel(ctx),
			id,
			decisionID,
			generation,
			err,
		); markerErr != nil {
			err = errors.Join(err, markerErr)
		}
	}()

	governed := rec.Environment != string(domain.EnvSandbox)
	run, err := h.resumeGraph(
		ctx, id, graph, suspend, outcome, ref, governed, governed,
		invocationTimeout(rec.Control),
		nil,
		&effectAudit{
			decisionID: decisionID, flowID: rec.FlowID, scope: "live",
			version: rec.Version, generation: generation, ref: ref,
		},
		spanObserver{ctx: ctx, tracer: h.tracer},
	)
	if err != nil {
		return DecideResult{}, err
	}
	for _, r := range run.Results {
		nodeOut, err := h.sealPII(ctx, id, ref, r.Output)
		if err != nil {
			return DecideResult{}, err
		}
		if err := h.emitUnique(ctx, id, events.TypeNodeEvaluated, events.NodeEvaluated{
			DecisionID: decisionID, NodeID: r.NodeID, NodeType: r.Type, Output: nodeOut,
		}, decisionNodeClaim(decisionID, generation, r.NodeID)); err != nil {
			return DecideResult{}, err
		}
	}
	dur := h.now().Sub(start).Milliseconds()

	switch run.Status {
	case domain.StatusFailed:
		terminalEvent, err := h.emitEnvelopeUnique(ctx, id, events.TypeDecisionFailed, events.DecisionFailed{
			DecisionID: decisionID, FlowID: rec.FlowID, Version: rec.Version, Variant: rec.Variant,
			NodeID: run.FailedNode, Error: run.Err, DurationMS: dur,
		}, decisionTerminalClaim(decisionID, generation))
		if err != nil {
			return DecideResult{}, err
		}
		escalationEvent, err := h.emitEscalations(
			ctx, id, decisionID, generation, ref, rec.Data, run, "",
		)
		if err != nil {
			return DecideResult{}, err
		}
		result := DecideResult{
			DecisionID: decisionID, Status: domain.StatusFailed, Error: run.Err,
			EventSeq: max(terminalEvent.Seq, escalationEvent.Seq),
		}
		return h.finalizeResult(ctx, id, result, generation)
	case domain.StatusSuspended:
		stateJSON, err := json.Marshal(run.Suspend)
		if err != nil {
			return DecideResult{}, fmt.Errorf("decision-engine: marshal suspend state: %w", err)
		}
		stateJSON, err = h.sealPII(ctx, id, ref, stateJSON)
		if err != nil {
			return DecideResult{}, err
		}
		nextCaseID := h.newID()
		terminalEvent, err := h.emitEnvelopeUnique(ctx, id, events.TypeDecisionSuspended, events.DecisionSuspended{
			DecisionID: decisionID, FlowID: rec.FlowID, Version: rec.Version, Variant: rec.Variant,
			NodeID: run.Suspend.NodeID, ResumeNode: run.Suspend.Resume, CaseID: nextCaseID,
			State: stateJSON, DurationMS: dur,
		}, decisionTerminalClaim(decisionID, generation))
		if err != nil {
			return DecideResult{}, err
		}
		escalationEvent, err := h.emitEscalations(
			ctx, id, decisionID, generation, ref, rec.Data, run, nextCaseID,
		)
		if err != nil {
			return DecideResult{}, err
		}
		result := DecideResult{
			DecisionID: decisionID, Status: domain.StatusSuspended,
			EventSeq: max(terminalEvent.Seq, escalationEvent.Seq),
		}
		return h.finalizeResult(ctx, id, result, generation)
	default:
		outJSON, err := json.Marshal(run.Output)
		if err != nil {
			return DecideResult{}, fmt.Errorf("decision-engine: marshal output: %w", err)
		}
		outJSON, err = h.sealPII(ctx, id, ref, outJSON)
		if err != nil {
			return DecideResult{}, err
		}
		disp, policyErr := applyPolicy(selectedPolicy, run.Output)
		if policyErr != nil {
			terminalEvent, err := h.emitEnvelopeUnique(
				ctx,
				id,
				events.TypeDecisionFailed,
				events.DecisionFailed{
					DecisionID: decisionID, FlowID: rec.FlowID,
					Version: rec.Version, Variant: rec.Variant,
					NodeID: "policy", Error: policyErr.Error(), DurationMS: dur,
				},
				decisionTerminalClaim(decisionID, generation),
			)
			if err != nil {
				return DecideResult{}, err
			}
			result := DecideResult{
				DecisionID: decisionID,
				Status:     domain.StatusFailed,
				Error:      policyErr.Error(),
				EventSeq:   terminalEvent.Seq,
			}
			return h.finalizeResult(ctx, id, result, generation)
		}
		terminalEvent, err := h.emitEnvelopeUnique(ctx, id, events.TypeDecisionCompleted, events.DecisionCompleted{
			DecisionID: decisionID, FlowID: rec.FlowID, Version: rec.Version, Variant: rec.Variant,
			Output: outJSON, DurationMS: dur,
			Disposition: string(disp.disposition), DispositionCode: disp.code, DispositionReason: disp.reason,
			PolicyID: disp.policyID, PolicyVersion: disp.policyVersion,
		}, decisionTerminalClaim(decisionID, generation))
		if err != nil {
			return DecideResult{}, err
		}
		escalationEvent, err := h.emitEscalations(
			ctx, id, decisionID, generation, ref, rec.Data, run, "",
		)
		if err != nil {
			return DecideResult{}, err
		}
		result := DecideResult{
			DecisionID: decisionID, Status: domain.StatusCompleted, Output: run.Output,
			Disposition: disp.disposition, DispositionReason: disp.reason,
			EventSeq: max(terminalEvent.Seq, escalationEvent.Seq),
		}
		return h.finalizeResult(ctx, id, result, generation)
	}
}

func (h *DecideHandler) finalizeResult(
	ctx context.Context,
	id identity.Identity,
	result DecideResult,
	generation int,
) (DecideResult, error) {
	finalized, err := h.emitEnvelopeUnique(ctx, id, events.TypeDecisionFinalized, events.DecisionFinalized{
		DecisionID: result.DecisionID, ResultSeq: result.EventSeq, Generation: generation,
	}, decisionFinalizedClaim(result.DecisionID, generation))
	if err != nil {
		return DecideResult{}, err
	}
	result.EventSeq = finalized.Seq
	return result, nil
}

func (h *DecideHandler) executionRecoveryAfter(control events.ExecutionControl) time.Time {
	timeout := invocationTimeout(control)
	if timeout <= 0 {
		timeout = h.evalTimeout
	}
	// A live graph and its shadow can each consume the full bound. The recovery
	// lease adds one lease window for context preparation and durable side effects.
	return h.now().Add(2*timeout + decisionRecoveryLease)
}

func (h *DecideHandler) markExecutionInterrupted(
	ctx context.Context,
	id identity.Identity,
	decisionID string,
	generation int,
	cause error,
) error {
	_, err := h.emitEnvelopeUnique(
		ctx,
		id,
		events.TypeExecutionInterrupted,
		events.DecisionExecutionInterrupted{
			DecisionID: decisionID,
			Generation: generation,
			Error:      cause.Error(),
			At:         h.now(),
		},
		fmt.Sprintf("decision.interrupted\x00%s\x00%d", decisionID, generation),
	)
	if errors.Is(err, eventlog.ErrConflict) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decision-engine: record interrupted execution: %w", err)
	}
	return nil
}

// resumeClaim is the per-suspension unique key a resume's DecisionResumed event is
// appended under: the suspend-state digest pins it to one specific suspension.
func resumeClaim(decisionID string, state json.RawMessage) string {
	sum := sha256.Sum256(state)
	return "decision.resume\x00" + decisionID + "\x00" + hex.EncodeToString(sum[:8])
}

// badProviderRef matches — structurally, providers never import this package — a
// referenced resource the tenant never defined: fixable configuration, not a
// server fault.
func badProviderRef(err error) bool {
	var ref interface{ BadProviderRef() bool }
	return errors.As(err, &ref) && ref.BadProviderRef()
}

func validateAgentVersions(graph events.Graph, required bool) error {
	if !required {
		return nil
	}
	specs, err := domain.AISpecs(graph)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	for _, spec := range specs {
		if spec.Version <= 0 {
			return fmt.Errorf(
				"%w: ai node %q must pin an immutable agent version outside sandbox",
				ErrBadRequest, spec.NodeID,
			)
		}
	}
	return nil
}

// preparePreviewBase validates preview input and resolves the one non-graph
// dependency: the entity feature snapshot. A caller may supply mock_data.features
// to keep a preview record-free and provider-free.
func (h *DecideHandler) preparePreviewBase(
	ctx context.Context,
	id identity.Identity,
	version flows.VersionView,
	ref EntityRef,
	data map[string]any,
	mockData map[string]any,
) (map[string]any, error) {
	// Validate the caller's input against the version's contract before anything
	// is injected or recorded — a contract violation is a bad request, not a
	// recorded decision.
	if err := domain.ValidateInput(version.InputSchema, data); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadRequest, err)
	}

	// The features/connect/ai/predict top-level keys are engine-owned namespaces.
	// Strip any a caller supplied before features are captured and the interpreter
	// begins: otherwise a caller could forge feature values or provider outputs the
	// flow author believes are trusted.
	stripReservedNamespaces(data)

	if raw, mocked := mockData["features"]; mocked {
		features, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: mock_data.features must be an object", ErrBadRequest)
		}
		data["features"] = cloneDecisionInput(features)
		return data, nil
	}
	return h.injectFeatures(ctx, id, ref, data)
}

// Preview runs the environment's active version as Decide would —
// resolving the same version, validating input, executing reached effects and pure
// nodes, and applying the operational policy — but records NOTHING: it emits no
// decision events, opens no case, and runs no shadow. It backs the builder's "Test
// decision", so an author can exercise a flow (and see the trace, disposition, and
// reason codes) without polluting history, metrics, or the audit log. The returned
// DecideResult has the same shape as Decide's, with an empty DecisionID (no
// decision was recorded). The pre-approval fast path is intentionally skipped:
// honoring one would record a decision, and a preview should exercise the flow.
func (h *DecideHandler) Preview(ctx context.Context, id identity.Identity, slug, env string, data map[string]any, ref EntityRef) (DecideResult, error) {
	return h.PreviewWithInvocation(ctx, id, slug, env, Invocation{Data: data, Entity: ref})
}

// PreviewWithInvocation runs the same reached-effect interpreter without recording
// anything. MockData may satisfy reached feature/connect/ai/predict effects.
func (h *DecideHandler) PreviewWithInvocation(
	ctx context.Context,
	id identity.Identity,
	slug, env string,
	inv Invocation,
) (DecideResult, error) {
	if err := id.Valid(); err != nil {
		return DecideResult{}, err
	}
	if !domain.ValidEnvironment(env) {
		return DecideResult{}, fmt.Errorf("%w: invalid environment %q (sandbox|staging|production)", ErrBadRequest, env)
	}
	inv, err := normalizeInvocation(slug, env, inv, true)
	if err != nil {
		return DecideResult{}, err
	}
	data, ref := inv.Data, inv.Entity
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
	if err := requireDeployment(fv, slug, env); err != nil {
		return DecideResult{}, err
	}
	versionNo, _ := h.resolveVersion(fv, env)
	version, ok := versionByNumber(fv, versionNo)
	if !ok {
		return DecideResult{}, fmt.Errorf("%w: flow %q has no version %d", ErrNotFound, slug, versionNo)
	}
	selectedPolicy, err := h.selectPolicy(ctx, id, slug, env != string(domain.EnvSandbox))
	if err != nil {
		return DecideResult{}, err
	}
	if err := validateAgentVersions(version.Graph, env != string(domain.EnvSandbox)); err != nil {
		return DecideResult{}, err
	}

	// A preview records nothing — it is the author's test tool, exempt from the
	// MODEL four-eyes gate so a candidate model can be tried before approval.
	// Non-sandbox still runs a deployed flow, so its AI nodes must pin immutable
	// agent versions just like a recorded decision.
	data, err = h.preparePreviewBase(ctx, id, version, ref, data, inv.MockData)
	if err != nil {
		if badProviderRef(err) {
			return DecideResult{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
		}
		return DecideResult{}, err
	}

	run, err := h.executeGraph(
		ctx, id, version.Graph, data, ref, false, env != string(domain.EnvSandbox),
		invocationTimeout(inv.Control),
		inv.MockData,
		nil,
		spanObserver{ctx: ctx, tracer: h.tracer},
	)
	if err != nil {
		return DecideResult{}, err
	}
	if run.Status == domain.StatusFailed {
		return DecideResult{Status: domain.StatusFailed, Error: run.Err}, nil
	}
	// Apply the operational policy over the output, exactly as Decide does, so the
	// preview reflects the disposition the real decision would assign — but without
	// recording the decision the disposition would otherwise be attached to.
	disp, policyErr := applyPolicy(selectedPolicy, run.Output)
	if policyErr != nil {
		return DecideResult{Status: domain.StatusFailed, Error: policyErr.Error()}, nil
	}
	return DecideResult{
		Status: domain.StatusCompleted, Output: run.Output,
		Disposition: disp.disposition, DispositionReason: disp.reason,
	}, nil
}

// runShadow evaluates the candidate from the same raw caller input and feature
// snapshot as the live decision, but executes the candidate graph's reached
// connector, AI, and model effects independently. Preparation/execution failure is
// evidence about the candidate and never changes the live caller result; only a
// failure to durably record that evidence is returned.
func (h *DecideHandler) runShadow(
	ctx context.Context,
	id identity.Identity,
	fv flows.FlowView,
	env, decisionID string,
	liveVersion int,
	rawData, baseData map[string]any,
	ref EntityRef,
	variant domain.Variant,
	governed bool,
	selectedPolicy policySelection,
	live domain.Run,
	timeout time.Duration,
	generation int,
	recoveryEvidence map[string][]recoveredEffect,
) (eventlog.Envelope, error) {
	shadowVer := fv.Shadows[env]
	if shadowVer == 0 || variant == domain.VariantChallenger {
		return eventlog.Envelope{}, nil
	}
	basis := events.ShadowMatchOutput
	if selectedPolicy.policyID != "" {
		basis = events.ShadowMatchPolicy
	}
	ev := events.ShadowEvaluated{
		DecisionID: decisionID, FlowID: fv.FlowID, Environment: env,
		LiveVersion: liveVersion, ShadowVersion: shadowVer, LiveStatus: string(live.Status),
		MatchBasis: basis, PolicyID: selectedPolicy.policyID, PolicyVersion: selectedPolicy.version,
	}
	if shadowVer == liveVersion {
		ev.ShadowError = fmt.Sprintf(
			"shadow version %d is now the live champion; choose a different candidate",
			shadowVer,
		)
		return h.emitShadowEvaluated(ctx, id, decisionID, generation, ev)
	}
	sv, ok := versionByNumber(fv, shadowVer)
	if !ok {
		ev.ShadowError = fmt.Sprintf("shadow version %d not found", shadowVer)
	} else {
		// A candidate may declare a different input contract. Validate the original
		// caller payload against it, then start from the already-resolved feature
		// snapshot so live and candidate see the same subject state.
		if err := domain.ValidateInput(sv.InputSchema, rawData); err != nil {
			ev.ShadowError = fmt.Sprintf("shadow input: %v", err)
			return h.emitShadowEvaluated(ctx, id, decisionID, generation, ev)
		}
		shadowData := cloneDecisionInput(baseData)
		audit := &effectAudit{
			decisionID: decisionID, flowID: fv.FlowID, scope: "shadow",
			version: sv.Version, generation: generation, ref: ref,
		}
		var srun domain.Run
		var err error
		if recoveryEvidence == nil {
			srun, err = h.executeGraph(
				ctx, id, sv.Graph, shadowData, ref, governed, governed,
				timeout, nil, audit, nil,
			)
		} else {
			srun, err = h.driveRecoveredExecution(
				ctx, id, sv.Graph, domain.StartExecution(sv.Graph, shadowData),
				ref, governed, audit, recoveryEvidence, false, timeout,
			)
		}
		var indeterminate indeterminateEffectError
		if errors.As(err, &indeterminate) {
			ev.ShadowStatus = string(domain.StatusFailed)
			ev.ShadowError = err.Error()
			return h.emitShadowEvaluated(ctx, id, decisionID, generation, ev)
		}
		if err != nil {
			return eventlog.Envelope{}, err
		}
		ev.ShadowStatus = string(srun.Status)
		if srun.Status == domain.StatusFailed {
			ev.ShadowError = srun.Err
		}
		if live.Status == domain.StatusCompleted && srun.Status == domain.StatusCompleted {
			ev.ChangedFields = changedOutputFields(live.Output, srun.Output)
			if basis == events.ShadowMatchPolicy {
				liveDisp, livePolicyErr := applyPolicy(selectedPolicy, live.Output)
				if livePolicyErr != nil {
					return eventlog.Envelope{}, fmt.Errorf(
						"decision-engine: live shadow comparison policy: %w",
						livePolicyErr,
					)
				}
				shadowDisp, shadowPolicyErr := applyPolicy(selectedPolicy, srun.Output)
				if shadowPolicyErr != nil {
					ev.ShadowStatus = string(domain.StatusFailed)
					ev.ShadowError = shadowPolicyErr.Error()
					return h.emitShadowEvaluated(ctx, id, decisionID, generation, ev)
				}
				ev.LiveDisposition, ev.ShadowDisposition = string(liveDisp.disposition), string(shadowDisp.disposition)
				ev.LiveCode, ev.ShadowCode = liveDisp.code, shadowDisp.code
				ev.LiveReason, ev.ShadowReason = liveDisp.reason, shadowDisp.reason
				ev.Matched = liveDisp == shadowDisp
			} else {
				ev.Matched = reflect.DeepEqual(live.Output, srun.Output)
			}
		}
	}
	return h.emitShadowEvaluated(ctx, id, decisionID, generation, ev)
}

func (h *DecideHandler) emitShadowEvaluated(
	ctx context.Context,
	id identity.Identity,
	decisionID string,
	generation int,
	payload events.ShadowEvaluated,
) (eventlog.Envelope, error) {
	envelope, err := h.emitEnvelopeUnique(
		ctx,
		id,
		events.TypeShadowEvaluated,
		payload,
		fmt.Sprintf("decision.shadow\x00%s\x00%d", decisionID, generation),
	)
	if errors.Is(err, eventlog.ErrConflict) {
		return eventlog.Envelope{}, nil
	}
	return envelope, err
}

// changedOutputFields reports the top-level shape of a divergence without
// copying subject values into the shadow report. The event remains useful to an
// operator while preserving the decision history as the only value-bearing log.
func changedOutputFields(live, candidate map[string]any) []string {
	keys := make(map[string]struct{}, len(live)+len(candidate))
	for key := range live {
		keys[key] = struct{}{}
	}
	for key := range candidate {
		keys[key] = struct{}{}
	}
	changed := make([]string, 0, len(keys))
	for key := range keys {
		if !reflect.DeepEqual(live[key], candidate[key]) {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

// sealPII crypto-shreds the configured PII fields of a recorded document under
// the referenced entity subject. A no-op without a sealer or an entity reference.
func (h *DecideHandler) sealPII(ctx context.Context, id identity.Identity, ref EntityRef, doc json.RawMessage) (json.RawMessage, error) {
	if len(doc) == 0 || h.sealer == nil || ref.Empty() {
		return doc, nil
	}
	return h.sealer.SealPII(ctx, id, ref.Key(), doc)
}

func (h *DecideHandler) openPII(ctx context.Context, id identity.Identity, ref EntityRef, doc json.RawMessage) (json.RawMessage, error) {
	if len(doc) == 0 || h.sealer == nil || ref.Empty() {
		return doc, nil
	}
	return h.sealer.OpenPII(ctx, id, ref.Key(), doc)
}

// reservedInputNamespaces are the top-level keys the engine populates from
// authoritative features / connector responses / agent outputs / model predictions. They are
// engine-owned: a caller must not supply them (see stripReservedNamespaces).
// reason_codes is the accumulated adverse-action trail the Reason/manual_review
// nodes build and evalOutput always surfaces; stripping it here (as the Resume path
// already does, decide.go ~446) stops a caller from seeding a forged ECOA/Reg-B
// explanation into a recorded, regulated decision.
var reservedInputNamespaces = [...]string{"features", "connect", "ai", "predict", "reason_codes"}

// stripReservedNamespaces removes the engine-owned namespaces from caller input so
// only engine preparation/interpreter effects can populate them — making "caller forges a feature/score"
// non-representable rather than depending on a node being present or a provider
// being wired.
func stripReservedNamespaces(data map[string]any) {
	for _, k := range reservedInputNamespaces {
		delete(data, k)
	}
}

// cloneDecisionInput isolates the top-level working map while preserving scalar
// Go types for in-process callers. Input preparation and Execute only write
// top-level keys; nested caller values are read-only.
func cloneDecisionInput(data map[string]any) map[string]any {
	cloned := make(map[string]any, len(data))
	for key, value := range data {
		cloned[key] = value
	}
	return cloned
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

// enforceSharing gates a Connect node that declares shares_npi: if the decision's
// subject has opted out of NPI sharing with nonaffiliated third parties (GLBA §6802),
// the connector — which would transmit the subject's NPI outward — must not be called.
// It fails LOUD when sharing is declared but no gate is wired or the decision has no
// subject, so an opt-out is never silently ignored.
func (h *DecideHandler) enforceSharing(ctx context.Context, id identity.Identity, ref EntityRef, sp domain.ConnectSpec) error {
	if !sp.SharesNPI {
		return nil
	}
	if h.sharing == nil {
		return fmt.Errorf("decision-engine: connect node %q shares NPI but no sharing opt-out gate is configured", sp.NodeID)
	}
	if ref.Empty() {
		return fmt.Errorf("decision-engine: connect node %q shares NPI but the decision has no subject (entity ref)", sp.NodeID)
	}
	optedOut, err := h.sharing.HasOptedOut(ctx, id, ref.Key())
	if err != nil {
		return fmt.Errorf("decision-engine: connect node %q sharing check: %w", sp.NodeID, err)
	}
	if optedOut {
		return fmt.Errorf("decision-engine: connect node %q — subject %q has opted out of NPI sharing (GLBA); the share is blocked", sp.NodeID, ref.Key())
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
func (h *DecideHandler) captureConsent(
	ctx context.Context,
	id identity.Identity,
	ref EntityRef,
	data map[string]any,
	decisionID string,
) error {
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
		claim := fmt.Sprintf("decision.consent\x00%s\x00%s", decisionID, purpose)
		if err := h.consent.RecordConsent(
			ctx, id, ref.Key(), purpose, in.Basis, claim,
		); err != nil && !errors.Is(err, eventlog.ErrConflict) {
			return fmt.Errorf("decision-engine: record consent for %q: %w", purpose, err)
		}
	}
	return nil
}

func (h *DecideHandler) emitEscalations(
	ctx context.Context,
	id identity.Identity,
	decisionID string,
	generation int,
	ref EntityRef,
	dataJSON json.RawMessage,
	run domain.Run,
	suspendedCaseID string,
) (eventlog.Envelope, error) {
	usedSuspendedID := false
	var last eventlog.Envelope
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
			return eventlog.Envelope{}, err
		}
		var out struct {
			CompanyName json.RawMessage `json:"company_name"`
			CaseType    json.RawMessage `json:"case_type"`
			SLADays     int             `json:"sla_days"`
		}
		if err := json.Unmarshal(sealedOut, &out); err != nil {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: decode manual_review output: %w", err)
		}
		caseID := h.newID()
		if suspendedCaseID != "" && run.Suspend != nil && res.NodeID == run.Suspend.NodeID {
			caseID = suspendedCaseID
			usedSuspendedID = true
		}
		last, err = h.emitEnvelopeUnique(ctx, id, events.TypeManualReviewRequested, events.ManualReviewRequested{
			CaseID: caseID, DecisionID: decisionID, NodeID: res.NodeID,
			CompanyName: labelFromSealed(out.CompanyName), CaseType: labelFromSealed(out.CaseType),
			SLADays: out.SLADays, Context: dataJSON,
		}, decisionManualReviewClaim(decisionID, generation, res.NodeID))
		if err != nil {
			if errors.Is(err, eventlog.ErrConflict) {
				continue
			}
			return eventlog.Envelope{}, err
		}
	}
	if suspendedCaseID != "" && !usedSuspendedID {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: suspended case %q did not match a manual-review result", suspendedCaseID)
	}
	return last, nil
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
func (h *DecideHandler) honorPreApproval(
	ctx context.Context,
	id identity.Identity,
	fv flows.FlowView,
	version int,
	env, variant, slug string,
	ref EntityRef,
	data map[string]any,
	inv Invocation,
	requestHash, keyHash string,
) (DecideResult, bool, error) {
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
	metadataJSON, err := h.sealPII(ctx, id, ref, inv.Metadata)
	if err != nil {
		return DecideResult{}, false, err
	}
	decisionID := h.newID()
	if _, err := h.appendDecisionStarted(ctx, id, events.DecisionStarted{
		DecisionID: decisionID, FlowID: fv.FlowID, Slug: slug, Version: version, Environment: env,
		Variant: variant, EntityType: string(ref.Type), EntityID: string(ref.ID), Data: dataJSON,
		IdempotencyKeyHash: keyHash, RequestHash: requestHash,
		BusinessReference: inv.BusinessReference, CorrelationID: inv.CorrelationID,
		Metadata: metadataJSON, Control: inv.Control,
		RecoveryAfter:           h.executionRecoveryAfter(inv.Control),
		PolicySelectionRecorded: true,
		PreApprovalID:           pa.PreApprovalID, PreApprovalDisposition: pa.Disposition,
		PreApprovalTerms: outJSON,
	}, keyHash); err != nil {
		if errors.Is(err, eventlog.ErrConflict) && keyHash != "" {
			previous, found, waitErr := h.awaitIdempotentResult(ctx, id, slug, env, keyHash, requestHash)
			if waitErr != nil {
				return DecideResult{}, false, waitErr
			}
			if !found {
				return DecideResult{}, false, fmt.Errorf("decision-engine: idempotency claim exists without a matching decision")
			}
			return previous, true, nil
		}
		return DecideResult{}, false, err
	}
	completedEvent, err := h.emitEnvelopeUnique(ctx, id, events.TypeDecisionCompleted, events.DecisionCompleted{
		DecisionID: decisionID, FlowID: fv.FlowID, Version: version, Variant: variant,
		Output: outJSON, DurationMS: 0,
		Disposition: pa.Disposition, DispositionReason: "pre-approval honored", PreApprovalID: pa.PreApprovalID,
	}, decisionTerminalClaim(decisionID, 1))
	if err != nil {
		return DecideResult{}, false, errors.Join(
			err,
			h.markExecutionInterrupted(
				context.WithoutCancel(ctx), id, decisionID, 1, err,
			),
		)
	}
	honoredEvent, err := h.appendStreamEnvelopeUnique(ctx, id, preapproval.StreamPreApprovals, preapproval.TypeHonored, preapproval.Honored{
		PreApprovalID: pa.PreApprovalID, EntityType: string(ref.Type), EntityID: string(ref.ID), DecisionID: decisionID,
	}, "preapproval.honored\x00"+decisionID)
	if err != nil {
		return DecideResult{}, false, errors.Join(
			err,
			h.markExecutionInterrupted(
				context.WithoutCancel(ctx), id, decisionID, 1, err,
			),
		)
	}
	finalizedEvent, err := h.emitEnvelopeUnique(ctx, id, events.TypeDecisionFinalized, events.DecisionFinalized{
		DecisionID: decisionID, ResultSeq: max(completedEvent.Seq, honoredEvent.Seq), Generation: 1,
	}, decisionFinalizedClaim(decisionID, 1))
	if err != nil {
		return DecideResult{}, false, errors.Join(
			err,
			h.markExecutionInterrupted(
				context.WithoutCancel(ctx), id, decisionID, 1, err,
			),
		)
	}
	return DecideResult{
		DecisionID: decisionID, Status: domain.StatusCompleted, Output: terms,
		Disposition: policy.Disposition(pa.Disposition), DispositionReason: "pre-approval honored",
		PreApprovalID: pa.PreApprovalID, EventSeq: finalizedEvent.Seq,
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

type policySelection struct {
	policyID string
	version  int
	spec     policy.Spec
}

// selectPolicy resolves the exact policy version before execution. Sandbox uses
// the latest published draft; non-sandbox decisions and previews use only a
// checker-approved version.
func (h *DecideHandler) selectPolicy(
	ctx context.Context,
	id identity.Identity,
	slug string,
	requireApproval bool,
) (policySelection, error) {
	var (
		pv  policy.View
		ver policy.VersionView
		ok  bool
		err error
	)
	if requireApproval {
		pv, ver, ok, err = policy.ApprovedForFlow(ctx, h.store, id, slug)
	} else {
		pv, ver, ok, err = policy.ActiveForFlow(ctx, h.store, id, slug)
	}
	if err != nil {
		if errors.Is(err, policy.ErrNoApprovedVersion) {
			return policySelection{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
		}
		return policySelection{}, err
	}
	if !ok {
		return policySelection{}, nil
	}
	return policySelection{policyID: pv.PolicyID, version: ver.Version, spec: ver.Spec}, nil
}

// policyForResume restores the version selected when the decision began. A legacy
// suspended decision with a currently-bound policy cannot prove which version it
// started under, so it fails loudly instead of silently re-resolving mutable logic.
func (h *DecideHandler) policyForResume(
	ctx context.Context,
	id identity.Identity,
	rec history.Record,
) (policySelection, error) {
	if !rec.PolicySelectionRecorded {
		_, _, bound, err := policy.ActiveForFlow(ctx, h.store, id, rec.Slug)
		if err != nil {
			return policySelection{}, err
		}
		if bound {
			return policySelection{}, fmt.Errorf(
				"decision-engine: suspended decision %q predates policy snapshots and cannot safely resume while a policy is bound",
				rec.DecisionID,
			)
		}
		return policySelection{}, nil
	}
	if rec.PolicyID == "" {
		return policySelection{}, nil
	}
	pv, ver, ok, err := policy.ReadVersion(ctx, h.store, id, rec.PolicyID, rec.PolicyVersion)
	if err != nil {
		return policySelection{}, err
	}
	if !ok {
		return policySelection{}, fmt.Errorf(
			"decision-engine: decision %q references missing policy %q version %d",
			rec.DecisionID, rec.PolicyID, rec.PolicyVersion,
		)
	}
	return policySelection{policyID: pv.PolicyID, version: ver.Version, spec: ver.Spec}, nil
}

// applyPolicy applies an already-selected immutable version. An evaluation error
// is returned to the command so the recorded decision fails loudly.
func applyPolicy(selected policySelection, output map[string]any) (dispositionResult, error) {
	if selected.policyID == "" {
		return dispositionResult{}, nil
	}
	res := dispositionResult{policyID: selected.policyID, policyVersion: selected.version}
	out, err := selected.spec.Apply(output)
	if err != nil {
		return dispositionResult{}, err
	}
	res.disposition, res.code, res.reason = out.Disposition, out.Code, out.Description
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

// requireDeployment keeps recorded and record-free execution aligned: outside the
// sandbox, selecting an environment always means its governed deployed version.
func requireDeployment(fv flows.FlowView, slug, env string) error {
	dep, deployed := fv.Deployments[env]
	if env != string(domain.EnvSandbox) && (!deployed || dep.Version == 0) {
		return fmt.Errorf("%w: flow %q has no %s deployment — deploy a version there first", ErrNotFound, slug, env)
	}
	return nil
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

func (h *DecideHandler) emitEnvelope(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	return h.appendStreamEnvelope(ctx, id, events.StreamDecisions, typ, payload)
}

func (h *DecideHandler) emitEnvelopeUnique(
	ctx context.Context,
	id identity.Identity,
	typ string,
	payload any,
	unique string,
) (eventlog.Envelope, error) {
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor,
		events.StreamDecisions, typ, h.now(), payload, unique,
	)
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
	_, err := h.appendStreamEnvelope(ctx, id, stream, typ, payload)
	return err
}

func (h *DecideHandler) appendStreamEnvelope(ctx context.Context, id identity.Identity, stream, typ string, payload any) (eventlog.Envelope, error) {
	return h.appendStreamEnvelopeUnique(ctx, id, stream, typ, payload, "")
}

func (h *DecideHandler) appendStreamEnvelopeUnique(
	ctx context.Context,
	id identity.Identity,
	stream, typ string,
	payload any,
	unique string,
) (eventlog.Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal %s: %w", typ, err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    stream,
		Type:      typ,
		Time:      h.now(),
		Payload:   b,
		Unique:    unique,
	})
}
