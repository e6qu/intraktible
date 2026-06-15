// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import "encoding/json"

// StreamDecisions is the event stream for decision runs. Each /decide call is a
// DecisionStarted, every node a NodeEvaluated, and the run ends with
// DecisionCompleted or DecisionFailed. This stream IS the replayable decision
// history (PLAN.md §3.3).
const StreamDecisions = "decision.runs"

// Decision run event types.
const (
	TypeDecisionStarted   = "decision.run.started"
	TypeNodeEvaluated     = "decision.run.node_evaluated"
	TypeDecisionCompleted = "decision.run.completed"
	TypeDecisionFailed    = "decision.run.failed"
	// TypeManualReviewRequested is emitted when a decision reaches a manual_review
	// node; the Case Manager consumes it to open a case (escalation hook).
	TypeManualReviewRequested = "decision.manual_review_requested"
)

// DecisionStarted records the start of a decision: which flow version ran against
// what input, in which environment. The recorded Data makes the run replayable.
type DecisionStarted struct {
	DecisionID  string          `json:"decision_id"`
	FlowID      string          `json:"flow_id"`
	Slug        string          `json:"slug"`
	Version     int             `json:"version"`
	Environment string          `json:"environment"`
	Variant     string          `json:"variant,omitempty"` // champion | challenger
	Data        json.RawMessage `json:"data"`
}

// NodeEvaluated records one node's evaluation and its output, in execution order.
type NodeEvaluated struct {
	DecisionID string          `json:"decision_id"`
	NodeID     string          `json:"node_id"`
	NodeType   NodeType        `json:"node_type"`
	Output     json.RawMessage `json:"output,omitempty"`
}

// DecisionCompleted records a successful decision and its output. Flow context
// (flow/version/variant) is carried so the read side can attribute the outcome
// without correlating back to DecisionStarted.
type DecisionCompleted struct {
	DecisionID string          `json:"decision_id"`
	FlowID     string          `json:"flow_id"`
	Version    int             `json:"version"`
	Variant    string          `json:"variant,omitempty"`
	Output     json.RawMessage `json:"output"`
	DurationMS int64           `json:"duration_ms"`
}

// ManualReviewRequested is raised when a decision runs a manual_review node. It
// carries a recorded case_id (so replay is stable) and the case fields evaluated
// from the node, plus the decision's input as context. The Case Manager opens a
// case from it, linked by DecisionID.
type ManualReviewRequested struct {
	CaseID      string          `json:"case_id"`
	DecisionID  string          `json:"decision_id"`
	NodeID      string          `json:"node_id"`
	CompanyName string          `json:"company_name"`
	CaseType    string          `json:"case_type"`
	SLADays     int             `json:"sla_days"`
	Context     json.RawMessage `json:"context,omitempty"`
}

// DecisionFailed records a decision that errored during evaluation (fail loudly:
// the failure is recorded, not swallowed). It carries flow context for the same
// reason as DecisionCompleted.
type DecisionFailed struct {
	DecisionID string `json:"decision_id"`
	FlowID     string `json:"flow_id"`
	Version    int    `json:"version"`
	Variant    string `json:"variant,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	Error      string `json:"error"`
	DurationMS int64  `json:"duration_ms"`
}
