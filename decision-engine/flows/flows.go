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

// Collection is the store collection holding flow documents.
const Collection = "decision_flows"

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

// DeploymentRequest is one maker-checker change request and its decision status.
type DeploymentRequest struct {
	RequestID         string    `json:"request_id"`
	Environment       string    `json:"environment"`
	Version           int       `json:"version"`
	ChallengerVersion int       `json:"challenger_version,omitempty"`
	ChallengerPct     int       `json:"challenger_pct,omitempty"`
	Status            string    `json:"status"` // pending | approved | rejected
	Reason            string    `json:"reason,omitempty"`
	RequestedBy       string    `json:"requested_by"`
	RequestedAt       time.Time `json:"requested_at"`
	DecidedBy         string    `json:"decided_by,omitempty"`
	DecidedAt         time.Time `json:"decided_at,omitempty"`
}

// FlowView is the materialized read model for one flow.
type FlowView struct {
	Org                string                                 `json:"org"`
	Workspace          string                                 `json:"workspace"`
	FlowID             string                                 `json:"flow_id"`
	Slug               string                                 `json:"slug"`
	Name               string                                 `json:"name"`
	Latest             int                                    `json:"latest"`
	Versions           []VersionView                          `json:"versions"`
	Deployments        map[string]DeploymentView              `json:"deployments,omitempty"`
	DeploymentRequests []DeploymentRequest                    `json:"deployment_requests,omitempty"`
	PromotionPolicy    map[string]events.PromotionStagePolicy `json:"promotion_policy,omitempty"`
	CreatedAt          time.Time                              `json:"created_at"`
	UpdatedAt          time.Time                              `json:"updated_at"`
}

// Projector folds flow lifecycle events into FlowView documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "decision_flows" }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// flowAppliers dispatches each flow event type to its handler (a map keeps the
// dispatch flat — events of other types are simply absent and skipped).
var flowAppliers = map[string]func(context.Context, eventlog.Envelope, store.Store) error{
	events.TypeFlowCreated:          applyCreated,
	events.TypeFlowVersionPublished: applyPublished,
	events.TypeFlowVersionDeployed:  applyDeployed,
	events.TypeDeploymentRequested:  applyDeploymentRequested,
	events.TypeDeploymentApproved:   applyDeploymentApproved,
	events.TypeDeploymentRejected:   applyDeploymentRejected,
	events.TypePromotionPolicySet:   applyPromotionPolicySet,
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
func mutateFlow(ctx context.Context, s store.Store, e eventlog.Envelope, flowID string, fn func(*FlowView)) error {
	ok, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, flowID), func(fv *FlowView) {
		fn(fv)
		fv.UpdatedAt = e.Time
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("decision_flows: event seq %d for unknown flow %q", e.Seq, flowID)
	}
	return nil
}

func applyDeploymentRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DeploymentRequested
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deployment_requested seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) {
		fv.DeploymentRequests = append(fv.DeploymentRequests, DeploymentRequest{
			RequestID: p.RequestID, Environment: p.Environment, Version: p.Version,
			ChallengerVersion: p.ChallengerVersion, ChallengerPct: p.ChallengerPct,
			Status: "pending", RequestedBy: e.Actor, RequestedAt: e.Time,
		})
	})
}

func applyDeploymentApproved(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DeploymentApproved
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deployment_approved seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) {
		// Approving deploys the version.
		if fv.Deployments == nil {
			fv.Deployments = make(map[string]DeploymentView)
		}
		fv.Deployments[p.Environment] = DeploymentView{
			Version: p.Version, ChallengerVersion: p.ChallengerVersion, ChallengerPct: p.ChallengerPct,
		}
		decideRequest(fv, p.RequestID, "approved", p.Reason, e)
	})
}

func applyDeploymentRejected(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.DeploymentRejected
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode deployment_rejected seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) {
		decideRequest(fv, p.RequestID, "rejected", p.Reason, e)
	})
}

func applyPromotionPolicySet(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.PromotionPolicySet
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode promotion_policy_set seq %d: %w", e.Seq, err)
	}
	return mutateFlow(ctx, s, e, p.FlowID, func(fv *FlowView) {
		fv.PromotionPolicy = EffectivePromotionPolicy(p.Policy)
	})
}

// decideRequest stamps a request's terminal status, decider, and time.
func decideRequest(fv *FlowView, reqID, status, reason string, e eventlog.Envelope) {
	for i := range fv.DeploymentRequests {
		if fv.DeploymentRequests[i].RequestID != reqID {
			continue
		}
		fv.DeploymentRequests[i].Status = status
		fv.DeploymentRequests[i].Reason = reason
		fv.DeploymentRequests[i].DecidedBy = e.Actor
		fv.DeploymentRequests[i].DecidedAt = e.Time
		return
	}
}

func applyCreated(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.FlowCreated
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("decision_flows: decode created seq %d: %w", e.Seq, err)
	}
	fv := FlowView{
		Org:       e.Org,
		Workspace: e.Workspace,
		FlowID:    p.FlowID,
		Slug:      p.Slug,
		Name:      p.Name,
		CreatedAt: e.Time,
		UpdatedAt: e.Time,
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.FlowID), fv)
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
	fvs, err := List(ctx, s, id)
	if err != nil {
		return FlowView{}, false, err
	}
	for _, fv := range fvs {
		if fv.Slug == slug {
			fv.PromotionPolicy = EffectivePromotionPolicy(fv.PromotionPolicy)
			return fv, true, nil
		}
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

// DefaultPromotionPolicy preserves the existing promotion behavior.
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
			AllowForce:              true,
			RequireReview:           true,
		},
	}
}

// EffectivePromotionPolicy fills missing stages from the default and forces the
// non-negotiable production review requirement.
func EffectivePromotionPolicy(policy map[string]events.PromotionStagePolicy) map[string]events.PromotionStagePolicy {
	effective := DefaultPromotionPolicy()
	for env, stage := range policy {
		if env == "production" {
			stage.RequireReview = true
		}
		effective[env] = stage
	}
	return effective
}
