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
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
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

// WithNow overrides the clock used to stamp recorded events (deterministic
// tests, the demo seeder) and returns the handler.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// flowAgg is the command-side aggregate of one flow: its slug, current details
// (name/description), and highest published version, folded from the log.
type flowAgg struct {
	slug        string
	name        string
	description string
	latest      int
	latestEtag  string
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
	payload, err := json.Marshal(events.FlowCreated{FlowID: flowID, Slug: cmd.Slug, Name: cmd.Name, Description: cmd.Description})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal created: %w", err)
	}
	// The slug claim makes uniqueness hold across processes too: the in-memory
	// bySlug check above catches the common case, and a concurrent creator that
	// raced past it loses here with ErrConflict.
	e, err := h.appendFlowEventUnique(ctx, id, events.TypeFlowCreated, payload, slugClaim(id, cmd.Slug))
	if errors.Is(err, eventlog.ErrConflict) {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: flow slug %q already exists", cmd.Slug)
	}
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return flowID, e, nil
}

// UpdateFlow changes a flow's mutable details (name/description). It resolves
// the caller's partial update against the current values folded from the log,
// then appends a FlowDetailsSet carrying the FULL resulting details — so the
// event stream and projection stay branch-free. It returns the resolved values.
func (h *Handler) UpdateFlow(ctx context.Context, id identity.Identity, cmd domain.UpdateFlow) (string, string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	byID, _, err := h.foldTenant(ctx, id)
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	agg, ok := byID[cmd.FlowID]
	if !ok {
		return "", "", eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown flow %q", cmd.FlowID)
	}
	name, description := agg.name, agg.description
	if cmd.Name != nil {
		name = *cmd.Name
	}
	if cmd.Description != nil {
		description = *cmd.Description
	}
	payload, err := json.Marshal(events.FlowDetailsSet{FlowID: cmd.FlowID, Name: name, Description: description})
	if err != nil {
		return "", "", eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal details_set: %w", err)
	}
	e, err := h.appendFlowEvent(ctx, id, events.TypeFlowDetailsSet, payload)
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	return name, description, e, nil
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

	// Re-fold and recompute the version each attempt: if another process published
	// the same version first, its append wins the versionClaim and ours returns
	// ErrConflict — we re-read the (now-advanced) latest and try again.
	for attempt := 0; ; attempt++ {
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
		e, err := h.appendFlowEventUnique(ctx, id, events.TypeFlowVersionPublished, payload, versionClaim(cmd.FlowID, version))
		if errors.Is(err, eventlog.ErrConflict) {
			if attempt >= maxClaimRetries {
				return 0, "", eventlog.Envelope{}, fmt.Errorf("decision-engine: publish version conflict on flow %q after %d retries", cmd.FlowID, attempt)
			}
			continue
		}
		if err != nil {
			return 0, "", eventlog.Envelope{}, err
		}
		return version, etag, e, nil
	}
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

	// Re-fold each attempt so create + publish stay correct across processes: a
	// raced slug-create or version-publish loses its claim with ErrConflict, and the
	// loop re-reads the now-advanced state (the flow now exists / latest advanced).
	for attempt := 0; ; attempt++ {
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
			if _, err := h.appendFlowEventUnique(ctx, id, events.TypeFlowCreated, payload, slugClaim(id, cmd.Slug)); err != nil {
				if errors.Is(err, eventlog.ErrConflict) && attempt < maxClaimRetries {
					continue // another process created this slug; re-fold and publish onto it
				}
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
		e, err := h.appendFlowEventUnique(ctx, id, events.TypeFlowVersionPublished, payload, versionClaim(flowID, version))
		if err != nil {
			if errors.Is(err, eventlog.ErrConflict) && attempt < maxClaimRetries {
				continue // a concurrent publish took this version; re-fold and retry
			}
			return ImportResult{}, err
		}
		return ImportResult{FlowID: flowID, Version: version, Etag: etag, Created: created, Published: true, Event: e}, nil
	}
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
	if domain.Environment(cmd.Environment) == domain.EnvProduction {
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
	// Re-fold + append under a per-request decision claim so the fold-then-append is
	// correct across processes, not just within this Handler's mutex: an approve and
	// a concurrent reject (or two approvals) of the same request contend on the SAME
	// claim, so exactly one terminal decision can commit. The loser re-reads the now-
	// decided request and fails loudly.
	for attempt := 0; ; attempt++ {
		req, ok, err := h.foldRequest(ctx, id, flowID, reqID)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if !ok {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown deployment request %q", reqID)
		}
		if req.status != flows.RequestPending {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: deployment request %q is already %s", reqID, req.status)
		}
		if req.requestedBy == id.Actor {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: four-eyes — %q cannot approve their own deployment request", id.Actor)
		}
		// The request pins the version it was raised for. If the environment has been
		// deployed onto since, approving would quietly revert live traffic to it.
		if req.superseded {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: deployment request %q is stale — %s has been deployed since it was raised; reject it and request again", reqID, req.env)
		}
		payload, err := json.Marshal(events.DeploymentApproved{
			RequestID: reqID, FlowID: flowID, Environment: req.env,
			Version: req.version, ChallengerVersion: req.challengerVersion, ChallengerPct: req.challengerPct,
			Reason: reason,
		})
		if err != nil {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal approved: %w", err)
		}
		e, err := h.appendFlowEventUnique(ctx, id, events.TypeDeploymentApproved, payload, decisionClaim(flowID, reqID))
		if err != nil {
			if errors.Is(err, eventlog.ErrConflict) && attempt < maxClaimRetries {
				continue // a concurrent approve/reject took this request's decision; re-fold
			}
			return eventlog.Envelope{}, err
		}
		return e, nil
	}
}

// RejectDeployment rejects a pending deployment request.
func (h *Handler) RejectDeployment(ctx context.Context, id identity.Identity, flowID, reqID, reason string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Same per-request decision claim as ApproveDeployment, so a reject racing an
	// approve cannot both commit (see ApproveDeployment).
	for attempt := 0; ; attempt++ {
		req, ok, err := h.foldRequest(ctx, id, flowID, reqID)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if !ok {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown deployment request %q", reqID)
		}
		if req.status != flows.RequestPending {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: deployment request %q is already %s", reqID, req.status)
		}
		payload, err := json.Marshal(events.DeploymentRejected{RequestID: reqID, FlowID: flowID, Reason: reason})
		if err != nil {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal rejected: %w", err)
		}
		e, err := h.appendFlowEventUnique(ctx, id, events.TypeDeploymentRejected, payload, decisionClaim(flowID, reqID))
		if err != nil {
			if errors.Is(err, eventlog.ErrConflict) && attempt < maxClaimRetries {
				continue // a concurrent approve/reject took this request's decision; re-fold
			}
			return eventlog.Envelope{}, err
		}
		return e, nil
	}
}

// deployHistory returns the ordered list of versions that have been live in an
// environment (oldest first), folding the deploy/approve/rollback events from the
// log. It is the source of truth for "the previous live version" since the read
// model only keeps the current one.
func (h *Handler) deployHistory(ctx context.Context, id identity.Identity, flowID, env string) ([]int, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("decision-engine: read log: %w", err)
	}
	var versions []int
	for _, e := range evs {
		if e.Stream != events.StreamFlows || e.Org != id.Org || e.Workspace != id.Workspace {
			continue
		}
		switch e.Type {
		case events.TypeFlowVersionDeployed:
			var p events.FlowVersionDeployed
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decision-engine: decode deployed seq %d: %w", e.Seq, err)
			}
			if p.FlowID == flowID && p.Environment == env {
				versions = append(versions, p.Version)
			}
		case events.TypeDeploymentApproved:
			var p events.DeploymentApproved
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decision-engine: decode approved seq %d: %w", e.Seq, err)
			}
			if p.FlowID == flowID && p.Environment == env {
				versions = append(versions, p.Version)
			}
		case events.TypeFlowVersionRolledBack:
			var p events.FlowVersionRolledBack
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("decision-engine: decode rolled_back seq %d: %w", e.Seq, err)
			}
			if p.FlowID == flowID && p.Environment == env {
				versions = append(versions, p.Version)
			}
		}
	}
	return versions, nil
}

// Rollback reverts an environment to its previous live version. It is allowed for
// any environment (including production) because it returns to a version that was
// already live — a deliberate, audited emergency action, not a new deploy — so it
// does not require a fresh maker-checker approval.
func (h *Handler) Rollback(ctx context.Context, id identity.Identity, flowID, env string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if !domain.ValidEnvironment(env) {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: invalid environment %q", env)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	history, err := h.deployHistory(ctx, id, flowID, env)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if len(history) < 2 {
		return eventlog.Envelope{}, fmt.Errorf("%w: no prior version to roll back to in %s", ErrBadRequest, env)
	}
	current, prior := history[len(history)-1], history[len(history)-2]
	payload, err := json.Marshal(events.FlowVersionRolledBack{
		FlowID: flowID, Environment: env, Version: prior, FromVersion: current,
	})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal rolled_back: %w", err)
	}
	return h.appendFlowEvent(ctx, id, events.TypeFlowVersionRolledBack, payload)
}

// ScheduleDeploy queues a deployment for a future time. When until is set the
// deploy is time-boxed and auto-reverts after it. Returns the new schedule id.
func (h *Handler) ScheduleDeploy(ctx context.Context, id identity.Identity, flowID, env string, version int, at time.Time, until *time.Time) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if !domain.ValidEnvironment(env) {
		return "", eventlog.Envelope{}, fmt.Errorf("%w: invalid environment %q", ErrBadRequest, env)
	}
	if version < 1 {
		return "", eventlog.Envelope{}, fmt.Errorf("%w: version must be >= 1", ErrBadRequest)
	}
	if until != nil && !until.After(at) {
		return "", eventlog.Envelope{}, fmt.Errorf("%w: until must be after at", ErrBadRequest)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	byID, _, err := h.foldTenant(ctx, id)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	agg, ok := byID[flowID]
	if !ok {
		return "", eventlog.Envelope{}, fmt.Errorf("%w: unknown flow %q", ErrNotFound, flowID)
	}
	if version > agg.latest {
		return "", eventlog.Envelope{}, fmt.Errorf("%w: version %d not published (latest is %d)", ErrBadRequest, version, agg.latest)
	}
	scheduleID := h.newID()
	payload, err := json.Marshal(events.DeployScheduled{
		ScheduleID: scheduleID, FlowID: flowID, Environment: env, Version: version, At: at.UTC(), Until: until,
	})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal deploy_scheduled: %w", err)
	}
	e, err := h.appendFlowEvent(ctx, id, events.TypeDeployScheduled, payload)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return scheduleID, e, nil
}

// CancelSchedule cancels a pending or active scheduled deploy.
func (h *Handler) CancelSchedule(ctx context.Context, id identity.Identity, flowID, scheduleID, reason string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if scheduleID == "" {
		return eventlog.Envelope{}, fmt.Errorf("%w: schedule_id is required", ErrBadRequest)
	}
	payload, err := json.Marshal(events.DeployScheduleCanceled{ScheduleID: scheduleID, FlowID: flowID, Reason: reason})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal deploy_schedule_canceled: %w", err)
	}
	return h.appendFlowEvent(ctx, id, events.TypeDeployScheduleCanceled, payload)
}

// ActivateSchedule is called by the deploy scheduler when a schedule's time has
// arrived: it marks the schedule active (recording the prior live version for a
// later revert) and deploys the scheduled version. The marker is recorded first so
// a crash mid-activation cannot leave the schedule pending and re-deploy forever.
func (h *Handler) ActivateSchedule(ctx context.Context, id identity.Identity, scheduleID, flowID, env string, version, priorVersion int) error {
	marker, err := json.Marshal(events.DeployScheduleActivated{ScheduleID: scheduleID, FlowID: flowID, PriorVersion: priorVersion})
	if err != nil {
		return fmt.Errorf("decision-engine: marshal deploy_schedule_activated: %w", err)
	}
	// The activation marker is claimed per schedule so two scheduler replicas ticking
	// together can't both activate: the loser conflicts and skips (returns nil), which
	// also prevents the PriorVersion-capture race that would strand the boxed version
	// live forever. The claim gates the deploy below because we return on conflict.
	if _, err := h.appendFlowEventUnique(ctx, id, events.TypeDeployScheduleActivated, marker, scheduleClaim("activate", scheduleID)); err != nil {
		if errors.Is(err, eventlog.ErrConflict) {
			return nil // another replica already activated this schedule
		}
		return err
	}
	deployed, err := json.Marshal(events.FlowVersionDeployed{FlowID: flowID, Environment: env, Version: version})
	if err != nil {
		return fmt.Errorf("decision-engine: marshal deployed: %w", err)
	}
	_, err = h.appendFlowEvent(ctx, id, events.TypeFlowVersionDeployed, deployed)
	return err
}

// RevertSchedule is called by the scheduler when a time-boxed schedule's window
// elapses: it marks the schedule reverted and (when a prior version existed)
// re-deploys it. With no prior version the deployment is left in place (there is no
// un-deploy), recorded only as reverted.
func (h *Handler) RevertSchedule(ctx context.Context, id identity.Identity, scheduleID, flowID, env string, priorVersion int) error {
	marker, err := json.Marshal(events.DeployScheduleReverted{ScheduleID: scheduleID, FlowID: flowID})
	if err != nil {
		return fmt.Errorf("decision-engine: marshal deploy_schedule_reverted: %w", err)
	}
	// Claimed per schedule so two replicas can't both revert (double rollback).
	if _, err := h.appendFlowEventUnique(ctx, id, events.TypeDeployScheduleReverted, marker, scheduleClaim("revert", scheduleID)); err != nil {
		if errors.Is(err, eventlog.ErrConflict) {
			return nil
		}
		return err
	}
	if priorVersion < 1 {
		return nil
	}
	reverted, err := json.Marshal(events.FlowVersionRolledBack{FlowID: flowID, Environment: env, Version: priorVersion})
	if err != nil {
		return fmt.Errorf("decision-engine: marshal rolled_back: %w", err)
	}
	_, err = h.appendFlowEvent(ctx, id, events.TypeFlowVersionRolledBack, reverted)
	return err
}

// SetSLO records a flow's service-level objectives (success-rate + latency
// targets). A zeroed SLO clears them.
func (h *Handler) SetSLO(ctx context.Context, id identity.Identity, cmd domain.SetSLO) (eventlog.Envelope, error) {
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	return h.setFlowAttribute(ctx, id, cmd.FlowID, events.TypeSLOSet, "slo", events.SLOSet{FlowID: cmd.FlowID, SLO: cmd.SLO})
}

// SetPromotionPolicy records a flow's per-stage promotion gate policy.
func (h *Handler) SetPromotionPolicy(ctx context.Context, id identity.Identity, cmd domain.SetPromotionPolicy) (eventlog.Envelope, error) {
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	return h.setFlowAttribute(ctx, id, cmd.FlowID, events.TypePromotionPolicySet, "promotion policy",
		events.PromotionPolicySet{FlowID: cmd.FlowID, Policy: cmd.Policy})
}

// setFlowAttribute validates the identity, asserts the flow exists (under the
// handler lock), and appends the attribute event — the shared spine of the
// flow-scoped Set* commands.
func (h *Handler) setFlowAttribute(ctx context.Context, id identity.Identity, flowID, typ, what string, payload any) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	byID, _, err := h.foldTenant(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if _, ok := byID[flowID]; !ok {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown flow %q", flowID)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal %s: %w", what, err)
	}
	return h.appendFlowEvent(ctx, id, typ, b)
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
	requestedBy                               string
	status                                    flows.RequestStatus
	// superseded records that the request's environment was deployed, rolled back,
	// or approved onto by someone else after the request was raised. Approving then
	// would silently revert live traffic to this request's older version.
	superseded bool
}

// RequestEnv reports the environment a pending deployment request targets, so the
// HTTP layer can enforce the caller's environment scope before approve/reject —
// the request's environment is immutable, so a pre-check cannot go stale.
func (h *Handler) RequestEnv(ctx context.Context, id identity.Identity, flowID, reqID string) (string, bool, error) {
	if err := id.Valid(); err != nil {
		return "", false, err
	}
	req, ok, err := h.foldRequest(ctx, id, flowID, reqID)
	if err != nil || !ok {
		return "", ok, err
	}
	return req.env, true, nil
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
					challengerPct: p.ChallengerPct, requestedBy: e.Actor, status: flows.RequestPending}
				found = true
			}
		case events.TypeDeploymentApproved:
			var p events.DeploymentApproved
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return deployReq{}, false, fmt.Errorf("decision-engine: decode approved seq %d: %w", e.Seq, err)
			}
			if p.RequestID == reqID {
				req.status = flows.RequestApproved
			} else if found && p.FlowID == flowID && p.Environment == req.env {
				req.superseded = true
			}
		case events.TypeDeploymentRejected:
			var p events.DeploymentRejected
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return deployReq{}, false, fmt.Errorf("decision-engine: decode rejected seq %d: %w", e.Seq, err)
			}
			if p.RequestID == reqID {
				req.status = flows.RequestRejected
			}
		case events.TypeFlowVersionDeployed:
			var p events.FlowVersionDeployed
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return deployReq{}, false, fmt.Errorf("decision-engine: decode deployed seq %d: %w", e.Seq, err)
			}
			if found && p.FlowID == flowID && p.Environment == req.env {
				req.superseded = true
			}
		case events.TypeFlowVersionRolledBack:
			var p events.FlowVersionRolledBack
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return deployReq{}, false, fmt.Errorf("decision-engine: decode rolled back seq %d: %w", e.Seq, err)
			}
			if found && p.FlowID == flowID && p.Environment == req.env {
				req.superseded = true
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

// TrainModel fits a logistic-regression model to a labelled dataset and defines it
// under name — an ordinary ModelDefined carrying the fitted spec, so the trained model
// is served and audited exactly like a hand-authored one. Returns the training report
// (cross-validated metrics + feature importance). The fit is deterministic, so
// re-training the same dataset/options reproduces both the model and the report.
func (h *Handler) TrainModel(ctx context.Context, id identity.Identity, name string, rows []models.Row, opts models.TrainOptions) (eventlog.Envelope, models.TrainReport, error) {
	spec, report, err := models.FitLogistic(rows, opts)
	if err != nil {
		return eventlog.Envelope{}, models.TrainReport{}, err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return eventlog.Envelope{}, models.TrainReport{}, fmt.Errorf("decision-engine: marshal trained model: %w", err)
	}
	e, err := h.DefineModel(ctx, id, name, raw)
	if err != nil {
		return eventlog.Envelope{}, models.TrainReport{}, err
	}
	return e, report, nil
}

// modelGov is the folded governance state of one model: its current version and
// author, the approved version, and any pending approval request.
type modelGov struct {
	version         int
	owner           string
	approvedVersion int
	pendingID       string
	pendingVersion  int
	pendingBy       string
	exists          bool
}

// foldModelGov folds the models stream for one model into its governance state. Like
// the flow maker-checker, it reads the log (not the projection) so a request made in
// this process is visible to an approval in another before the projector catches up.
func (h *Handler) foldModelGov(ctx context.Context, id identity.Identity, name string) (modelGov, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return modelGov{}, fmt.Errorf("decision-engine: read log: %w", err)
	}
	var g modelGov
	for _, e := range evs {
		if e.Stream != events.StreamModels || e.Org != id.Org || e.Workspace != id.Workspace {
			continue
		}
		switch e.Type {
		case events.TypeModelDefined:
			var p events.ModelDefined
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return modelGov{}, fmt.Errorf("decision-engine: decode model-defined seq %d: %w", e.Seq, err)
			}
			if p.Name != name {
				continue
			}
			g.exists = true
			g.version++
			g.owner = e.Actor
			g.pendingID, g.pendingVersion, g.pendingBy = "", 0, ""
		case events.TypeModelApprovalRequested:
			var p events.ModelApprovalRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return modelGov{}, fmt.Errorf("decision-engine: decode approval-requested seq %d: %w", e.Seq, err)
			}
			if p.Name == name && p.Version == g.version {
				g.pendingID, g.pendingVersion, g.pendingBy = p.RequestID, p.Version, e.Actor
			}
		case events.TypeModelApprovalApproved:
			var p events.ModelApprovalApproved
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return modelGov{}, fmt.Errorf("decision-engine: decode approval-approved seq %d: %w", e.Seq, err)
			}
			if p.Name == name && p.RequestID == g.pendingID {
				g.approvedVersion = p.Version
				g.pendingID, g.pendingVersion, g.pendingBy = "", 0, ""
			}
		case events.TypeModelApprovalRejected:
			var p events.ModelApprovalRejected
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return modelGov{}, fmt.Errorf("decision-engine: decode approval-rejected seq %d: %w", e.Seq, err)
			}
			if p.Name == name && p.RequestID == g.pendingID {
				g.pendingID, g.pendingVersion, g.pendingBy = "", 0, ""
			}
		}
	}
	return g, nil
}

// appendModelEventUnique appends a models-stream event with an optimistic claim key,
// mirroring appendFlowEventUnique for the flow maker-checker.
func (h *Handler) appendModelEventUnique(ctx context.Context, id identity.Identity, typ string, payload json.RawMessage, unique string) (eventlog.Envelope, error) {
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: events.StreamModels, Type: typ, Time: h.now(), Payload: payload, Unique: unique,
	})
}

// modelDecisionClaim reserves a model-approval request's terminal decision so a
// concurrent approve and reject of the same request cannot both commit.
func modelDecisionClaim(name, reqID string) string {
	return "model.approval.decision\x00" + name + "\x00" + reqID
}

// RequestModelApproval proposes the model's current version for review (the maker
// side of four-eyes). It fails if the model is unknown, already approved at this
// version, or already has a pending request.
func (h *Handler) RequestModelApproval(ctx context.Context, id identity.Identity, name string) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	g, err := h.foldModelGov(ctx, id, name)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if !g.exists {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown model %q", name)
	}
	if g.approvedVersion == g.version {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: model %q version %d is already approved", name, g.version)
	}
	if g.pendingID != "" {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: model %q already has a pending approval request", name)
	}
	reqID := h.newID()
	payload, err := json.Marshal(events.ModelApprovalRequested{RequestID: reqID, Name: name, Version: g.version})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal model-approval request: %w", err)
	}
	e, err := h.appendModelEventUnique(ctx, id, events.TypeModelApprovalRequested, payload, "")
	return reqID, e, err
}

// ApproveModelApproval is the checker side: a different actor — neither the requester
// nor the version's author — approves a pending request, marking the version approved
// for serving.
func (h *Handler) ApproveModelApproval(ctx context.Context, id identity.Identity, name, reqID, reason string) (eventlog.Envelope, error) {
	return h.decideModelApproval(ctx, id, name, reqID, reason, true)
}

// RejectModelApproval rejects a pending model-approval request.
func (h *Handler) RejectModelApproval(ctx context.Context, id identity.Identity, name, reqID, reason string) (eventlog.Envelope, error) {
	return h.decideModelApproval(ctx, id, name, reqID, reason, false)
}

func (h *Handler) decideModelApproval(ctx context.Context, id identity.Identity, name, reqID, reason string, approve bool) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; ; attempt++ {
		g, err := h.foldModelGov(ctx, id, name)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if g.pendingID == "" || g.pendingID != reqID {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: no pending model-approval request %q for %q", reqID, name)
		}
		if approve {
			// Four-eyes: the approver must differ from both the requester and the
			// version's author, so a model is never self-approved into production.
			if id.Actor == g.pendingBy {
				return eventlog.Envelope{}, fmt.Errorf("decision-engine: four-eyes — %q cannot approve their own model-approval request", id.Actor)
			}
			if id.Actor == g.owner {
				return eventlog.Envelope{}, fmt.Errorf("decision-engine: four-eyes — %q authored model %q and cannot approve it", id.Actor, name)
			}
			if g.pendingVersion != g.version {
				return eventlog.Envelope{}, fmt.Errorf("decision-engine: model-approval request %q is stale — %q was redefined since; reject and request again", reqID, name)
			}
		}
		typ := events.TypeModelApprovalApproved
		var payload []byte
		if approve {
			payload, err = json.Marshal(events.ModelApprovalApproved{RequestID: reqID, Name: name, Version: g.pendingVersion, Reason: reason})
		} else {
			typ = events.TypeModelApprovalRejected
			payload, err = json.Marshal(events.ModelApprovalRejected{RequestID: reqID, Name: name, Version: g.pendingVersion, Reason: reason})
		}
		if err != nil {
			return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal model-approval decision: %w", err)
		}
		e, err := h.appendModelEventUnique(ctx, id, typ, payload, modelDecisionClaim(name, reqID))
		if err != nil {
			if errors.Is(err, eventlog.ErrConflict) && attempt < maxClaimRetries {
				continue
			}
			return eventlog.Envelope{}, err
		}
		return e, nil
	}
}

// RecordModelValidation attaches validation evidence to the model's current version
// (dataset, named metrics, validator, notes, pass/fail). Evidence is what an approver
// reviews; it is not itself the gate.
func (h *Handler) RecordModelValidation(ctx context.Context, id identity.Identity, name string, rec events.ModelValidationRecorded) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	g, err := h.foldModelGov(ctx, id, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !g.exists {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: unknown model %q", name)
	}
	rec.Name, rec.Version = name, g.version
	payload, err := json.Marshal(rec)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal model validation: %w", err)
	}
	return h.appendModelEventUnique(ctx, id, events.TypeModelValidationRecorded, payload, "")
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

// RecordModelOutcome records a realized ground-truth outcome (label 0/1) for a
// prediction a model made (probability in [0,1]), so live performance is measured
// against actuals. decisionID is optional lineage. Recording an outcome for a model
// with no folded predictions is a no-op in the projector (nothing to reconcile).
func (h *Handler) RecordModelOutcome(ctx context.Context, id identity.Identity, name string, probability, label float64, decisionID string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(name) == "" {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: model name is required")
	}
	if probability < 0 || probability > 1 {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: outcome probability %v: want a fraction in [0,1]", probability)
	}
	if label != 0 && label != 1 {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: outcome label %v is not binary (0 or 1)", label)
	}
	payload, err := json.Marshal(events.ModelOutcomeRecorded{Name: name, Probability: probability, Label: label, DecisionID: decisionID})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal model outcome: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamModels,
		Type:      events.TypeModelOutcomeRecorded,
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

// MarkModelDriftAlerted records that a model's PSI crossed its threshold (the
// drift was pushed to webhooks) — the ok→firing edge the drift scheduler dedups on.
func (h *Handler) MarkModelDriftAlerted(ctx context.Context, id identity.Identity, name string, psi, threshold float64) (eventlog.Envelope, error) {
	payload, err := json.Marshal(events.ModelDriftAlerted{Name: name, PSI: psi, Threshold: threshold})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal model drift alert: %w", err)
	}
	return h.appendModelEvent(ctx, id, events.TypeModelDriftAlerted, payload)
}

// MarkModelDriftResolved records that a previously-alerting model's PSI fell back
// under its threshold (firing→ok).
func (h *Handler) MarkModelDriftResolved(ctx context.Context, id identity.Identity, name string) (eventlog.Envelope, error) {
	payload, err := json.Marshal(events.ModelDriftResolved{Name: name})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("decision-engine: marshal model drift resolve: %w", err)
	}
	return h.appendModelEvent(ctx, id, events.TypeModelDriftResolved, payload)
}

func (h *Handler) appendModelEvent(ctx context.Context, id identity.Identity, typ string, payload json.RawMessage) (eventlog.Envelope, error) {
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamModels,
		Type:      typ,
		Time:      h.now(),
		Payload:   payload,
	})
}

func (h *Handler) appendFlowEvent(ctx context.Context, id identity.Identity, typ string, payload json.RawMessage) (eventlog.Envelope, error) {
	return h.appendFlowEventUnique(ctx, id, typ, payload, "")
}

// appendFlowEventUnique appends with an optimistic-concurrency claim key (see
// eventlog.Envelope.Unique): the log rejects a second append with the same key as
// ErrConflict, so a fold-then-append stays correct across processes, not just
// within this Handler's mutex.
func (h *Handler) appendFlowEventUnique(ctx context.Context, id identity.Identity, typ string, payload json.RawMessage, unique string) (eventlog.Envelope, error) {
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamFlows,
		Type:      typ,
		Time:      h.now(),
		Payload:   payload,
		Unique:    unique,
	})
}

// maxClaimRetries bounds the optimistic-concurrency retry loop: a cross-process
// race re-folds and recomputes, but a pathological hot loop must terminate loudly.
const maxClaimRetries = 8

// slugClaim is the global claim key reserving a flow slug for a tenant; versionClaim
// reserves a (flow, version) so two processes can't both publish the same version.
func slugClaim(id identity.Identity, slug string) string {
	return "flow.slug\x00" + id.Org + "\x00" + id.Workspace + "\x00" + slug
}
func versionClaim(flowID string, version int) string {
	return "flow.version\x00" + flowID + "\x00" + strconv.Itoa(version)
}

// decisionClaim reserves the terminal decision (approve OR reject) for one
// deployment request, so two concurrent checkers — even an approve racing a reject
// across processes — cannot both commit a decision on the same request.
func decisionClaim(flowID, reqID string) string {
	return "deployment.decision\x00" + flowID + "\x00" + reqID
}

// scheduleClaim reserves a schedule's one-shot activate/revert transition, so two
// scheduler replicas sweeping the same due schedule cannot both fire it.
func scheduleClaim(transition, scheduleID string) string {
	return "deploy.schedule." + transition + "\x00" + scheduleID
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
			byID[p.FlowID] = &flowAgg{slug: p.Slug, name: p.Name, description: p.Description}
			bySlug[p.Slug] = p.FlowID
		case events.TypeFlowDetailsSet:
			var p events.FlowDetailsSet
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, nil, fmt.Errorf("decision-engine: decode details_set seq %d: %w", e.Seq, err)
			}
			if a, ok := byID[p.FlowID]; ok {
				a.name = p.Name
				a.description = p.Description
			}
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
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("decision-engine: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
