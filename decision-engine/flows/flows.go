// SPDX-License-Identifier: AGPL-3.0-or-later

// Package flows is the Decision Engine's flow-registry read model: a projector
// that folds flow lifecycle events into per-tenant flow documents (metadata plus
// the full set of published versions) for the builder UI and the decide path.
package flows

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the store collection holding flow documents (keyed by flow id).
const Collection = "decision_flows"

// slugIndexCollection maps a tenant's flow slug to its flow id, so BySlug — on the
// decide hot path — is a keyed lookup instead of a whole-collection scan. Slugs are
// immutable per flow, so the mapping is stable.
const slugIndexCollection = "decision_flows_by_slug"

type slugRef struct {
	FlowID string `json:"flow_id"`
}

// VersionView is one published, immutable flow version in the read model.
type VersionView struct {
	Version     int             `json:"version"`
	Etag        string          `json:"etag"`
	Graph       events.Graph    `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	PublishedAt time.Time       `json:"published_at"`
	PublishedBy string          `json:"published_by"`
}

// DeploymentView is which version is live in an environment, with an optional
// A/B challenger taking ChallengerPct percent of decisions.
type DeploymentView struct {
	Version           int `json:"version"`
	ChallengerVersion int `json:"challenger_version,omitempty"`
	ChallengerPct     int `json:"challenger_pct,omitempty"`
}

// RequestStatus is the lifecycle state of a maker-checker deployment request. A
// named type (not bare "pending"/"approved"/"rejected" literals scattered across the
// projection and the command fold) so the values have one source. JSON-identical to
// a plain string, so stored read models still decode.
type RequestStatus string

const (
	RequestPending  RequestStatus = "pending"
	RequestApproved RequestStatus = "approved"
	RequestRejected RequestStatus = "rejected"
)

// Valid reports whether s is a known request status.
func (s RequestStatus) Valid() bool {
	return s == RequestPending || s == RequestApproved || s == RequestRejected
}

// DeploymentRequest is one maker-checker change request and its decision status.
type DeploymentRequest struct {
	RequestID         string        `json:"request_id"`
	Environment       string        `json:"environment"`
	Version           int           `json:"version"`
	ChallengerVersion int           `json:"challenger_version,omitempty"`
	ChallengerPct     int           `json:"challenger_pct,omitempty"`
	ScheduleID        string        `json:"schedule_id,omitempty"`
	At                *time.Time    `json:"at,omitempty"`
	Until             *time.Time    `json:"until,omitempty"`
	Status            RequestStatus `json:"status"` // pending | approved | rejected
	Reason            string        `json:"reason,omitempty"`
	RequestedBy       string        `json:"requested_by"`
	RequestedAt       time.Time     `json:"requested_at"`
	DecidedBy         string        `json:"decided_by,omitempty"`
	DecidedAt         time.Time     `json:"decided_at,omitempty"`
}

// FlowView is the materialized read model for one flow.
type FlowView struct {
	Org                string                                 `json:"org"`
	Workspace          string                                 `json:"workspace"`
	FlowID             string                                 `json:"flow_id"`
	Slug               string                                 `json:"slug"`
	Name               string                                 `json:"name"`
	Description        string                                 `json:"description,omitempty"`
	Latest             int                                    `json:"latest"`
	Versions           []VersionView                          `json:"versions"`
	Deployments        map[string]DeploymentView              `json:"deployments,omitempty"`
	DeploymentRequests []DeploymentRequest                    `json:"deployment_requests,omitempty"`
	PromotionPolicy    map[string]events.PromotionStagePolicy `json:"promotion_policy,omitempty"`
	// Shadows maps an environment to a shadow version evaluated alongside live
	// decisions for divergence analysis (absent = none).
	Shadows map[string]int `json:"shadows,omitempty"`
	// SLO is the flow's service-level objectives (absent = none configured), against
	// which attainment and error-budget burn are reported.
	SLO       *events.SLOConfig `json:"slo,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Projector folds flow lifecycle events into FlowView documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_flows" }

// Collections lists the store collections this projector owns (reset on rebuild).
func (Projector) Collections() []string { return []string{Collection, slugIndexCollection} }

// flowAppliers dispatches each flow event type to its handler (a map keeps the
// dispatch flat — events of other types are simply absent and skipped).
var flowAppliers = map[string]func(context.Context, eventlog.Envelope, store.Store) error{
	events.TypeFlowCreated:             applyCreated,
	events.TypeFlowDetailsSet:          applyDetailsSet,
	events.TypeFlowVersionPublished:    applyPublished,
	events.TypeFlowVersionDeployed:     applyDeployed,
	events.TypeDeploymentRequested:     applyDeploymentRequested,
	events.TypeDeploymentApproved:      applyDeploymentApproved,
	events.TypeDeploymentRejected:      applyDeploymentRejected,
	events.TypePromotionPolicySet:      applyPromotionPolicySet,
	events.TypeShadowSet:               applyShadowSet,
	events.TypeSLOSet:                  applySLOSet,
	events.TypeFlowVersionRolledBack:   applyRolledBack,
	events.TypeDeployScheduleActivated: applyScheduleActivated,
	events.TypeDeployScheduleReverted:  applyScheduleReverted,
	events.TypeDeployScheduleCanceled:  applyScheduleCanceled,
}

// Apply maintains the flow document. Events of other types are not this
// projector's concern and are skipped (correct routing, not error-swallowing).
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if fn, ok := flowAppliers[e.Type]; ok {
		return fn(ctx, e, s)
	}
	return nil
}

// mutateFlow loads a flow, applies fn (which may set UpdatedAt), and writes it
// back — failing loudly when the flow is unknown.
func mutateFlow(ctx context.Context, s store.Store, e eventlog.Envelope, flowID string, fn func(*FlowView) error) error {
	var mutationErr error
	ok, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, flowID), func(fv *FlowView) {
		mutationErr = fn(fv)
		if mutationErr != nil {
			return
		}
		fv.UpdatedAt = e.Time
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("decision_flows: event seq %d for unknown flow %q", e.Seq, flowID)
	}
	return mutationErr
}

func applyDeploymentRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DeploymentRequested
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deployment_requested seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		fv.DeploymentRequests = append(fv.DeploymentRequests, DeploymentRequest{
			RequestID: p.RequestID, Environment: p.Environment, Version: p.Version,
			ChallengerVersion: p.ChallengerVersion, ChallengerPct: p.ChallengerPct,
			At: p.At, Until: p.Until,
			Status: RequestPending, RequestedBy: e.Actor, RequestedAt: e.Time,
		})
		return nil
	})
}

func applyDeploymentApproved(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DeploymentApproved
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deployment_approved seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		request, err := pendingRequest(fv, p.RequestID, e)
		if err != nil {
			return err
		}
		// An immediate approval deploys now. A future approval instead creates a
		// governed schedule in the schedule projector from this same event.
		if p.ScheduleID == "" {
			if fv.Deployments == nil {
				fv.Deployments = make(map[string]DeploymentView)
			}
			fv.Deployments[p.Environment] = DeploymentView{
				Version: p.Version, ChallengerVersion: p.ChallengerVersion, ChallengerPct: p.ChallengerPct,
			}
		}
		request.ScheduleID = p.ScheduleID
		decideRequest(request, RequestApproved, p.Reason, e)
		return nil
	})
}

// applyScheduleActivated makes activation and the live deployment one atomic
// projection transition. Legacy activation events have no environment/version;
// their companion FlowVersionDeployed event continues to supply the deployment.
func applyScheduleActivated(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DeployScheduleActivated
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deploy_schedule_activated seq %d: %w", e.Seq, err)
	}
	if p.Environment == "" || p.Version < 1 {
		return nil
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		if fv.Deployments == nil {
			fv.Deployments = make(map[string]DeploymentView)
		}
		fv.Deployments[p.Environment] = DeploymentView{Version: p.Version}
		return nil
	})
}

// applyScheduleReverted restores the captured prior version from the same event
// that closes the schedule. A zero version means there was nothing live before,
// so the environment becomes undeployed. A superseded schedule only closes its
// lifecycle: it must not overwrite a newer deliberate deployment.
func applyScheduleReverted(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DeployScheduleReverted
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deploy_schedule_reverted seq %d: %w", e.Seq, err)
	}
	if p.Environment == "" || p.Superseded {
		return nil
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		if live := fv.Deployments[p.Environment].Version; live != p.FromVersion {
			return fmt.Errorf(
				"decision_flows: schedule %q revert at seq %d expected %s v%d live, got v%d",
				p.ScheduleID, e.Seq, p.Environment, p.FromVersion, live,
			)
		}
		if p.Version < 1 {
			delete(fv.Deployments, p.Environment)
			return nil
		}
		if fv.Deployments == nil {
			fv.Deployments = make(map[string]DeploymentView)
		}
		fv.Deployments[p.Environment] = DeploymentView{Version: p.Version}
		return nil
	})
}

// applyScheduleCanceled makes canceling an active schedule an atomic deployment
// transition too. Pending cancellations (and active schedules superseded by a
// newer deploy) do not touch the live environment.
func applyScheduleCanceled(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DeployScheduleCanceled
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deploy_schedule_canceled seq %d: %w", e.Seq, err)
	}
	if !p.Active || p.Environment == "" || p.Superseded {
		return nil
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		if live := fv.Deployments[p.Environment].Version; live != p.FromVersion {
			return fmt.Errorf(
				"decision_flows: active schedule %q cancel at seq %d expected %s v%d live, got v%d",
				p.ScheduleID, e.Seq, p.Environment, p.FromVersion, live,
			)
		}
		if p.Version < 1 {
			delete(fv.Deployments, p.Environment)
			return nil
		}
		fv.Deployments[p.Environment] = DeploymentView{Version: p.Version}
		return nil
	})
}

func applyDeploymentRejected(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DeploymentRejected
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deployment_rejected seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		request, err := pendingRequest(fv, p.RequestID, e)
		if err != nil {
			return err
		}
		decideRequest(request, RequestRejected, p.Reason, e)
		return nil
	})
}

func applyPromotionPolicySet(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.PromotionPolicySet
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode promotion_policy_set seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		fv.PromotionPolicy = EffectivePromotionPolicy(p.Policy)
		return nil
	})
}

func applySLOSet(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.SLOSet
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode slo_set seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		// A zeroed objective clears the SLO (no targets) rather than storing an empty one.
		if p.SLO.SuccessTarget == 0 && p.SLO.LatencyTargetMS == 0 {
			fv.SLO = nil
			return nil
		}
		slo := p.SLO
		fv.SLO = &slo
		return nil
	})
}

func applyShadowSet(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ShadowSet
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode shadow_set seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		if p.Version == 0 {
			delete(fv.Shadows, p.Environment)
			return nil
		}
		if fv.Shadows == nil {
			fv.Shadows = map[string]int{}
		}
		fv.Shadows[p.Environment] = p.Version
		return nil
	})
}

// pendingRequest resolves the exact pending request a terminal event is allowed
// to transition. A missing or already-decided request means the event stream is
// impossible; replay must stop before it mutates a deployment.
func pendingRequest(fv *FlowView, reqID string, e eventlog.Envelope) (*DeploymentRequest, error) {
	for i := range fv.DeploymentRequests {
		if fv.DeploymentRequests[i].RequestID != reqID {
			continue
		}
		if fv.DeploymentRequests[i].Status != RequestPending {
			return nil, fmt.Errorf(
				"decision_flows: request %q already %s at seq %d",
				reqID, fv.DeploymentRequests[i].Status, e.Seq,
			)
		}
		return &fv.DeploymentRequests[i], nil
	}
	return nil, fmt.Errorf("decision_flows: terminal event seq %d for unknown request %q", e.Seq, reqID)
}

// decideRequest stamps a validated request's terminal status, decider, and time.
func decideRequest(request *DeploymentRequest, status RequestStatus, reason string, e eventlog.Envelope) {
	request.Status = status
	request.Reason = reason
	request.DecidedBy = e.Actor
	request.DecidedAt = e.Time
}

func applyCreated(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.FlowCreated
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode created seq %d: %w", e.Seq, err)
	}
	fv := FlowView{
		Org:         e.Org,
		Workspace:   e.Workspace,
		FlowID:      p.FlowID,
		Slug:        p.Slug,
		Name:        p.Name,
		Description: p.Description,
		CreatedAt:   e.Time,
		UpdatedAt:   e.Time,
	}
	if err := store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.FlowID), fv); err != nil {
		return err
	}
	return store.PutDoc(ctx, s, slugIndexCollection, store.Key(e.Org, e.Workspace, p.Slug), slugRef{FlowID: p.FlowID})
}

// applyDetailsSet overwrites the flow's mutable details; the event carries the
// full resolved values, so no per-field branching is needed here.
func applyDetailsSet(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.FlowDetailsSet
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode details_set seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		fv.Name = p.Name
		fv.Description = p.Description
		return nil
	})
}

func applyPublished(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.FlowVersionPublished
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode published seq %d: %w", e.Seq, err)
	}
	key := store.Key(e.Org, e.Workspace, p.FlowID)
	fv, ok, err := store.GetDoc[FlowView](ctx, s, Collection, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("decision_flows: published seq %d for unknown flow %q", e.Seq, p.FlowID)
	}
	fv.Versions = append(fv.Versions, VersionView{
		Version:     p.Version,
		Etag:        p.Etag,
		Graph:       p.Graph,
		InputSchema: p.InputSchema,
		PublishedAt: e.Time,
		PublishedBy: e.Actor,
	})
	fv.Latest = p.Version
	fv.UpdatedAt = e.Time
	return store.PutDoc(ctx, s, Collection, key, fv)
}

func applyDeployed(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.FlowVersionDeployed
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deployed seq %d: %w", e.Seq, err)
	}
	key := store.Key(e.Org, e.Workspace, p.FlowID)
	fv, ok, err := store.GetDoc[FlowView](ctx, s, Collection, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("decision_flows: deployed seq %d for unknown flow %q", e.Seq, p.FlowID)
	}
	if fv.Deployments == nil {
		fv.Deployments = make(map[string]DeploymentView)
	}
	fv.Deployments[p.Environment] = DeploymentView{
		Version:           p.Version,
		ChallengerVersion: p.ChallengerVersion,
		ChallengerPct:     p.ChallengerPct,
	}
	fv.UpdatedAt = e.Time
	return store.PutDoc(ctx, s, Collection, key, fv)
}

// applyRolledBack makes a previously-live version live again in an environment,
// clearing any challenger (a rollback returns to a single known-good version).
func applyRolledBack(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.FlowVersionRolledBack
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode rolled_back seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) error {
		if fv.Deployments == nil {
			fv.Deployments = make(map[string]DeploymentView)
		}
		fv.Deployments[p.Environment] = DeploymentView{Version: p.Version}
		return nil
	})
}

// Read returns the flow with the given id for id's tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, flowID string) (FlowView, bool, error) {
	fv, ok, err := store.GetDoc[FlowView](ctx, s, Collection, store.Key(id.Org, id.Workspace, flowID))
	if ok {
		fv.PromotionPolicy = EffectivePromotionPolicy(fv.PromotionPolicy)
	}
	return fv, ok, err
}

// GraphForVersion returns a flow version's graph (version 0 = latest published).
func GraphForVersion(fv FlowView, version int) (events.Graph, error) {
	want := version
	if want == 0 {
		want = fv.Latest
	}
	for _, v := range fv.Versions {
		if v.Version == want {
			return v.Graph, nil
		}
	}
	return events.Graph{}, fmt.Errorf("decision_flows: flow has no version %d", want)
}

// BySlug returns the flow with the given slug for id's tenant. Slugs are unique
// per tenant, so at most one matches; it is the decide path's flow lookup.
func BySlug(ctx context.Context, s store.Store, id identity.Identity, slug string) (FlowView, bool, error) {
	// The owned slug index resolves directly to a flow id on the decide hot path.
	// Projector rebuilds create the flow and index together, so an index miss is a
	// real not-found result and a dangling index is corrupt projection state.
	ref, indexed, err := store.GetDoc[slugRef](ctx, s, slugIndexCollection, store.Key(id.Org, id.Workspace, slug))
	if err != nil {
		return FlowView{}, false, fmt.Errorf("decision-engine: read slug index %q: %w", slug, err)
	}
	if indexed {
		fv, found, err := store.GetDoc[FlowView](ctx, s, Collection, store.Key(id.Org, id.Workspace, ref.FlowID))
		if err != nil {
			return FlowView{}, false, fmt.Errorf("decision-engine: read flow %q: %w", ref.FlowID, err)
		}
		if !found {
			return FlowView{}, false, fmt.Errorf(
				"decision-engine: slug index %q points to missing flow %q",
				slug, ref.FlowID,
			)
		}
		fv.PromotionPolicy = EffectivePromotionPolicy(fv.PromotionPolicy)
		return fv, true, nil
	}
	return FlowView{}, false, nil
}

// List returns all flows for id's tenant, ordered by store key.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]FlowView, error) {
	fvs, err := store.ListDocs[FlowView](ctx, s, Collection, store.Key(id.Org, id.Workspace, ""))
	for i := range fvs {
		fvs[i].PromotionPolicy = EffectivePromotionPolicy(fvs[i].PromotionPolicy)
	}
	return fvs, err
}

// DefaultPromotionPolicy preserves the existing promotion behavior. Production is
// governed by four-eyes review (RequireReview), so its deployment never runs the
// automated health gate — a human checker decides. AllowForce is pinned false there
// as defense-in-depth: force is gated by no privilege of its own, so no future code
// path can wave a production deploy past the gates on the strength of a force flag.
func DefaultPromotionPolicy() map[string]events.PromotionStagePolicy {
	return map[string]events.PromotionStagePolicy{
		"sandbox": {
			RequireAssertions:       true,
			RequireNoFiringMonitors: true,
			AllowForce:              true,
			RequireReview:           false,
		},
		"staging": {
			RequireAssertions:       true,
			RequireNoFiringMonitors: true,
			AllowForce:              true,
			RequireReview:           false,
		},
		"production": {
			RequireAssertions:       true,
			RequireNoFiringMonitors: true,
			AllowForce:              false,
			RequireReview:           true,
		},
	}
}

// EffectivePromotionPolicy fills missing stages from the default and forces the
// non-negotiable production requirements: review, and no force override.
func EffectivePromotionPolicy(policy map[string]events.PromotionStagePolicy) map[string]events.PromotionStagePolicy {
	effective := DefaultPromotionPolicy()
	for env, stage := range policy {
		if env == "production" {
			stage.RequireReview = true
			stage.AllowForce = false
		}
		effective[env] = stage
	}
	return effective
}
