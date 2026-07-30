// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import (
	"encoding/json"
	"time"
)

// StreamDecisions is the event stream for decision runs. Each /decide call is a
// DecisionStarted, every node a NodeEvaluated, and the run ends with
// DecisionCompleted or DecisionFailed. This stream IS the replayable decision
// history (PLAN.md §3.3).
const StreamDecisions = "decision.runs"

// Decision run event types.
const (
	TypeDecisionStarted      = "decision.run.started"
	TypeContextPrepared      = "decision.run.context_prepared"
	TypeNodeEvaluated        = "decision.run.node_evaluated"
	TypeEffectRequested      = "decision.run.effect_requested"
	TypeEffectSucceeded      = "decision.run.effect_succeeded"
	TypeEffectFailed         = "decision.run.effect_failed"
	TypeDecisionCompleted    = "decision.run.completed"
	TypeDecisionFailed       = "decision.run.failed"
	TypeDecisionAbandoned    = "decision.run.abandoned"
	TypeDecisionFinalized    = "decision.run.finalized"
	TypeExecutionInterrupted = "decision.run.execution_interrupted"
	TypeRecoveryClaimed      = "decision.run.recovery_claimed"
	TypeRecoveryHeartbeat    = "decision.run.recovery_heartbeat"
	// TypeDecisionSuspended records a decision paused at a durable human task (a
	// manual_review node with suspend set); it carries the instance state needed to
	// resume. TypeDecisionResumed records the reviewer's outcome that un-pauses it
	// (the run then ends with a DecisionCompleted/DecisionFailed as usual).
	TypeDecisionSuspended = "decision.run.suspended"
	TypeDecisionResumed   = "decision.run.resumed"
	// TypeManualReviewRequested is emitted when a decision reaches a manual_review
	// node; the Case Manager consumes it to open a case (escalation hook).
	TypeManualReviewRequested = "decision.manual_review_requested"
	// TypeShadowEvaluated records that a shadow version was run alongside a live
	// decision for comparison; its result is never returned to the caller.
	TypeShadowEvaluated = "decision.run.shadow_evaluated"
)

// DecisionFinalized is the commit marker for the whole synchronous invocation,
// including terminal decision state and immediate case/shadow side effects. An
// idempotent retry waits for this marker before returning the original result.
type DecisionFinalized struct {
	DecisionID string `json:"decision_id"`
	ResultSeq  uint64 `json:"result_seq"`
	Generation int    `json:"generation,omitempty"`
}

// DecisionExecutionInterrupted releases the initial execution's recovery delay
// when the command returned an infrastructure error cleanly. A hard process loss
// cannot emit it, so workers wait until DecisionStarted.RecoveryAfter instead.
type DecisionExecutionInterrupted struct {
	DecisionID string    `json:"decision_id"`
	Generation int       `json:"generation"`
	Error      string    `json:"error"`
	At         time.Time `json:"at"`
}

// DecisionRecoveryClaimed assigns one unfinished invocation generation to one
// worker for a bounded lease. The unique (decision,generation,attempt) claim on
// its envelope makes concurrent workers contend durably across replicas.
type DecisionRecoveryClaimed struct {
	DecisionID  string    `json:"decision_id"`
	Generation  int       `json:"generation"`
	Owner       string    `json:"owner"`
	Attempt     int       `json:"attempt"`
	LeaseUntil  time.Time `json:"lease_until"`
	PreviousErr string    `json:"previous_error,omitempty"`
}

// DecisionRecoveryHeartbeat extends the active claim while the same owner is
// still working. A successor may claim the next attempt only after this lease.
type DecisionRecoveryHeartbeat struct {
	DecisionID string    `json:"decision_id"`
	Generation int       `json:"generation"`
	Owner      string    `json:"owner"`
	Attempt    int       `json:"attempt"`
	LeaseUntil time.Time `json:"lease_until"`
}

// DecisionEffectRequested records the exact graph-position effect the pure
// interpreter yielded before the shell performs I/O. InputHash is evidence of the
// reached record without duplicating subject data into another event.
type DecisionEffectRequested struct {
	DecisionID      string `json:"decision_id"`
	EffectID        string `json:"effect_id"`
	Scope           string `json:"scope"` // live | shadow
	FlowID          string `json:"flow_id"`
	Version         int    `json:"version"`
	NodeID          string `json:"node_id"`
	Kind            string `json:"kind"` // connect | ai | predict
	Reference       string `json:"reference"`
	ProviderVersion int    `json:"provider_version,omitempty"`
	Output          string `json:"output"`
	InputHash       string `json:"input_hash"`
	Attempt         int    `json:"attempt"`
	// Delivery is replay_safe, provider_idempotent, or at_least_once. It records
	// the recovery guarantee before the call so an indeterminate attempt can never
	// be retried under a stronger assumption learned later.
	Delivery string          `json:"delivery"`
	State    json.RawMessage `json:"state"` // sealed interpreter state before the effect
}

// DecisionEffectSucceeded records the provider result used to resume the pure
// interpreter. Output is the same value represented by the node trace, retained
// here with effect identity so attempts and recovery are independently auditable.
type DecisionEffectSucceeded struct {
	DecisionID string          `json:"decision_id"`
	EffectID   string          `json:"effect_id"`
	NodeID     string          `json:"node_id"`
	Kind       string          `json:"kind"`
	Attempt    int             `json:"attempt"`
	Output     json.RawMessage `json:"output"`
	DurationMS int64           `json:"duration_ms"`
}

// DecisionEffectFailed records an attempted provider/gate failure. It is terminal
// for the current decision attempt and is never represented as a successful empty
// result.
type DecisionEffectFailed struct {
	DecisionID string `json:"decision_id"`
	EffectID   string `json:"effect_id"`
	NodeID     string `json:"node_id"`
	Kind       string `json:"kind"`
	Attempt    int    `json:"attempt"`
	Error      string `json:"error"`
	DurationMS int64  `json:"duration_ms"`
}

// ShadowMatchBasis says which stable business contract was compared. A bound
// operational policy makes its disposition/code/reason the governed outcome;
// without one, the complete flow output remains the only available outcome.
type ShadowMatchBasis string

const (
	ShadowMatchOutput ShadowMatchBasis = "output"
	ShadowMatchPolicy ShadowMatchBasis = "policy"
)

// ShadowEvaluated records a shadow run: a candidate version evaluated from the
// same caller input and authoritative entity-feature snapshot as a live decision,
// for divergence analysis only. Candidate connector/AI/model dependencies are
// resolved independently. The shadow's outcome never affects the caller's result.
type ShadowEvaluated struct {
	DecisionID        string           `json:"decision_id"` // the live decision this shadows
	FlowID            string           `json:"flow_id"`
	Environment       string           `json:"environment"`
	LiveVersion       int              `json:"live_version"`
	ShadowVersion     int              `json:"shadow_version"`
	MatchBasis        ShadowMatchBasis `json:"match_basis"`
	PolicyID          string           `json:"policy_id,omitempty"`
	PolicyVersion     int              `json:"policy_version,omitempty"`
	LiveStatus        string           `json:"live_status"`
	ShadowStatus      string           `json:"shadow_status"`
	LiveDisposition   string           `json:"live_disposition,omitempty"`
	ShadowDisposition string           `json:"shadow_disposition,omitempty"`
	LiveCode          string           `json:"live_code,omitempty"`
	ShadowCode        string           `json:"shadow_code,omitempty"`
	LiveReason        string           `json:"live_reason,omitempty"`
	ShadowReason      string           `json:"shadow_reason,omitempty"`
	ChangedFields     []string         `json:"changed_fields,omitempty"`
	Matched           bool             `json:"matched"`
	ShadowError       string           `json:"shadow_error,omitempty"`
}

// DecisionStarted records the start of a decision: which flow version ran against
// what caller input, in which environment. DecisionContextPrepared records the
// authoritative feature/consent snapshot used by the interpreter.
type DecisionStarted struct {
	DecisionID  string          `json:"decision_id"`
	FlowID      string          `json:"flow_id"`
	Slug        string          `json:"slug"`
	Version     int             `json:"version"`
	Environment string          `json:"environment"`
	Variant     string          `json:"variant,omitempty"` // champion | challenger
	EntityType  string          `json:"entity_type,omitempty"`
	EntityID    string          `json:"entity_id,omitempty"`
	Data        json.RawMessage `json:"data"`
	// Caller identity and retry fields are first-class tracking dimensions. Only
	// the idempotency-key digest is retained; the caller's secret key is not.
	IdempotencyKeyHash string           `json:"idempotency_key_hash,omitempty"`
	RequestHash        string           `json:"request_hash,omitempty"`
	BusinessReference  string           `json:"business_reference,omitempty"`
	CorrelationID      string           `json:"correlation_id,omitempty"`
	Metadata           json.RawMessage  `json:"metadata,omitempty"`
	Control            ExecutionControl `json:"control,omitempty"`
	// RecoveryAfter is the hard upper bound of the synchronous owner's lease.
	// A recovery worker must not claim this generation before it, unless an
	// ExecutionInterrupted event explicitly releases the lease.
	RecoveryAfter time.Time `json:"recovery_after,omitempty"`
	// PolicySelectionRecorded distinguishes a new decision that deliberately had
	// no bound policy from a legacy event that predates policy snapshots.
	PolicySelectionRecorded bool   `json:"policy_selection_recorded,omitempty"`
	PolicyID                string `json:"policy_id,omitempty"`
	PolicyVersion           int    `json:"policy_version,omitempty"`
	// Pre-approval fields snapshot the fast-path grant so recovery never has to
	// re-read a grant that may have expired or been revoked after acceptance.
	PreApprovalID          string          `json:"preapproval_id,omitempty"`
	PreApprovalDisposition string          `json:"preapproval_disposition,omitempty"`
	PreApprovalTerms       json.RawMessage `json:"preapproval_terms,omitempty"`
}

// ExecutionControl is the bounded, persisted per-invocation execution contract.
// Zero values select server defaults.
type ExecutionControl struct {
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

// DecisionContextPrepared records the authoritative working context after feature
// snapshotting and consent capture, before the graph requests its first effect.
type DecisionContextPrepared struct {
	DecisionID string          `json:"decision_id"`
	Data       json.RawMessage `json:"data"`
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
	// The operational policy's disposition for this decision (approve|decline|
	// refer), plus the policy that assigned it — recorded so it is replay-stable.
	// Empty when no policy is bound to the flow.
	Disposition       string `json:"disposition,omitempty"`
	DispositionCode   string `json:"disposition_code,omitempty"`
	DispositionReason string `json:"disposition_reason,omitempty"`
	PolicyID          string `json:"policy_id,omitempty"`
	PolicyVersion     int    `json:"policy_version,omitempty"`
	// PreApprovalID is set when the decision was served instantly from a
	// pre-approval (the flow was skipped); the output carries the stored terms.
	PreApprovalID string `json:"preapproval_id,omitempty"`
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

// DecisionSuspended records a decision paused at a durable human task. State is the
// captured instance (the record at the pause, the node to resume into, the inject
// key, and the case) — enough to deterministically resume. CaseID links the case a
// reviewer acts on. The decision is non-terminal until a DecisionResumed + terminal.
type DecisionSuspended struct {
	DecisionID string          `json:"decision_id"`
	FlowID     string          `json:"flow_id"`
	Version    int             `json:"version"`
	Variant    string          `json:"variant,omitempty"`
	NodeID     string          `json:"node_id"`
	ResumeNode string          `json:"resume_node,omitempty"`
	CaseID     string          `json:"case_id,omitempty"`
	State      json.RawMessage `json:"state"`
	DurationMS int64           `json:"duration_ms"`
}

// DecisionResumed records the reviewer's outcome that un-pauses a suspended decision.
// The outcome is injected into the record and the flow runs on to a terminal event.
type DecisionResumed struct {
	DecisionID    string          `json:"decision_id"`
	CaseID        string          `json:"case_id,omitempty"`
	Actor         string          `json:"actor"`
	Outcome       json.RawMessage `json:"outcome"`
	RecoveryAfter time.Time       `json:"recovery_after,omitempty"`
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

// DecisionAbandoned is a terminal recovery outcome: the platform found an
// indeterminate at-least-once provider call and refused to duplicate it, or
// exhausted the bounded recovery attempts. It is distinct from an evaluated
// flow failure so operators know manual reconciliation is required.
type DecisionAbandoned struct {
	DecisionID string `json:"decision_id"`
	FlowID     string `json:"flow_id"`
	Version    int    `json:"version"`
	Variant    string `json:"variant,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	Error      string `json:"error"`
	Attempt    int    `json:"attempt"`
}
