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
	TypeReviewRequested           = "cases.review_requested"
	TypeCaseTypePublished         = "cases.type_published"
	TypeQueueConfigured           = "cases.queue_configured"
	TypeReviewerConfigured        = "cases.reviewer_configured"
	TypeCaseAssigned              = "cases.assigned"
	TypeCaseRouted                = "cases.routed"
	TypeCaseStatusChanged         = "cases.status_changed"
	TypeCasePriorityChanged       = "cases.priority_changed"
	TypeCaseDispositionRecorded   = "cases.disposition_recorded"
	TypeCaseNoteAdded             = "cases.note_added"
	TypeCaseEvidenceLinked        = "cases.evidence_linked"
	TypeCaseAttachmentRegistered  = "cases.attachment_registered"
	TypeCaseAttachmentAccessed    = "cases.attachment_accessed"
	TypeCaseSavedViewUpserted     = "cases.saved_view_upserted"
	TypeCaseSavedViewDeleted      = "cases.saved_view_deleted"
	TypeCaseQASelected            = "cases.qa_selected"
	TypeCaseQAReviewed            = "cases.qa_reviewed"
	TypeCaseReviewerFeedbackAdded = "cases.reviewer_feedback_added"
	TypeCaseSLABreached           = "cases.sla_breached"
	TypeCaseSLAEscalated          = "cases.sla_escalated"
	// TypeCaseSLAReminder is emitted once by the SLA sweep when an open case enters its
	// "due soon" window (before breach), nudging an assignee toward a task before it
	// goes overdue. Like the breach event it carries only the case id; the notifications
	// projector enriches it with the assignee/label from its own case index.
	TypeCaseSLAReminder = "cases.sla_reminder"
)

// ReviewRequested opens a case for human review.
type ReviewRequested struct {
	CaseID           string          `json:"case_id"`
	CompanyName      string          `json:"company_name"`
	CaseType         string          `json:"case_type"`
	CaseTypeVersion  int             `json:"case_type_version,omitempty"`
	InitialState     string          `json:"initial_state,omitempty"`
	Priority         string          `json:"priority,omitempty"`
	Jurisdiction     string          `json:"jurisdiction,omitempty"`
	Subject          string          `json:"subject,omitempty"`
	Queue            string          `json:"queue,omitempty"`
	Deadline         string          `json:"deadline,omitempty"`
	SLADays          int             `json:"sla_days"`
	Context          json.RawMessage `json:"context,omitempty"`
	SourceDecisionID string          `json:"source_decision_id,omitempty"`
}

// CaseTypePublished records one immutable case-type definition version.
type CaseTypePublished struct {
	Key        string          `json:"key"`
	Version    int             `json:"version"`
	Definition json.RawMessage `json:"definition"`
}

// QueueConfigured replaces one queue's active routing definition.
type QueueConfigured struct {
	Key        string          `json:"key"`
	Definition json.RawMessage `json:"definition"`
}

// ReviewerConfigured replaces one reviewer's active routing profile.
type ReviewerConfigured struct {
	Actor   string          `json:"actor"`
	Profile json.RawMessage `json:"profile"`
}

// CaseAssigned records who a case is assigned to.
type CaseAssigned struct {
	CaseID   string `json:"case_id"`
	Assignee string `json:"assignee"`
}

// CaseRouted records the deterministic queue decision and optional initial
// assignee. It is claimed once per case across scheduler replicas.
type CaseRouted struct {
	CaseID      string `json:"case_id"`
	Queue       string `json:"queue"`
	Assignee    string `json:"assignee,omitempty"`
	Explanation string `json:"explanation"`
}

// CaseStatusChanged records a status transition.
type CaseStatusChanged struct {
	CaseID string `json:"case_id"`
	Status string `json:"status"`
}

// CasePriorityChanged records an operator-controlled priority transition.
type CasePriorityChanged struct {
	CaseID   string `json:"case_id"`
	Priority string `json:"priority"`
}

// CaseDispositionRecorded records the reviewer's reasoned terminal or
// non-terminal outcome under the pinned case-type version.
type CaseDispositionRecorded struct {
	CaseID      string `json:"case_id"`
	Disposition string `json:"disposition"`
	ReasonCode  string `json:"reason_code"`
	Note        string `json:"note,omitempty"`
	State       string `json:"state"`
	Override    bool   `json:"override,omitempty"`
}

// CaseNoteAdded records a note added to a case (author/time come from the envelope).
type CaseNoteAdded struct {
	CaseID string `json:"case_id"`
	Text   string `json:"text"`
}

// CaseEvidenceLinked records a typed immutable relationship to source evidence.
type CaseEvidenceLinked struct {
	CaseID      string `json:"case_id"`
	EvidenceID  string `json:"evidence_id"`
	Requirement string `json:"requirement,omitempty"`
	Kind        string `json:"kind"`
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Label       string `json:"label"`
	ContentHash string `json:"content_hash,omitempty"`
}

// CaseAttachmentRegistered records immutable metadata for bytes held by an
// operator-approved artifact store. Intraktible does not silently store bytes in
// the event log.
type CaseAttachmentRegistered struct {
	CaseID       string `json:"case_id"`
	AttachmentID string `json:"attachment_id"`
	Name         string `json:"name"`
	MediaType    string `json:"media_type"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	StorageRef   string `json:"storage_ref"`
	Requirement  string `json:"requirement,omitempty"`
	Subject      string `json:"subject,omitempty"`
	LawfulBasis  string `json:"lawful_basis,omitempty"`
	RetainUntil  string `json:"retain_until,omitempty"`
	LegalHold    bool   `json:"legal_hold,omitempty"`
}

// CaseAttachmentAccessed audits a metadata/download boundary access.
type CaseAttachmentAccessed struct {
	CaseID       string `json:"case_id"`
	AttachmentID string `json:"attachment_id"`
	Purpose      string `json:"purpose"`
}

// CaseSavedViewUpserted stores a user's named filter/sort contract.
type CaseSavedViewUpserted struct {
	ViewID string          `json:"view_id"`
	Name   string          `json:"name"`
	Owner  string          `json:"owner"`
	Query  json.RawMessage `json:"query"`
}

// CaseSavedViewDeleted removes a saved view owned by the actor.
type CaseSavedViewDeleted struct {
	ViewID string `json:"view_id"`
}

// CaseQASelected records deterministic inclusion in a QA sample.
type CaseQASelected struct {
	CaseID       string `json:"case_id"`
	SampleID     string `json:"sample_id"`
	PrimaryActor string `json:"primary_actor"`
	Reviewer     string `json:"reviewer"`
}

// CaseQAReviewed records the independent second review.
type CaseQAReviewed struct {
	CaseID      string `json:"case_id"`
	SampleID    string `json:"sample_id"`
	Disposition string `json:"disposition"`
	ReasonCode  string `json:"reason_code"`
	Agreement   bool   `json:"agreement"`
	Override    bool   `json:"override,omitempty"`
	Note        string `json:"note,omitempty"`
}

// CaseReviewerFeedbackAdded records feedback after a QA review.
type CaseReviewerFeedbackAdded struct {
	CaseID   string `json:"case_id"`
	SampleID string `json:"sample_id"`
	Reviewer string `json:"reviewer"`
	Text     string `json:"text"`
}

// CaseSLABreached records that a case passed its SLA deadline. It is emitted by
// the SLA sweep (an effect performed in the shell against the wall clock, then
// recorded so replay is stable). The breach time is the envelope time.
type CaseSLABreached struct {
	CaseID string `json:"case_id"`
}

// SLAEscalationStatus is the durable outcome of processing an overdue case's
// external escalation. Pending is set by the breach projection; only terminal
// values are recorded in CaseSLAEscalated events.
type SLAEscalationStatus string

const (
	SLAEscalationPending          SLAEscalationStatus = "pending"
	SLAEscalationDelivered        SLAEscalationStatus = "delivered"
	SLAEscalationNoChannel        SLAEscalationStatus = "no_channel"
	SLAEscalationPermanentFailure SLAEscalationStatus = "permanent_failure"
)

// Terminal reports whether a status completes delivery processing.
func (s SLAEscalationStatus) Terminal() bool {
	return s == SLAEscalationDelivered || s == SLAEscalationNoChannel || s == SLAEscalationPermanentFailure
}

// CaseSLAEscalated records the terminal delivery outcome. Retryable delivery
// failures intentionally emit no event, leaving the case pending for the next tick.
type CaseSLAEscalated struct {
	CaseID string              `json:"case_id"`
	Status SLAEscalationStatus `json:"status"`
}

// CaseSLAReminder records that an open case entered its "due soon" window (emitted
// once by the SLA sweep, before any breach). The reminder time is the envelope time.
type CaseSLAReminder struct {
	CaseID string `json:"case_id"`
}
