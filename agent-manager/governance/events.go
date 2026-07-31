// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	engine "github.com/e6qu/intraktible/decision-engine/domain"
)

const Stream = "agents.governance"

const (
	TypeTemplateRegistered       = "agents.template_registered"
	TypeReleaseCreated           = "agents.release_created"
	TypeSuitePublished           = "agents.suite_published"
	TypeCampaignRecorded         = "agents.campaign_recorded"
	TypeCampaignTrialAdjudicated = "agents.campaign_trial_adjudicated"
	TypeReleaseReviewRequested   = "agents.release_review_requested"
	TypeReleaseReviewed          = "agents.release_reviewed"
	TypeReleaseReviewExpired     = "agents.release_review_expired"
	TypeReleaseRetired           = "agents.release_retired"
	TypeDeploymentRequested      = "agents.deployment_requested"
	TypeDeploymentActivated      = "agents.deployment_activated"
	TypeDeploymentPaused         = "agents.deployment_paused"
	TypeDeploymentResumed        = "agents.deployment_resumed"
	TypeDeploymentRolledBack     = "agents.deployment_rolled_back"
	TypeAssistRequested          = "agents.assist_requested"
	TypeAssistClaimed            = "agents.assist_claimed"
	TypeAssistHeartbeat          = "agents.assist_heartbeat"
	TypeAssistCompleted          = "agents.assist_completed"
	TypeAssistFailed             = "agents.assist_failed"
	TypeAssistDeadLettered       = "agents.assist_dead_lettered"
	TypeAssistRetryRequested     = "agents.assist_retry_requested"
	TypeAssistCancelRequested    = "agents.assist_cancel_requested"
	TypeAssistReviewerActed      = "agents.assist_reviewer_acted"
	TypeToolApprovalRequested    = "agents.tool_approval_requested"
	TypeToolApprovalDecided      = "agents.tool_approval_decided"
	TypeToolApprovalExpired      = "agents.tool_approval_expired"
	TypeSafetyIncidentOpened     = "agents.safety_incident_opened"
	TypeSafetyIncidentResolved   = "agents.safety_incident_resolved"
)

type TemplateRegistered struct {
	Template Template `json:"template"`
}

type ReleaseCreated struct {
	TemplateID string      `json:"template_id"`
	Release    int         `json:"release"`
	Spec       ReleaseSpec `json:"spec"`
	SpecHash   string      `json:"spec_hash"`
	CreatedBy  string      `json:"created_by"`
	CreatedAt  time.Time   `json:"created_at"`
}

type SuitePublished struct {
	Suite       EvalSuite `json:"suite"`
	PublishedBy string    `json:"published_by"`
	PublishedAt time.Time `json:"published_at"`
}

type CampaignRecorded struct {
	Result     CampaignResult `json:"result"`
	RecordedBy string         `json:"recorded_by"`
	RecordedAt time.Time      `json:"recorded_at"`
}

type CampaignTrialAdjudicated struct {
	Adjudication       TrialAdjudication  `json:"adjudication"`
	TemplateID         string             `json:"template_id"`
	Release            int                `json:"release"`
	PreviousAssessment CampaignAssessment `json:"previous_assessment"`
	Assessment         CampaignAssessment `json:"assessment"`
}

type ReleaseReviewRequestedEvent struct {
	RequestID   string    `json:"request_id"`
	TemplateID  string    `json:"template_id"`
	Release     int       `json:"release"`
	CampaignIDs []string  `json:"campaign_ids"`
	EvidenceIDs []string  `json:"evidence_ids"`
	Reviewers   []string  `json:"reviewers,omitempty"`
	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type ReleaseReviewed struct {
	RequestID  string         `json:"request_id"`
	TemplateID string         `json:"template_id"`
	Release    int            `json:"release"`
	Decision   ReviewDecision `json:"decision"`
	Reason     string         `json:"reason"`
	ReviewedBy string         `json:"reviewed_by"`
	ReviewedAt time.Time      `json:"reviewed_at"`
}

type ReleaseReviewExpired struct {
	RequestID  string    `json:"request_id"`
	TemplateID string    `json:"template_id"`
	Release    int       `json:"release"`
	ExpiredAt  time.Time `json:"expired_at"`
}

type ReleaseRetiredEvent struct {
	TemplateID string    `json:"template_id"`
	Release    int       `json:"release"`
	Reason     string    `json:"reason"`
	RetiredAt  time.Time `json:"retired_at"`
}

type DeploymentRequestedEvent struct {
	Request     DeploymentRequest `json:"request"`
	RequestedBy string            `json:"requested_by"`
	RequestedAt time.Time         `json:"requested_at"`
}

type DeploymentActivatedEvent struct {
	DeploymentID string             `json:"deployment_id"`
	TemplateID   string             `json:"template_id"`
	Release      int                `json:"release"`
	Environment  engine.Environment `json:"environment"`
	ActivatedBy  string             `json:"activated_by"`
	ActivatedAt  time.Time          `json:"activated_at"`
}

type DeploymentPausedEvent struct {
	DeploymentID string    `json:"deployment_id"`
	Reason       string    `json:"reason"`
	PausedBy     string    `json:"paused_by"`
	PausedAt     time.Time `json:"paused_at"`
}

type DeploymentResumedEvent struct {
	DeploymentID string    `json:"deployment_id"`
	Reason       string    `json:"reason"`
	ResumedBy    string    `json:"resumed_by"`
	ResumedAt    time.Time `json:"resumed_at"`
}

type DeploymentRolledBackEvent struct {
	DeploymentID string    `json:"deployment_id"`
	FromRelease  int       `json:"from_release"`
	ToRelease    int       `json:"to_release"`
	Reason       string    `json:"reason"`
	RolledBackBy string    `json:"rolled_back_by"`
	RolledBackAt time.Time `json:"rolled_back_at"`
}

type AssistRequested struct {
	AssistID        string              `json:"assist_id"`
	CaseID          string              `json:"case_id"`
	Kind            AssistKind          `json:"kind"`
	TemplateID      string              `json:"template_id"`
	Release         int                 `json:"release"`
	Environment     engine.Environment  `json:"environment"`
	EvidenceIDs     []string            `json:"evidence_ids"`
	EvidenceSeq     uint64              `json:"evidence_seq"`
	RequestedBy     string              `json:"requested_by"`
	RequestedAt     time.Time           `json:"requested_at"`
	IdempotencyHash string              `json:"idempotency_hash,omitempty"`
	RequestHash     string              `json:"request_hash,omitempty"`
	Subject         string              `json:"subject,omitempty"`
	InvocationID    string              `json:"invocation_id,omitempty"`
	InputSubject    string              `json:"input_subject,omitempty"`
	SealedInput     []byte              `json:"sealed_input,omitempty"`
	PolicySource    *AssistPolicySource `json:"policy_source,omitempty"`
}

type AssistClaimed struct {
	AssistID   string    `json:"assist_id"`
	Owner      string    `json:"owner"`
	Attempt    int       `json:"attempt"`
	LeaseUntil time.Time `json:"lease_until"`
}

type AssistHeartbeat struct {
	AssistID   string    `json:"assist_id"`
	Owner      string    `json:"owner"`
	Attempt    int       `json:"attempt"`
	LeaseUntil time.Time `json:"lease_until"`
}

type AssistCompleted struct {
	Result         AssistResult `json:"result"`
	ContentSubject string       `json:"content_subject,omitempty"`
	SealedContent  []byte       `json:"sealed_content,omitempty"`
	CompletedAt    time.Time    `json:"completed_at"`
	ClaimOwner     string       `json:"claim_owner,omitempty"`
	ClaimAttempt   int          `json:"claim_attempt,omitempty"`
}

type AssistFailed struct {
	AssistID     string    `json:"assist_id"`
	Reason       string    `json:"reason"`
	FailedAt     time.Time `json:"failed_at"`
	ClaimOwner   string    `json:"claim_owner,omitempty"`
	ClaimAttempt int       `json:"claim_attempt,omitempty"`
}

type AssistDeadLettered struct {
	AssistID string    `json:"assist_id"`
	Attempt  int       `json:"attempt"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
}

type AssistRetryRequested struct {
	AssistID               string    `json:"assist_id"`
	Reason                 string    `json:"reason"`
	RequestedBy            string    `json:"requested_by"`
	AcknowledgeAtLeastOnce bool      `json:"acknowledge_at_least_once"`
	At                     time.Time `json:"at"`
}

type AssistCancelRequested struct {
	AssistID    string    `json:"assist_id"`
	Reason      string    `json:"reason"`
	RequestedBy string    `json:"requested_by"`
	At          time.Time `json:"at"`
}

type AssistReviewerActed struct {
	Action         ReviewerAction         `json:"action"`
	Actor          string                 `json:"actor"`
	ActedAt        time.Time              `json:"acted_at"`
	SuggestionHash string                 `json:"suggestion_hash"`
	FinalHash      string                 `json:"final_hash,omitempty"`
	Differences    []SuggestionDifference `json:"differences,omitempty"`
	ContentSubject string                 `json:"content_subject,omitempty"`
	SealedFinal    []byte                 `json:"sealed_final,omitempty"`
}

func (event AssistReviewerActed) Validate(request AssistRequested) error {
	if strings.TrimSpace(event.Actor) == "" || event.ActedAt.IsZero() ||
		event.Action.AssistID != request.AssistID ||
		!event.Action.Action.Valid() ||
		event.Action.EvidenceHeadSeq < request.EvidenceSeq ||
		event.Action.EvidenceStale !=
			(event.Action.EvidenceHeadSeq > request.EvidenceSeq) ||
		!validSHA256(event.SuggestionHash) {
		return errors.New("agent governance: reviewer action lineage is incomplete")
	}
	if err := rejectTextPII("reviewer feedback", event.Action.Reason); err != nil {
		return err
	}
	if (event.Action.Action == AssistRejected ||
		event.Action.Action == AssistEscalated) &&
		strings.TrimSpace(event.Action.Reason) == "" {
		return errors.New(
			"agent governance: rejection or escalation requires a reason",
		)
	}
	if event.Action.TimeSavedMS < 0 {
		return errors.New("agent governance: time_saved_ms cannot be negative")
	}
	for index, difference := range event.Differences {
		if err := difference.Validate(); err != nil {
			return err
		}
		if index > 0 {
			previous := event.Differences[index-1]
			if previous.Path > difference.Path ||
				(previous.Path == difference.Path &&
					previous.Kind >= difference.Kind) {
				return errors.New(
					"agent governance: suggestion differences are not unique and ordered",
				)
			}
		}
	}
	switch event.Action.Action {
	case AssistAccepted:
		if len(event.Action.Final) > 0 || event.FinalHash != event.SuggestionHash ||
			len(event.Differences) > 0 || event.ContentSubject != "" ||
			len(event.SealedFinal) > 0 {
			return errors.New(
				"agent governance: accepted assist has invalid final-result evidence",
			)
		}
	case AssistEdited:
		if !validSHA256(event.FinalHash) {
			return errors.New(
				"agent governance: edited assist has no final-result hash",
			)
		}
		switch {
		case len(event.SealedFinal) > 0:
			if event.ContentSubject == "" ||
				event.ContentSubject != request.Subject ||
				len(event.Action.Final) > 0 {
				return errors.New(
					"agent governance: sealed reviewer final has invalid subject lineage",
				)
			}
		default:
			if event.ContentSubject != "" {
				return errors.New(
					"agent governance: reviewer final subject has no sealed content",
				)
			}
			if err := validateJSONObject(
				"edited assist result", event.Action.Final,
			); err != nil {
				return err
			}
			if err := rejectJSONPII(
				"reviewer-edited assist", event.Action.Final,
			); err != nil {
				return err
			}
			hash, err := hashJSON(event.Action.Final)
			if err != nil {
				return err
			}
			if hash != event.FinalHash {
				return errors.New(
					"agent governance: edited assist final hash does not match content",
				)
			}
		}
	case AssistRejected, AssistEscalated:
		if len(event.Action.Final) > 0 || event.FinalHash != "" ||
			len(event.Differences) > 0 || event.ContentSubject != "" ||
			len(event.SealedFinal) > 0 {
			return fmt.Errorf(
				"agent governance: %s assist cannot record a final result",
				event.Action.Action,
			)
		}
	default:
		return errors.New("agent governance: reviewer action is invalid")
	}
	return nil
}

type ToolApprovalRequested struct {
	ApprovalID    string    `json:"approval_id"`
	AssistID      string    `json:"assist_id"`
	InvocationID  string    `json:"invocation_id"`
	CallID        string    `json:"call_id"`
	Name          string    `json:"name"`
	Purpose       string    `json:"purpose"`
	ArgumentsHash string    `json:"arguments_hash"`
	RequestedBy   string    `json:"requested_by"`
	RequestedAt   time.Time `json:"requested_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type ToolApprovalDecided struct {
	ApprovalID string             `json:"approval_id"`
	AssistID   string             `json:"assist_id"`
	Decision   ToolApprovalStatus `json:"decision"`
	Reason     string             `json:"reason"`
	DecidedBy  string             `json:"decided_by"`
	DecidedAt  time.Time          `json:"decided_at"`
}

type ToolApprovalExpired struct {
	ApprovalID string    `json:"approval_id"`
	AssistID   string    `json:"assist_id"`
	ExpiredAt  time.Time `json:"expired_at"`
}

type SafetyIncidentOpened struct {
	IncidentID   string          `json:"incident_id"`
	TemplateID   string          `json:"template_id"`
	Release      int             `json:"release"`
	DeploymentID string          `json:"deployment_id,omitempty"`
	RunID        string          `json:"run_id,omitempty"`
	Kind         string          `json:"kind"`
	Severity     Severity        `json:"severity"`
	Summary      string          `json:"summary"`
	Evidence     json.RawMessage `json:"evidence,omitempty"`
	OpenedAt     time.Time       `json:"opened_at"`
}

type SafetyIncidentResolved struct {
	IncidentID string    `json:"incident_id"`
	Resolution string    `json:"resolution"`
	ResolvedAt time.Time `json:"resolved_at"`
}
