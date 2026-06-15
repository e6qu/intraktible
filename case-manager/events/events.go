// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events defines the Case Manager's event payloads. A case is opened by a
// ReviewRequested event (raised manually or escalated from a decision flow) and
// evolves through assignment, status, and note events — the full audit trail.
package events

import "encoding/json"

// StreamCases is the case-lifecycle event stream.
const StreamCases = "cases"

// Case lifecycle event types.
const (
	TypeReviewRequested   = "cases.review_requested"
	TypeCaseAssigned      = "cases.assigned"
	TypeCaseStatusChanged = "cases.status_changed"
	TypeCaseNoteAdded     = "cases.note_added"
	TypeCaseSLABreached   = "cases.sla_breached"
)

// ReviewRequested opens a case for human review.
type ReviewRequested struct {
	CaseID           string          `json:"case_id"`
	CompanyName      string          `json:"company_name"`
	CaseType         string          `json:"case_type"`
	SLADays          int             `json:"sla_days"`
	Context          json.RawMessage `json:"context,omitempty"`
	SourceDecisionID string          `json:"source_decision_id,omitempty"`
}

// CaseAssigned records who a case is assigned to.
type CaseAssigned struct {
	CaseID   string `json:"case_id"`
	Assignee string `json:"assignee"`
}

// CaseStatusChanged records a status transition.
type CaseStatusChanged struct {
	CaseID string `json:"case_id"`
	Status string `json:"status"`
}

// CaseNoteAdded records a note added to a case (author/time come from the envelope).
type CaseNoteAdded struct {
	CaseID string `json:"case_id"`
	Text   string `json:"text"`
}

// CaseSLABreached records that a case passed its SLA deadline. It is emitted by
// the SLA sweep (an effect performed in the shell against the wall clock, then
// recorded so replay is stable). The breach time is the envelope time.
type CaseSLABreached struct {
	CaseID string `json:"case_id"`
}
