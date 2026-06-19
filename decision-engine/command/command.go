// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command is the Decision Engine's write side (imperative shell): it
// validates via the functional core, derives version numbers from the flow's
// own event history, then appends events to the log.
package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/layout"
	"github.com/e6qu/intraktible/decision-engine/models"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler records flow lifecycle events. Version numbering and slug uniqueness
// are decided from the log (the source of truth) rather than the eventually
// consistent read model, so a mutex serializes the read-modify-append per
// instance — correct and sufficient for the monolith.
type Handler struct {
	log   eventlog.Log
	mu    sync.Mutex
	now   func() time.Time
	newID func() string
}

// NewHandler builds a Handler using the system clock and a random id source.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{
		log:   log,
		now:   func() time.Time { return time.Now().UTC() },
		newID: newID,
	}
}

// flowAgg is the command-side aggregate of one flow: its slug and highest
// published version, folded from the log.
type flowAgg struct {
	slug       string
	latest     int
	latestEtag string
}

// CreateFlow validates the command, ensures the slug is unique for the tenant,
// assigns a flow id, and appends a FlowCreated event. It returns the new id.
func (h *Handler) CreateFlow(ctx context.Context, id identity.Identity, cmd domain.CreateFlow) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	_, bySlug, err := h.foldTenant(ctx, id)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if _, exists := bySlug[cmd.Slug]; exists {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: flow slug %q already exists", cmd.Slug)
	}
	flowID := h.newID()
	payload, err := json.Marshal(events.FlowCreated{FlowID: flowID, Slug: cmd.Slug, Name: cmd.Name})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal created: %w", err)
	}
	e, err := h.appendFlowEvent(ctx, id, events.TypeFlowCreated, payload)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return flowID, e, nil
}

// PublishVersion validates the command, computes the next version number and the
// content etag, and appends a FlowVersionPublished event. It returns the
// assigned version and etag.
func (h *Handler) PublishVersion(ctx context.Context, id identity.Identity, cmd domain.PublishVersion) (int, string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	// Fill node positions when none were supplied, so an API-authored flow renders
	// with a sensible default layout (a UI/custom layout is preserved). Done before
	// the etag so the stored graph and its etag match.
	cmd.Graph = layout.Apply(cmd.Graph)
	etag, err := domain.Etag(cmd.Graph, cmd.InputSchema)
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	byID, _, err := h.foldTenant(ctx, id)
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	agg, ok := byID[cmd.FlowID]
	if !ok {
		return 0, "", eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown flow %q", cmd.FlowID)
	}
	version := agg.latest + 1
	payload, err := json.Marshal(events.FlowVersionPublished{
		FlowID:      cmd.FlowID,
		Version:     version,
		Etag:        etag,
		Graph:       cmd.Graph,
		InputSchema: cmd.InputSchema,
	})
	if err != nil {
		return 0, "", eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal published: %w", err)
	}
	e, err := h.appendFlowEvent(ctx, id, events.TypeFlowVersionPublished, payload)
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	return version, etag, e, nil
}

// ImportResult reports what an ImportFlow did: the (possibly new) flow id, the
// resulting latest version, and whether a flow/version was actually written.
type ImportResult struct {
	FlowID    string
	Version   int
	Etag      string
	Created   bool
	Published bool
	Event     eventlog.Envelope
}

// ImportFlow upserts a flow from an exported document: it creates the flow when
// the slug is new, then publishes the graph as a new version — unless the
// flow's current latest version already carries this exact content, which makes
// a re-import a no-op. It folds the authoritative log (not the read-side
// projection) under the write lock, so it is safe to run back-to-back from CI.
func (h *Handler) ImportFlow(ctx context.Context, id identity.Identity, cmd domain.ImportFlow) (ImportResult, error) {
	if err := id.Valid(); err != nil {
		return ImportResult{}, err
	}
	if err := cmd.Validate(); err != nil {
		return ImportResult{}, err
	}
	// Default layout for a position-less import; deterministic, so a re-import of the
	// same document still no-ops on the etag. A document that carries positions
	// (e.g. a prior export) keeps them.
	cmd.Graph = layout.Apply(cmd.Graph)
	etag, err := domain.Etag(cmd.Graph, cmd.InputSchema)
	if err != nil {
		return ImportResult{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	byID, bySlug, err := h.foldTenant(ctx, id)
	if err != nil {
		return ImportResult{}, err
	}

	created := false
	flowID, exists := bySlug[cmd.Slug]
	if exists {
		if agg := byID[flowID]; agg != nil && agg.latest > 0 && agg.latestEtag == etag {
			return ImportResult{FlowID: flowID, Version: agg.latest, Etag: etag}, nil
		}
	} else {
		flowID = h.newID()
		name := cmd.Name
		if name == "" {
			name = cmd.Slug
		}
		payload, err := json.Marshal(events.FlowCreated{FlowID: flowID, Slug: cmd.Slug, Name: name})
		if err != nil {
			return ImportResult{}, fmt.Errorf("decision-engine: marshal created: %w", err)
		}
		if _, err := h.appendFlowEvent(ctx, id, events.TypeFlowCreated, payload); err != nil {
			return ImportResult{}, err
		}
		created = true
	}

	version := 1
	if agg := byID[flowID]; agg != nil {
		version = agg.latest + 1
	}
	payload, err := json.Marshal(events.FlowVersionPublished{
		FlowID:      flowID,
		Version:     version,
		Etag:        etag,
		Graph:       cmd.Graph,
		InputSchema: cmd.InputSchema,
	})
	if err != nil {
		return ImportResult{}, fmt.Errorf("decision-engine: marshal published: %w", err)
	}
	e, err := h.appendFlowEvent(ctx, id, events.TypeFlowVersionPublished, payload)
	if err != nil {
		return ImportResult{}, err
	}
	return ImportResult{FlowID: flowID, Version: version, Etag: etag, Created: created, Published: true, Event: e}, nil
}

// Deploy makes a version (and optional challenger) live in an environment. It
// fails loudly if the flow or a referenced version does not exist.
func (h *Handler) Deploy(ctx context.Context, id identity.Identity, cmd domain.DeployVersion) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	// Change control: a production deployment must go through maker-checker
	// (RequestDeployment + a different user's ApproveDeployment), never a direct deploy.
	if cmd.Environment == domain.EnvProduction {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: production deployments require an approved deployment request")
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	byID, _, err := h.foldTenant(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	agg, ok := byID[cmd.FlowID]
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown flow %q", cmd.FlowID)
	}
	if cmd.Version > agg.latest {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: version %d not published (latest is %d)", cmd.Version, agg.latest)
	}
	if cmd.ChallengerVersion > agg.latest {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: challenger version %d not published (latest is %d)", cmd.ChallengerVersion, agg.latest)
	}
	payload, err := json.Marshal(events.FlowVersionDeployed{
		FlowID:            cmd.FlowID,
		Environment:       cmd.Environment,
		Version:           cmd.Version,
		ChallengerVersion: cmd.ChallengerVersion,
		ChallengerPct:     cmd.ChallengerPct,
	})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal deployed: %w", err)
	}
	return h.appendFlowEvent(ctx, id, events.TypeFlowVersionDeployed, payload)
}

// RequestDeployment proposes a deployment for review (the maker side of
// maker-checker). It validates the target version is published and records a
// DeploymentRequested; a different user must ApproveDeployment to make it live.
func (h *Handler) RequestDeployment(ctx context.Context, id identity.Identity, cmd domain.DeployVersion) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	byID, _, err := h.foldTenant(ctx, id)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	agg, ok := byID[cmd.FlowID]
	if !ok {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown flow %q", cmd.FlowID)
	}
	if cmd.Version > agg.latest || cmd.ChallengerVersion > agg.latest {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: version not published (latest is %d)", agg.latest)
	}
	reqID := h.newID()
	payload, err := json.Marshal(events.DeploymentRequested{
		RequestID: reqID, FlowID: cmd.FlowID, Environment: cmd.Environment,
		Version: cmd.Version, ChallengerVersion: cmd.ChallengerVersion, ChallengerPct: cmd.ChallengerPct,
	})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal requested: %w", err)
	}
	e, err := h.appendFlowEvent(ctx, id, events.TypeDeploymentRequested, payload)
	return reqID, e, err
}

// ApproveDeployment is the checker side: a *different* user approves a pending
// request (four-eyes), which deploys the version. The proposer cannot approve
// their own request.
func (h *Handler) ApproveDeployment(ctx context.Context, id identity.Identity, flowID, reqID, reason string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	req, ok, err := h.foldRequest(ctx, id, flowID, reqID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown deployment request %q", reqID)
	}
	if req.status != "pending" {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: deployment request %q is already %s", reqID, req.status)
	}
	if req.requestedBy == id.Actor {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: four-eyes — %q cannot approve their own deployment request", id.Actor)
	}
	payload, err := json.Marshal(events.DeploymentApproved{
		RequestID: reqID, FlowID: flowID, Environment: req.env,
		Version: req.version, ChallengerVersion: req.challengerVersion, ChallengerPct: req.challengerPct,
		Reason: reason,
	})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal approved: %w", err)
	}
	return h.appendFlowEvent(ctx, id, events.TypeDeploymentApproved, payload)
}

// RejectDeployment rejects a pending deployment request.
func (h *Handler) RejectDeployment(ctx context.Context, id identity.Identity, flowID, reqID, reason string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	req, ok, err := h.foldRequest(ctx, id, flowID, reqID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown deployment request %q", reqID)
	}
	if req.status != "pending" {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: deployment request %q is already %s", reqID, req.status)
	}
	payload, err := json.Marshal(events.DeploymentRejected{RequestID: reqID, FlowID: flowID, Reason: reason})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal rejected: %w", err)
	}
	return h.appendFlowEvent(ctx, id, events.TypeDeploymentRejected, payload)
}

// SetPromotionPolicy records a flow's per-stage promotion gate policy.
func (h *Handler) SetPromotionPolicy(ctx context.Context, id identity.Identity, cmd domain.SetPromotionPolicy) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	byID, _, err := h.foldTenant(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if _, ok := byID[cmd.FlowID]; !ok {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown flow %q", cmd.FlowID)
	}
	payload, err := json.Marshal(events.PromotionPolicySet{FlowID: cmd.FlowID, Policy: cmd.Policy})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal promotion policy: %w", err)
	}
	return h.appendFlowEvent(ctx, id, events.TypePromotionPolicySet, payload)
}

// SetShadow assigns (or clears, with version 0) the shadow version for an
// environment. A non-zero version must be published.
func (h *Handler) SetShadow(ctx context.Context, id identity.Identity, cmd domain.SetShadow) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	byID, _, err := h.foldTenant(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	agg, ok := byID[cmd.FlowID]
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown flow %q", cmd.FlowID)
	}
	if cmd.Version > agg.latest {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: shadow version %d not published (latest is %d)", cmd.Version, agg.latest)
	}
	payload, err := json.Marshal(events.ShadowSet{FlowID: cmd.FlowID, Environment: cmd.Environment, Version: cmd.Version})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal shadow set: %w", err)
	}
	return h.appendFlowEvent(ctx, id, events.TypeShadowSet, payload)
}

// deployReq is the folded state of one deployment request.
type deployReq struct {
	env                                       string
	version, challengerVersion, challengerPct int
	requestedBy, status                       string
}

// foldRequest reconstructs one deployment request from the flow stream.
func (h *Handler) foldRequest(ctx context.Context, id identity.Identity, flowID, reqID string) (deployReq, bool, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return deployReq{}, false, fmt.Errorf("decision-engine: read log: %w", err)
	}
	var req deployReq
	found := false
	for _, e := range evs {
		if e.Stream != events.StreamFlows || e.Org != id.Org || e.Workspace != id.Workspace {
			continue
		}
		switch e.Type {
		case events.TypeDeploymentRequested:
			var p events.DeploymentRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return deployReq{}, false, fmt.Errorf("decision-engine: decode requested seq %d: %w", e.Seq, err)
			}
			if p.FlowID == flowID && p.RequestID == reqID {
				req = deployReq{env: p.Environment, version: p.Version, challengerVersion: p.ChallengerVersion,
					challengerPct: p.ChallengerPct, requestedBy: e.Actor, status: "pending"}
				found = true
			}
		case events.TypeDeploymentApproved:
			var p events.DeploymentApproved
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return deployReq{}, false, fmt.Errorf("decision-engine: decode approved seq %d: %w", e.Seq, err)
			}
			if p.RequestID == reqID {
				req.status = "approved"
			}
		case events.TypeDeploymentRejected:
			var p events.DeploymentRejected
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return deployReq{}, false, fmt.Errorf("decision-engine: decode rejected seq %d: %w", e.Seq, err)
			}
			if p.RequestID == reqID {
				req.status = "rejected"
			}
		}
	}
	return req, found, nil
}

// DefineModel registers (or redefines) a named predictive model after validating
// its spec (kind + kind-specific shape). The spec is stored opaquely on the models
// stream; the registry projector materializes it for the Predict node to resolve.
func (h *Handler) DefineModel(ctx context.Context, id identity.Identity, name string, spec json.RawMessage) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(name) == "" {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: model name is required")
	}
	s, err := models.ParseSpec(spec)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if err := s.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	payload, err := json.Marshal(events.ModelDefined{Name: name, Spec: spec})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal model: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamModels,
		Type:      events.TypeModelDefined,
		Time:      h.now(),
		Payload:   payload,
	})
}

// CaptureModelBaseline snapshots a model's current prediction-probability
// distribution as the drift baseline (the projector reads its accumulated histogram
// at this event's position).
func (h *Handler) CaptureModelBaseline(ctx context.Context, id identity.Identity, name string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(name) == "" {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: model name is required")
	}
	payload, err := json.Marshal(events.ModelBaselineCaptured{Name: name})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal model baseline: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamModels,
		Type:      events.TypeModelBaselineCaptured,
		Time:      h.now(),
		Payload:   payload,
	})
}

// SetModelMonitor sets (threshold > 0) or clears (<= 0) the PSI drift threshold a
// model alerts on.
func (h *Handler) SetModelMonitor(ctx context.Context, id identity.Identity, name string, threshold float64) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(name) == "" {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: model name is required")
	}
	payload, err := json.Marshal(events.ModelMonitorSet{Name: name, Threshold: threshold})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal model monitor: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamModels,
		Type:      events.TypeModelMonitorSet,
		Time:      h.now(),
		Payload:   payload,
	})
}

func (h *Handler) appendFlowEvent(ctx context.Context, id identity.Identity, typ string, payload json.RawMessage) (eventlog.Envelope, error) {
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamFlows,
		Type:      typ,
		Time:      h.now(),
		Payload:   payload,
	})
}

// foldTenant replays the flow stream for id's tenant into per-flow aggregates,
// indexed by flow id and by slug. Callers hold h.mu.
func (h *Handler) foldTenant(ctx context.Context, id identity.Identity) (map[string]*flowAgg, map[string]string, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("decision-engine: read log: %w", err)
	}
	byID := make(map[string]*flowAgg)
	bySlug := make(map[string]string)
	for _, e := range evs {
		if e.Stream != events.StreamFlows || e.Org != id.Org || e.Workspace != id.Workspace {
			continue
		}
		switch e.Type {
		case events.TypeFlowCreated:
			var p events.FlowCreated
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, nil, fmt.Errorf("decision-engine: decode created seq %d: %w", e.Seq, err)
			}
			byID[p.FlowID] = &flowAgg{slug: p.Slug}
			bySlug[p.Slug] = p.FlowID
		case events.TypeFlowVersionPublished:
			var p events.FlowVersionPublished
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, nil, fmt.Errorf("decision-engine: decode published seq %d: %w", e.Seq, err)
			}
			if a, ok := byID[p.FlowID]; ok && p.Version > a.latest {
				a.latest = p.Version
				a.latestEtag = p.Etag
			}
		}
	}
	return byID, bySlug, nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
