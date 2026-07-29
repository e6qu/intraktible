// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

// StreamPolicies is the event stream for policy lifecycle.
const StreamPolicies = "decision.policies"

// Policy lifecycle event types.
const (
	TypePolicyCreated           = "decision.policy.created"
	TypePolicyVersionPublished  = "decision.policy.version_published"
	TypePolicyApprovalRequested = "decision.policy.approval_requested"
	TypePolicyApprovalApproved  = "decision.policy.approval_approved"
	TypePolicyApprovalRejected  = "decision.policy.approval_rejected"
)

// Created records a new (unversioned) policy bound to a flow slug.
type Created struct {
	PolicyID string `json:"policy_id"`
	Name     string `json:"name"`
	FlowSlug string `json:"flow_slug"`
}

// VersionPublished records an immutable policy version: the disposition spec,
// numbered monotonically per policy and stamped with a content etag.
type VersionPublished struct {
	PolicyID string `json:"policy_id"`
	Version  int    `json:"version"`
	Etag     string `json:"etag"`
	Spec     Spec   `json:"spec"`
}

// ApprovalRequested proposes the current policy version for four-eyes review.
// Version pins the submitted content so publishing again makes the request stale.
type ApprovalRequested struct {
	RequestID string `json:"request_id"`
	PolicyID  string `json:"policy_id"`
	Name      string `json:"name,omitempty"`
	Version   int    `json:"version"`
}

// ApprovalApproved records a checker approving one pending policy version for
// non-sandbox decisions. The prior approved version remains serving until this
// event commits.
type ApprovalApproved struct {
	RequestID string `json:"request_id"`
	PolicyID  string `json:"policy_id"`
	Version   int    `json:"version"`
	Reason    string `json:"reason,omitempty"`
}

// ApprovalRejected records a checker rejecting one pending policy version.
type ApprovalRejected struct {
	RequestID string `json:"request_id"`
	PolicyID  string `json:"policy_id"`
	Version   int    `json:"version"`
	Reason    string `json:"reason,omitempty"`
}
