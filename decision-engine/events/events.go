// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events defines the Decision Engine's event payloads and the flow data
// model they carry. A flow is a versioned DAG of typed nodes; publishing a
// version appends an immutable event, so the full edit history is replayable.
package events

import (
	"encoding/json"
	"time"
)

// StreamFlows is the event stream for flow lifecycle (creation, version publish).
const StreamFlows = "decision.flows"

// StreamModels is the event stream for the predictive-model registry.
const StreamModels = "decision.models"

// Model registry + drift event types.
const (
	TypeModelDefined          = "decision.model.defined"
	TypeModelBaselineCaptured = "decision.model.baseline_captured"
	TypeModelMonitorSet       = "decision.model.monitor_set"
	// Drift alert-transition events: the model-drift scheduler records the ok→firing
	// edge (Alerted) and the firing→ok edge (Resolved) so a steadily-drifting model
	// is pushed to webhooks once, not every tick — mirroring the flow monitor.
	TypeModelDriftAlerted  = "decision.model.drift_alerted"
	TypeModelDriftResolved = "decision.model.drift_resolved"
)

// ModelMonitorSet sets (Threshold > 0) or clears (Threshold <= 0) the PSI drift
// threshold a model alerts on.
type ModelMonitorSet struct {
	Name      string  `json:"name"`
	Threshold float64 `json:"threshold"`
}

// ModelDriftAlerted records that a model's PSI crossed its threshold (and the
// drift was pushed to webhooks). PSI/Threshold are captured for the audit trail.
type ModelDriftAlerted struct {
	Name      string  `json:"name"`
	PSI       float64 `json:"psi"`
	Threshold float64 `json:"threshold"`
}

// ModelDriftResolved records that a previously-alerting model's PSI fell back
// under its threshold.
type ModelDriftResolved struct {
	Name string `json:"name"`
}

// ModelDefined registers a named predictive model. Spec is the opaque, kind-specific
// model definition (logistic | gbm | expression | external), validated by the models package.
type ModelDefined struct {
	Name string          `json:"name"`
	Spec json.RawMessage `json:"spec"`
}

// ModelBaselineCaptured snapshots a model's current prediction-probability
// distribution as the reference that drift (PSI) is measured against.
type ModelBaselineCaptured struct {
	Name string `json:"name"`
}

// Flow lifecycle event types.
const (
	TypeFlowCreated          = "decision.flow.created"
	TypeFlowVersionPublished = "decision.flow.version_published"
	TypeFlowVersionDeployed  = "decision.flow.version_deployed"
	// Maker-checker (four-eyes) change control on deployments.
	TypeDeploymentRequested = "decision.flow.deployment_requested"
	TypeDeploymentApproved  = "decision.flow.deployment_approved"
	TypeDeploymentRejected  = "decision.flow.deployment_rejected"
	// Instant rollback + scheduled/time-boxed deploys.
	TypeFlowVersionRolledBack   = "decision.flow.version_rolled_back"
	TypeDeployScheduled         = "decision.flow.deploy_scheduled"
	TypeDeployScheduleActivated = "decision.flow.deploy_schedule_activated"
	TypeDeployScheduleReverted  = "decision.flow.deploy_schedule_reverted"
	TypeDeployScheduleCanceled  = "decision.flow.deploy_schedule_canceled"
	TypePromotionPolicySet      = "decision.flow.promotion_policy_set"
	// TypeShadowSet assigns (or clears) a per-environment shadow version: a
	// candidate evaluated alongside live decisions for divergence analysis.
	TypeShadowSet = "decision.flow.shadow_set"
	// TypeSLOSet records a flow's service-level objectives (success-rate + latency
	// targets) used to report attainment and error-budget burn.
	TypeSLOSet = "decision.flow.slo_set"
)

// SLOConfig is a flow's service-level objectives. SuccessTarget is the minimum
// acceptable fraction of decisions that complete (vs fail), in [0,1] — e.g. 0.99.
// LatencyTargetMS is the maximum acceptable average decision latency in ms (0 =
// no latency objective). A zero SuccessTarget means no availability objective.
type SLOConfig struct {
	SuccessTarget   float64 `json:"success_target"`
	LatencyTargetMS int64   `json:"latency_target_ms"`
}

// SLOSet records a flow's service-level objectives.
type SLOSet struct {
	FlowID string    `json:"flow_id"`
	SLO    SLOConfig `json:"slo"`
}

// ShadowSet assigns the shadow version for one environment (Version 0 clears it).
type ShadowSet struct {
	FlowID      string `json:"flow_id"`
	Environment string `json:"environment"`
	Version     int    `json:"version"`
}

// NodeType enumerates the node kinds in the MVP palette (PLAN.md §4.1). Input
// and Output bound the graph; the rest carry per-type config evaluated at decide
// time once the execution runtime lands.
type NodeType string

// The MVP node palette.
const (
	NodeInput         NodeType = "input"
	NodeRule          NodeType = "rule"
	NodeSplit         NodeType = "split"
	NodeAssignment    NodeType = "assignment"
	NodeScorecard     NodeType = "scorecard"
	NodeDecisionTable NodeType = "decision_table"
	NodeMatrix2D      NodeType = "2d_matrix"
	NodeCode          NodeType = "code"
	NodeAI            NodeType = "ai"
	NodeConnect       NodeType = "connect"
	NodePredict       NodeType = "predict"
	NodeManualReview  NodeType = "manual_review"
	NodeReason        NodeType = "reason"
	NodeOutput        NodeType = "output"
)

// Node is one vertex of a flow graph. Config is opaque per-type configuration,
// validated by each node engine at decide time (not by the graph structure).
// Position is the builder's saved canvas coordinate; the execution runtime never
// reads it, but persisting it keeps a flow's layout stable across edits/reloads.
// Lane is the swimlane the node belongs to (e.g. "Automated", "Underwriting") —
// also presentation/organizational, ignored by the runtime.
type Node struct {
	ID       string          `json:"id"`
	Type     NodeType        `json:"type"`
	Name     string          `json:"name,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
	Position *NodePosition   `json:"position,omitempty"`
	Lane     string          `json:"lane,omitempty"`
}

// NodePosition is a node's saved x/y on the builder canvas (presentation only).
type NodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Edge is a directed connection between two nodes. Branch labels a conditional
// outgoing edge (e.g. "yes"/"no" for a Split); empty for unconditional flow.
type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Branch string `json:"branch,omitempty"`
}

// Graph is the node/edge structure of one flow version. It is the unit that the
// execution runtime walks and that the builder UI renders.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// FlowCreated records a new (empty, unversioned) flow. Slug is the stable,
// URL-safe identifier used in the decide path; it is unique per tenant.
type FlowCreated struct {
	FlowID string `json:"flow_id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
}

// FlowVersionPublished records an immutable flow version. Version is monotonic
// per flow (1, 2, …); Etag is the content hash of (Graph, InputSchema), so an
// identical republish is detectable. InputSchema is the per-flow decide contract
// (JSON Schema), stored opaquely until schema validation lands.
type FlowVersionPublished struct {
	FlowID      string          `json:"flow_id"`
	Version     int             `json:"version"`
	Etag        string          `json:"etag"`
	Graph       Graph           `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// FlowVersionDeployed records which version is live in an environment, optionally
// with an A/B challenger version receiving ChallengerPct percent of decisions.
type FlowVersionDeployed struct {
	FlowID            string `json:"flow_id"`
	Environment       string `json:"environment"`
	Version           int    `json:"version"`
	ChallengerVersion int    `json:"challenger_version,omitempty"`
	ChallengerPct     int    `json:"challenger_pct,omitempty"`
}

// DeploymentRequested proposes a deployment for review (maker-checker). The
// proposer is the envelope actor; production deployments must go through this.
type DeploymentRequested struct {
	RequestID         string `json:"request_id"`
	FlowID            string `json:"flow_id"`
	Environment       string `json:"environment"`
	Version           int    `json:"version"`
	ChallengerVersion int    `json:"challenger_version,omitempty"`
	ChallengerPct     int    `json:"challenger_pct,omitempty"`
}

// DeploymentApproved records that a proposed deployment was approved by a
// different user (the checker, the envelope actor) — and is the event that
// actually deploys the version.
type DeploymentApproved struct {
	RequestID         string `json:"request_id"`
	FlowID            string `json:"flow_id"`
	Environment       string `json:"environment"`
	Version           int    `json:"version"`
	ChallengerVersion int    `json:"challenger_version,omitempty"`
	ChallengerPct     int    `json:"challenger_pct,omitempty"`
	Reason            string `json:"reason,omitempty"` // the approver's note (explanation)
}

// DeploymentRejected records that a proposed deployment was rejected.
type DeploymentRejected struct {
	RequestID string `json:"request_id"`
	FlowID    string `json:"flow_id"`
	Reason    string `json:"reason,omitempty"`
}

// FlowVersionRolledBack records an instant rollback: the environment is reverted to
// FromVersion's predecessor (Version), a previously-live version. Distinct from a
// deploy so the audit trail shows the revert explicitly. It deploys Version with no
// challenger (a rollback returns to a single known-good version).
type FlowVersionRolledBack struct {
	FlowID      string `json:"flow_id"`
	Environment string `json:"environment"`
	Version     int    `json:"version"`      // the version made live again
	FromVersion int    `json:"from_version"` // the version it replaced (for the trail)
}

// DeployScheduled records a future deployment: at At the Version goes live in
// Environment; if Until is set, the deploy is time-boxed and auto-reverts after it.
type DeployScheduled struct {
	ScheduleID  string     `json:"schedule_id"`
	FlowID      string     `json:"flow_id"`
	Environment string     `json:"environment"`
	Version     int        `json:"version"`
	At          time.Time  `json:"at"`
	Until       *time.Time `json:"until,omitempty"`
}

// DeployScheduleActivated marks a scheduled deploy as activated (so it is not
// re-activated), recording PriorVersion — the version that was live before — so a
// time-boxed schedule can revert to it.
type DeployScheduleActivated struct {
	ScheduleID   string `json:"schedule_id"`
	FlowID       string `json:"flow_id"`
	PriorVersion int    `json:"prior_version"`
}

// DeployScheduleReverted marks a time-boxed schedule as reverted after its window.
type DeployScheduleReverted struct {
	ScheduleID string `json:"schedule_id"`
	FlowID     string `json:"flow_id"`
}

// DeployScheduleCanceled cancels a pending (or active) schedule.
type DeployScheduleCanceled struct {
	ScheduleID string `json:"schedule_id"`
	FlowID     string `json:"flow_id"`
	Reason     string `json:"reason,omitempty"`
}

// PromotionStagePolicy is the gate applied when promoting into one target
// environment.
type PromotionStagePolicy struct {
	RequireAssertions       bool `json:"require_assertions"`
	RequireNoFiringMonitors bool `json:"require_no_firing_monitors"`
	AllowForce              bool `json:"allow_force"`
	RequireReview           bool `json:"require_review"`
}

// PromotionPolicySet records a flow's per-stage promotion gate policy.
type PromotionPolicySet struct {
	FlowID string                          `json:"flow_id"`
	Policy map[string]PromotionStagePolicy `json:"policy"`
}
