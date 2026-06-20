// SPDX-License-Identifier: AGPL-3.0-or-later

// Package preapproval is the pre-approval framework: durable, time-boxed
// pre-decisions for an entity (granted in bulk from a policy run) that the decide
// path honors instantly at request time. A grant records the disposition + the
// offer terms + provenance (policy/flow that produced it) + a validity window;
// honoring is recorded as an effect so replay is stable.
package preapproval

import (
	"encoding/json"
	"time"
)

// StreamPreApprovals is the event stream for pre-approval lifecycle.
const StreamPreApprovals = "decision.preapprovals"

// A pre-approval's disposition uses the shared policy.Disposition vocabulary
// (approve | decline — never refer); it is validated in the command and stored
// as a string on the event payload (the wire boundary).

// Lifecycle event types.
const (
	TypeGranted = "decision.preapproval.granted"
	TypeRevoked = "decision.preapproval.revoked"
	TypeHonored = "decision.preapproval.honored"
)

// Granted records a durable pre-decision for an entity, valid until ValidUntil,
// with the offer terms and the provenance that produced it.
type Granted struct {
	PreApprovalID string          `json:"preapproval_id"`
	EntityType    string          `json:"entity_type"`
	EntityID      string          `json:"entity_id"`
	Disposition   string          `json:"disposition"` // approve | decline
	Terms         json.RawMessage `json:"terms,omitempty"`
	PolicyID      string          `json:"policy_id,omitempty"`
	PolicyVersion int             `json:"policy_version,omitempty"`
	FlowSlug      string          `json:"flow_slug,omitempty"`
	ValidUntil    time.Time       `json:"valid_until"`
	Note          string          `json:"note,omitempty"`
}

// Revoked invalidates an entity's current pre-approval before it expires.
type Revoked struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Reason     string `json:"reason,omitempty"`
}

// Honored records that a decision was served from a pre-approval (the decide path
// short-circuited the flow). Recorded so replay reads the honor, not a re-lookup.
type Honored struct {
	PreApprovalID string `json:"preapproval_id"`
	EntityType    string `json:"entity_type"`
	EntityID      string `json:"entity_id"`
	DecisionID    string `json:"decision_id"`
}
