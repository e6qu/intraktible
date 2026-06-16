// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events defines the Decision Engine's event payloads and the flow data
// model they carry. A flow is a versioned DAG of typed nodes; publishing a
// version appends an immutable event, so the full edit history is replayable.
package events

import "encoding/json"

// StreamFlows is the event stream for flow lifecycle (creation, version publish).
const StreamFlows = "decision.flows"

// Flow lifecycle event types.
const (
	TypeFlowCreated          = "decision.flow.created"
	TypeFlowVersionPublished = "decision.flow.version_published"
	TypeFlowVersionDeployed  = "decision.flow.version_deployed"
	// Maker-checker (four-eyes) change control on deployments.
	TypeDeploymentRequested = "decision.flow.deployment_requested"
	TypeDeploymentApproved  = "decision.flow.deployment_approved"
	TypeDeploymentRejected  = "decision.flow.deployment_rejected"
)

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
	NodeManualReview  NodeType = "manual_review"
	NodeReason        NodeType = "reason"
	NodeOutput        NodeType = "output"
)

// Node is one vertex of a flow graph. Config is opaque per-type configuration,
// validated by each node engine at decide time (not by the graph structure).
type Node struct {
	ID     string          `json:"id"`
	Type   NodeType        `json:"type"`
	Name   string          `json:"name,omitempty"`
	Config json.RawMessage `json:"config,omitempty"`
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
}

// DeploymentRejected records that a proposed deployment was rejected.
type DeploymentRejected struct {
	RequestID string `json:"request_id"`
	FlowID    string `json:"flow_id"`
	Reason    string `json:"reason,omitempty"`
}
