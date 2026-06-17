// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

// StreamPolicies is the event stream for policy lifecycle.
const StreamPolicies = "decision.policies"

// Policy lifecycle event types.
const (
	TypePolicyCreated          = "decision.policy.created"
	TypePolicyVersionPublished = "decision.policy.version_published"
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
