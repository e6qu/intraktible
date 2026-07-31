// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	engine "github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	CollectionTemplates     = "agent_templates"
	CollectionReleases      = "agent_releases"
	CollectionSuites        = "agent_eval_suites"
	CollectionCampaigns     = "agent_eval_campaigns"
	CollectionDeployments   = "agent_deployments"
	CollectionAssists       = "agent_case_assists"
	CollectionToolApprovals = "agent_tool_approvals"
	CollectionIncidents     = "agent_safety_incidents"
)

type TemplateView struct {
	Org       string `json:"org"`
	Workspace string `json:"workspace"`
	Template
	LatestRelease int       `json:"latest_release"`
	RegisteredBy  string    `json:"registered_by"`
	RegisteredAt  time.Time `json:"registered_at"`
	Seq           uint64    `json:"seq"`
}

type ReviewView struct {
	RequestID   string         `json:"request_id"`
	CampaignIDs []string       `json:"campaign_ids"`
	EvidenceIDs []string       `json:"evidence_ids"`
	Reviewers   []string       `json:"reviewers,omitempty"`
	RequestedBy string         `json:"requested_by"`
	RequestedAt time.Time      `json:"requested_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
	Decision    ReviewDecision `json:"decision,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	ReviewedBy  string         `json:"reviewed_by,omitempty"`
	ReviewedAt  time.Time      `json:"reviewed_at,omitzero"`
	ExpiredAt   time.Time      `json:"expired_at,omitzero"`
}

type ReleaseView struct {
	Org         string        `json:"org"`
	Workspace   string        `json:"workspace"`
	TemplateID  string        `json:"template_id"`
	Release     int           `json:"release"`
	Status      ReleaseStatus `json:"status"`
	Spec        ReleaseSpec   `json:"spec"`
	SpecHash    string        `json:"spec_hash"`
	CampaignIDs []string      `json:"campaign_ids"`
	Review      *ReviewView   `json:"review,omitempty"`
	CreatedBy   string        `json:"created_by"`
	CreatedAt   time.Time     `json:"created_at"`
	RetiredAt   time.Time     `json:"retired_at,omitzero"`
	Seq         uint64        `json:"seq"`
}

type SuiteView struct {
	Org       string `json:"org"`
	Workspace string `json:"workspace"`
	EvalSuite
	PublishedBy string    `json:"published_by"`
	PublishedAt time.Time `json:"published_at"`
	Seq         uint64    `json:"seq"`
}

type CampaignView struct {
	Org       string `json:"org"`
	Workspace string `json:"workspace"`
	CampaignResult
	Assessment    CampaignAssessment  `json:"assessment"`
	Adjudications []TrialAdjudication `json:"adjudications,omitempty"`
	RecordedBy    string              `json:"recorded_by"`
	RecordedAt    time.Time           `json:"recorded_at"`
	Seq           uint64              `json:"seq"`
}

type DeploymentView struct {
	Org             string             `json:"org"`
	Workspace       string             `json:"workspace"`
	DeploymentID    string             `json:"deployment_id"`
	TemplateID      string             `json:"template_id"`
	Release         int                `json:"release"`
	Environment     engine.Environment `json:"environment"`
	Status          DeploymentStatus   `json:"status"`
	Reason          string             `json:"reason"`
	RequestedBy     string             `json:"requested_by"`
	RequestedAt     time.Time          `json:"requested_at"`
	ActivateAt      *time.Time         `json:"activate_at,omitempty"`
	ExpiresAt       *time.Time         `json:"expires_at,omitempty"`
	ActivatedBy     string             `json:"activated_by,omitempty"`
	ActivatedAt     time.Time          `json:"activated_at,omitzero"`
	PausedBy        string             `json:"paused_by,omitempty"`
	PausedAt        time.Time          `json:"paused_at,omitzero"`
	ResumedBy       string             `json:"resumed_by,omitempty"`
	ResumedAt       time.Time          `json:"resumed_at,omitzero"`
	PreviousRelease int                `json:"previous_release,omitempty"`
	Seq             uint64             `json:"seq"`
}

type AssistView struct {
	Org                  string                 `json:"org"`
	Workspace            string                 `json:"workspace"`
	AssistID             string                 `json:"assist_id"`
	CaseID               string                 `json:"case_id"`
	Kind                 AssistKind             `json:"kind"`
	TemplateID           string                 `json:"template_id"`
	Release              int                    `json:"release"`
	Environment          engine.Environment     `json:"environment"`
	EvidenceIDs          []string               `json:"evidence_ids"`
	EvidenceSeq          uint64                 `json:"evidence_seq"`
	CurrentEvidenceSeq   uint64                 `json:"current_evidence_seq"`
	EvidenceStale        bool                   `json:"evidence_stale,omitempty"`
	InvocationID         string                 `json:"invocation_id"`
	PolicySource         *AssistPolicySource    `json:"policy_source,omitempty"`
	Status               AssistStatus           `json:"status"`
	Result               *AssistResult          `json:"result,omitempty"`
	ContentSubject       string                 `json:"content_subject,omitempty"`
	SealedContent        []byte                 `json:"sealed_content,omitempty"`
	ContentErased        bool                   `json:"content_erased,omitempty"`
	Failure              string                 `json:"failure,omitempty"`
	Action               *ReviewerAction        `json:"action,omitempty"`
	ActionActor          string                 `json:"action_actor,omitempty"`
	SuggestionHash       string                 `json:"suggestion_hash,omitempty"`
	FinalHash            string                 `json:"final_hash,omitempty"`
	Differences          []SuggestionDifference `json:"differences,omitempty"`
	ActionContentSubject string                 `json:"action_content_subject,omitempty"`
	SealedActionFinal    []byte                 `json:"sealed_action_final,omitempty"`
	ActionFinalErased    bool                   `json:"action_final_erased,omitempty"`
	RequestedBy          string                 `json:"requested_by"`
	RequestedAt          time.Time              `json:"requested_at"`
	CompletedAt          time.Time              `json:"completed_at,omitzero"`
	WorkerOwner          string                 `json:"worker_owner,omitempty"`
	Attempt              int                    `json:"attempt,omitempty"`
	LeaseUntil           time.Time              `json:"lease_until,omitzero"`
	Seq                  uint64                 `json:"seq"`
}

type IncidentView struct {
	Org          string          `json:"org"`
	Workspace    string          `json:"workspace"`
	IncidentID   string          `json:"incident_id"`
	TemplateID   string          `json:"template_id"`
	Release      int             `json:"release"`
	DeploymentID string          `json:"deployment_id,omitempty"`
	RunID        string          `json:"run_id,omitempty"`
	Kind         string          `json:"kind"`
	Severity     Severity        `json:"severity"`
	Summary      string          `json:"summary"`
	Evidence     json.RawMessage `json:"evidence,omitempty"`
	Status       string          `json:"status"`
	Resolution   string          `json:"resolution,omitempty"`
	OpenedAt     time.Time       `json:"opened_at"`
	ResolvedAt   time.Time       `json:"resolved_at,omitzero"`
	Seq          uint64          `json:"seq"`
}

type ToolApprovalView struct {
	Org           string             `json:"org"`
	Workspace     string             `json:"workspace"`
	ApprovalID    string             `json:"approval_id"`
	AssistID      string             `json:"assist_id"`
	InvocationID  string             `json:"invocation_id"`
	CallID        string             `json:"call_id"`
	Name          string             `json:"name"`
	Purpose       string             `json:"purpose"`
	ArgumentsHash string             `json:"arguments_hash"`
	Status        ToolApprovalStatus `json:"status"`
	RequestedBy   string             `json:"requested_by"`
	RequestedAt   time.Time          `json:"requested_at"`
	ExpiresAt     time.Time          `json:"expires_at"`
	DecidedBy     string             `json:"decided_by,omitempty"`
	DecidedAt     time.Time          `json:"decided_at,omitzero"`
	Reason        string             `json:"reason,omitempty"`
	Seq           uint64             `json:"seq"`
}

type Projector struct{}

func (Projector) Name() string { return "agent_governance" }
func (Projector) Collections() []string {
	return []string{
		CollectionTemplates, CollectionReleases, CollectionSuites, CollectionCampaigns,
		CollectionDeployments, CollectionAssists, CollectionToolApprovals, CollectionIncidents,
	}
}

func (Projector) Apply(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	switch event.Type {
	case TypeTemplateRegistered:
		return applyTemplateRegistered(ctx, event, st)
	case TypeReleaseCreated:
		return applyReleaseCreated(ctx, event, st)
	case TypeSuitePublished:
		return applySuitePublished(ctx, event, st)
	case TypeCampaignRecorded:
		return applyCampaignRecorded(ctx, event, st)
	case TypeCampaignTrialAdjudicated:
		return applyCampaignTrialAdjudicated(ctx, event, st)
	case TypeReleaseReviewRequested:
		return applyReviewRequested(ctx, event, st)
	case TypeReleaseReviewed:
		return applyReviewed(ctx, event, st)
	case TypeReleaseReviewExpired:
		return applyReviewExpired(ctx, event, st)
	case TypeReleaseRetired:
		return applyReleaseRetired(ctx, event, st)
	case TypeDeploymentRequested:
		return applyDeploymentRequested(ctx, event, st)
	case TypeDeploymentActivated:
		return applyDeploymentActivated(ctx, event, st)
	case TypeDeploymentPaused:
		return applyDeploymentPaused(ctx, event, st)
	case TypeDeploymentResumed:
		return applyDeploymentResumed(ctx, event, st)
	case TypeDeploymentRolledBack:
		return applyDeploymentRolledBack(ctx, event, st)
	case TypeAssistRequested:
		return applyAssistRequested(ctx, event, st)
	case TypeAssistClaimed:
		return applyAssistClaimed(ctx, event, st)
	case TypeAssistHeartbeat:
		return applyAssistHeartbeat(ctx, event, st)
	case TypeAssistCompleted:
		return applyAssistCompleted(ctx, event, st)
	case TypeAssistFailed:
		return applyAssistFailed(ctx, event, st)
	case TypeAssistDeadLettered:
		return applyAssistDeadLettered(ctx, event, st)
	case TypeAssistRetryRequested:
		return applyAssistRetryRequested(ctx, event, st)
	case TypeAssistCancelRequested:
		return applyAssistCancelRequested(ctx, event, st)
	case TypeAssistReviewerActed:
		return applyAssistActed(ctx, event, st)
	case TypeToolApprovalRequested:
		return applyToolApprovalRequested(ctx, event, st)
	case TypeToolApprovalDecided:
		return applyToolApprovalDecided(ctx, event, st)
	case TypeToolApprovalExpired:
		return applyToolApprovalExpired(ctx, event, st)
	case TypeSafetyIncidentOpened:
		return applyIncidentOpened(ctx, event, st)
	case TypeSafetyIncidentResolved:
		return applyIncidentResolved(ctx, event, st)
	default:
		return nil
	}
}

func applyTemplateRegistered(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload TemplateRegistered
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	if err := payload.Template.Validate(); err != nil {
		return fmt.Errorf("agent governance: invalid template at seq %d: %w", event.Seq, err)
	}
	key := store.Key(event.Org, event.Workspace, payload.Template.TemplateID)
	if _, exists, err := store.GetDoc[TemplateView](ctx, st, CollectionTemplates, key); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("agent governance: duplicate template %q at seq %d", payload.Template.TemplateID, event.Seq)
	}
	return store.PutDoc(ctx, st, CollectionTemplates, key, TemplateView{
		Org: event.Org, Workspace: event.Workspace, Template: payload.Template,
		RegisteredBy: event.Actor, RegisteredAt: event.Time, Seq: event.Seq,
	})
}

func applyReleaseCreated(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload ReleaseCreated
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	if payload.Release < 1 || payload.SpecHash == "" {
		return fmt.Errorf("agent governance: incomplete release at seq %d", event.Seq)
	}
	if err := payload.Spec.Validate(); err != nil {
		return fmt.Errorf("agent governance: invalid release at seq %d: %w", event.Seq, err)
	}
	specHash, err := hashJSON(payload.Spec)
	if err != nil {
		return err
	}
	if specHash != payload.SpecHash {
		return fmt.Errorf("agent governance: release spec hash mismatch at seq %d", event.Seq)
	}
	templateKey := store.Key(event.Org, event.Workspace, payload.TemplateID)
	template, found, err := store.GetDoc[TemplateView](ctx, st, CollectionTemplates, templateKey)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("agent governance: release at seq %d references unknown template %q", event.Seq, payload.TemplateID)
	}
	if payload.Release != template.LatestRelease+1 {
		return fmt.Errorf("agent governance: release %d is not next after %d", payload.Release, template.LatestRelease)
	}
	if template.HighImpact && !payload.Spec.RequireHumanGate {
		return fmt.Errorf("agent governance: high-impact release at seq %d lacks human gate", event.Seq)
	}
	release := ReleaseView{
		Org: event.Org, Workspace: event.Workspace, TemplateID: payload.TemplateID,
		Release: payload.Release, Status: ReleaseDraft, Spec: payload.Spec, SpecHash: payload.SpecHash,
		CampaignIDs: []string{}, CreatedBy: payload.CreatedBy, CreatedAt: payload.CreatedAt, Seq: event.Seq,
	}
	if err := store.PutDoc(ctx, st, CollectionReleases, releaseKey(event.Org, event.Workspace, payload.TemplateID, payload.Release), release); err != nil {
		return err
	}
	template.LatestRelease, template.Seq = payload.Release, event.Seq
	return store.PutDoc(ctx, st, CollectionTemplates, templateKey, template)
}

func applySuitePublished(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload SuitePublished
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	if err := payload.Suite.Validate(); err != nil {
		return fmt.Errorf("agent governance: invalid suite at seq %d: %w", event.Seq, err)
	}
	key := suiteKey(event.Org, event.Workspace, payload.Suite.SuiteID, payload.Suite.Version)
	if _, exists, err := store.GetDoc[SuiteView](ctx, st, CollectionSuites, key); err != nil {
		return err
	} else if exists {
		return fmt.Errorf(
			"agent governance: duplicate suite %s@%d at seq %d",
			payload.Suite.SuiteID, payload.Suite.Version, event.Seq,
		)
	}
	return store.PutDoc(ctx, st, CollectionSuites, key, SuiteView{
		Org: event.Org, Workspace: event.Workspace, EvalSuite: payload.Suite,
		PublishedBy: payload.PublishedBy, PublishedAt: payload.PublishedAt, Seq: event.Seq,
	})
}

func applyCampaignRecorded(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload CampaignRecorded
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	if err := payload.Result.Validate(); err != nil {
		return fmt.Errorf("agent governance: invalid campaign at seq %d: %w", event.Seq, err)
	}
	suite, found, err := store.GetDoc[SuiteView](
		ctx, st, CollectionSuites,
		suiteKey(
			event.Org, event.Workspace, payload.Result.SuiteID, payload.Result.SuiteVersion,
		),
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("agent governance: campaign at seq %d references unknown suite", event.Seq)
	}
	derived, err := BuildCampaign(
		payload.Result.CampaignID, payload.Result.TemplateID, payload.Result.Release,
		suite.EvalSuite, payload.Result.Trials,
	)
	if err != nil {
		return fmt.Errorf("agent governance: derive campaign at seq %d: %w", event.Seq, err)
	}
	expected, _ := json.Marshal(derived)
	actual, _ := json.Marshal(payload.Result)
	if !bytes.Equal(expected, actual) {
		return fmt.Errorf(
			"agent governance: campaign aggregates at seq %d do not match trial evidence",
			event.Seq,
		)
	}
	release, err := requireRelease(ctx, st, event, payload.Result.TemplateID, payload.Result.Release)
	if err != nil {
		return err
	}
	if release.Status != ReleaseDraft && release.Status != ReleaseEvaluated {
		return fmt.Errorf("agent governance: campaign at seq %d cannot change %s release", event.Seq, release.Status)
	}
	for _, id := range release.CampaignIDs {
		if id == payload.Result.CampaignID {
			return fmt.Errorf("agent governance: duplicate campaign %q", id)
		}
	}
	release.CampaignIDs = append(release.CampaignIDs, payload.Result.CampaignID)
	sort.Strings(release.CampaignIDs)
	release.Status, release.Seq = ReleaseEvaluated, event.Seq
	if err := store.PutDoc(ctx, st, CollectionReleases, releaseKey(event.Org, event.Workspace, release.TemplateID, release.Release), release); err != nil {
		return err
	}
	return store.PutDoc(ctx, st, CollectionCampaigns, store.Key(event.Org, event.Workspace, payload.Result.CampaignID), CampaignView{
		Org: event.Org, Workspace: event.Workspace, CampaignResult: payload.Result,
		Assessment: CampaignAssessment{
			Total: payload.Result.Total, Passed: payload.Result.Passed,
			PassRate: payload.Result.PassRate, Variance: payload.Result.Variance,
			ConfidenceLow:  payload.Result.ConfidenceLow,
			ConfidenceHigh: payload.Result.ConfidenceHigh,
			Blocking:       payload.Result.Blocking,
		},
		RecordedBy: payload.RecordedBy, RecordedAt: payload.RecordedAt, Seq: event.Seq,
	})
}

func applyCampaignTrialAdjudicated(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload CampaignTrialAdjudicated
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	if err := payload.Adjudication.Validate(); err != nil {
		return fmt.Errorf(
			"agent governance: invalid trial adjudication at seq %d: %w", event.Seq, err,
		)
	}
	key := store.Key(event.Org, event.Workspace, payload.Adjudication.CampaignID)
	campaign, found, err := store.GetDoc[CampaignView](
		ctx, st, CollectionCampaigns, key,
	)
	if err != nil {
		return err
	}
	if !found || campaign.RecordedBy == payload.Adjudication.AdjudicatedBy {
		return fmt.Errorf(
			"agent governance: trial adjudication at seq %d violates campaign independence",
			event.Seq,
		)
	}
	if payload.TemplateID != campaign.TemplateID ||
		payload.Release != campaign.Release {
		return fmt.Errorf(
			"agent governance: trial adjudication at seq %d has invalid release lineage",
			event.Seq,
		)
	}
	if campaign.Assessment != payload.PreviousAssessment {
		return fmt.Errorf(
			"agent governance: trial adjudication at seq %d has stale assessment",
			event.Seq,
		)
	}
	release, err := requireRelease(
		ctx, st, event, campaign.TemplateID, campaign.Release,
	)
	if err != nil {
		return err
	}
	if release.Status != ReleaseEvaluated {
		return fmt.Errorf(
			"agent governance: trial adjudication at seq %d targets %s release",
			event.Seq, release.Status,
		)
	}
	for _, prior := range campaign.Adjudications {
		if prior.CaseID == payload.Adjudication.CaseID &&
			prior.Trial == payload.Adjudication.Trial {
			return fmt.Errorf(
				"agent governance: trial adjudication at seq %d duplicates %s",
				event.Seq,
				trialRef(payload.Adjudication.CaseID, payload.Adjudication.Trial),
			)
		}
	}
	suite, found, err := store.GetDoc[SuiteView](
		ctx, st, CollectionSuites,
		suiteKey(
			event.Org, event.Workspace, campaign.SuiteID, campaign.SuiteVersion,
		),
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"agent governance: trial adjudication at seq %d has no suite", event.Seq,
		)
	}
	campaign.Adjudications = append(
		campaign.Adjudications, payload.Adjudication,
	)
	campaign.Assessment, err = AssessCampaign(
		campaign.CampaignResult, suite.EvalSuite, campaign.Adjudications,
	)
	if err != nil {
		return fmt.Errorf(
			"agent governance: assess adjudication at seq %d: %w", event.Seq, err,
		)
	}
	if campaign.Assessment != payload.Assessment {
		return fmt.Errorf(
			"agent governance: trial adjudication at seq %d assessment is not reproducible",
			event.Seq,
		)
	}
	campaign.Seq = event.Seq
	return store.PutDoc(ctx, st, CollectionCampaigns, key, campaign)
}

func applyReviewRequested(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload ReleaseReviewRequestedEvent
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	release, err := requireRelease(ctx, st, event, payload.TemplateID, payload.Release)
	if err != nil {
		return err
	}
	if release.Status != ReleaseEvaluated {
		return fmt.Errorf("agent governance: review request at seq %d requires evaluated release", event.Seq)
	}
	if payload.RequestID == "" || payload.RequestedBy == "" ||
		!payload.ExpiresAt.After(payload.RequestedAt) || len(payload.CampaignIDs) == 0 {
		return fmt.Errorf("agent governance: invalid review request at seq %d", event.Seq)
	}
	template, found, err := store.GetDoc[TemplateView](
		ctx, st, CollectionTemplates, store.Key(event.Org, event.Workspace, payload.TemplateID),
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("agent governance: review at seq %d references unknown template", event.Seq)
	}
	requiredSeen, adversarialSeen := false, false
	for _, campaignID := range payload.CampaignIDs {
		campaign, found, err := store.GetDoc[CampaignView](
			ctx, st, CollectionCampaigns, store.Key(event.Org, event.Workspace, campaignID),
		)
		if err != nil {
			return err
		}
		if !found || campaign.TemplateID != payload.TemplateID ||
			campaign.Release != payload.Release || campaign.Assessment.Blocking {
			return fmt.Errorf(
				"agent governance: review at seq %d has invalid campaign %q",
				event.Seq, campaignID,
			)
		}
		suite, found, err := store.GetDoc[SuiteView](
			ctx, st, CollectionSuites,
			suiteKey(event.Org, event.Workspace, campaign.SuiteID, campaign.SuiteVersion),
		)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf(
				"agent governance: review at seq %d has missing campaign suite", event.Seq,
			)
		}
		requiredSeen = requiredSeen || suite.Required
		adversarialSeen = adversarialSeen || suite.Adversarial
	}
	if !requiredSeen || (template.HighImpact && !adversarialSeen) {
		return fmt.Errorf("agent governance: review at seq %d does not satisfy evaluation gates", event.Seq)
	}
	release.Status = ReleaseReviewRequested
	release.Review = &ReviewView{
		RequestID: payload.RequestID, CampaignIDs: payload.CampaignIDs, EvidenceIDs: payload.EvidenceIDs,
		Reviewers: payload.Reviewers, RequestedBy: payload.RequestedBy,
		RequestedAt: payload.RequestedAt, ExpiresAt: payload.ExpiresAt,
	}
	release.Seq = event.Seq
	return store.PutDoc(ctx, st, CollectionReleases, releaseKey(event.Org, event.Workspace, release.TemplateID, release.Release), release)
}

func applyReviewed(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload ReleaseReviewed
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	release, err := requireRelease(ctx, st, event, payload.TemplateID, payload.Release)
	if err != nil {
		return err
	}
	if release.Status != ReleaseReviewRequested || release.Review == nil || release.Review.RequestID != payload.RequestID {
		return fmt.Errorf("agent governance: review at seq %d does not match an open request", event.Seq)
	}
	if !payload.Decision.Valid() {
		return fmt.Errorf("agent governance: review at seq %d has invalid decision %q", event.Seq, payload.Decision)
	}
	if payload.Reason == "" || payload.ReviewedBy == "" {
		return fmt.Errorf("agent governance: review at seq %d lacks reason or actor", event.Seq)
	}
	if payload.Decision == ReviewApprove {
		if payload.ReviewedBy == release.CreatedBy ||
			payload.ReviewedBy == release.Review.RequestedBy {
			return fmt.Errorf("agent governance: review at seq %d violates four-eyes", event.Seq)
		}
		if len(release.Review.Reviewers) > 0 &&
			!contains(release.Review.Reviewers, payload.ReviewedBy) {
			return fmt.Errorf("agent governance: review at seq %d actor was not assigned", event.Seq)
		}
		if !payload.ReviewedAt.Before(release.Review.ExpiresAt) {
			return fmt.Errorf("agent governance: review at seq %d occurred after expiry", event.Seq)
		}
	}
	release.Review.Decision, release.Review.Reason = payload.Decision, payload.Reason
	release.Review.ReviewedBy, release.Review.ReviewedAt = payload.ReviewedBy, payload.ReviewedAt
	if payload.Decision == ReviewApprove {
		release.Status = ReleaseApproved
	} else {
		release.Status = ReleaseRejected
	}
	release.Seq = event.Seq
	return store.PutDoc(ctx, st, CollectionReleases, releaseKey(event.Org, event.Workspace, release.TemplateID, release.Release), release)
}

func applyReviewExpired(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload ReleaseReviewExpired
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	release, err := requireRelease(ctx, st, event, payload.TemplateID, payload.Release)
	if err != nil {
		return err
	}
	if release.Status != ReleaseReviewRequested || release.Review == nil ||
		release.Review.RequestID != payload.RequestID ||
		payload.ExpiredAt.Before(release.Review.ExpiresAt) {
		return fmt.Errorf(
			"agent governance: review expiry at seq %d has stale lineage", event.Seq,
		)
	}
	release.Status, release.Review.ExpiredAt, release.Seq =
		ReleaseEvaluated, payload.ExpiredAt, event.Seq
	return store.PutDoc(
		ctx, st, CollectionReleases,
		releaseKey(event.Org, event.Workspace, release.TemplateID, release.Release),
		release,
	)
}

func applyReleaseRetired(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload ReleaseRetiredEvent
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	release, err := requireRelease(ctx, st, event, payload.TemplateID, payload.Release)
	if err != nil {
		return err
	}
	if release.Status == ReleaseRetired {
		return fmt.Errorf("agent governance: release %s@%d already retired", payload.TemplateID, payload.Release)
	}
	deployments, err := ListAllDeployments(ctx, st)
	if err != nil {
		return err
	}
	for _, deployment := range deployments {
		if deployment.Org == event.Org && deployment.Workspace == event.Workspace &&
			deployment.TemplateID == payload.TemplateID && deployment.Release == payload.Release &&
			(deployment.Status == DeploymentScheduled || deployment.Status == DeploymentActive) {
			return fmt.Errorf(
				"agent governance: release retirement at seq %d has live deployment %q",
				event.Seq, deployment.DeploymentID,
			)
		}
	}
	release.Status, release.RetiredAt, release.Seq = ReleaseRetired, payload.RetiredAt, event.Seq
	return store.PutDoc(ctx, st, CollectionReleases, releaseKey(event.Org, event.Workspace, release.TemplateID, release.Release), release)
}

func applyDeploymentRequested(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload DeploymentRequestedEvent
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	if err := payload.Request.Validate(); err != nil {
		return fmt.Errorf("agent governance: invalid deployment request at seq %d: %w", event.Seq, err)
	}
	release, err := requireRelease(
		ctx, st, event, payload.Request.TemplateID, payload.Request.Release,
	)
	if err != nil {
		return err
	}
	if release.Status != ReleaseApproved {
		return fmt.Errorf("agent governance: deployment at seq %d requires approved release", event.Seq)
	}
	key := store.Key(event.Org, event.Workspace, payload.Request.DeploymentID)
	if _, exists, err := store.GetDoc[DeploymentView](
		ctx, st, CollectionDeployments, key,
	); err != nil {
		return err
	} else if exists {
		return fmt.Errorf(
			"agent governance: duplicate deployment %q at seq %d",
			payload.Request.DeploymentID, event.Seq,
		)
	}
	deployments, err := ListDeployments(
		ctx, st, identity.Identity{Org: event.Org, Workspace: event.Workspace},
	)
	if err != nil {
		return err
	}
	for _, deployment := range deployments {
		if deployment.TemplateID == payload.Request.TemplateID &&
			deployment.Environment == payload.Request.Environment &&
			(deployment.Status == DeploymentScheduled || deployment.Status == DeploymentActive) {
			return fmt.Errorf(
				"agent governance: environment already bound by deployment %q at seq %d",
				deployment.DeploymentID, event.Seq,
			)
		}
	}
	status := DeploymentScheduled
	return store.PutDoc(ctx, st, CollectionDeployments, key, DeploymentView{
		Org: event.Org, Workspace: event.Workspace, DeploymentID: payload.Request.DeploymentID,
		TemplateID: payload.Request.TemplateID, Release: payload.Request.Release,
		Environment: payload.Request.Environment, Status: status, Reason: payload.Request.Reason,
		RequestedBy: payload.RequestedBy, RequestedAt: payload.RequestedAt,
		ActivateAt: payload.Request.At, ExpiresAt: payload.Request.ExpiresAt, Seq: event.Seq,
	})
}

func applyDeploymentActivated(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload DeploymentActivatedEvent
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	key := store.Key(event.Org, event.Workspace, payload.DeploymentID)
	deployment, found, err := store.GetDoc[DeploymentView](ctx, st, CollectionDeployments, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("agent governance: activation at seq %d references unknown deployment", event.Seq)
	}
	if deployment.Status != DeploymentScheduled || deployment.TemplateID != payload.TemplateID ||
		deployment.Release != payload.Release || deployment.Environment != payload.Environment {
		return fmt.Errorf("agent governance: activation at seq %d has stale lineage", event.Seq)
	}
	release, err := requireRelease(ctx, st, event, payload.TemplateID, payload.Release)
	if err != nil {
		return err
	}
	if release.Status != ReleaseApproved ||
		(deployment.ActivateAt != nil && payload.ActivatedAt.Before(*deployment.ActivateAt)) ||
		(deployment.ExpiresAt != nil && !payload.ActivatedAt.Before(*deployment.ExpiresAt)) {
		return fmt.Errorf("agent governance: activation at seq %d violates release or time gate", event.Seq)
	}
	deployment.Status, deployment.ActivatedBy = DeploymentActive, payload.ActivatedBy
	deployment.ActivatedAt, deployment.Seq = payload.ActivatedAt, event.Seq
	return store.PutDoc(ctx, st, CollectionDeployments, key, deployment)
}

func applyDeploymentPaused(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload DeploymentPausedEvent
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	key := store.Key(event.Org, event.Workspace, payload.DeploymentID)
	deployment, found, err := store.GetDoc[DeploymentView](ctx, st, CollectionDeployments, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("agent governance: pause at seq %d references unknown deployment", event.Seq)
	}
	if deployment.Status != DeploymentScheduled && deployment.Status != DeploymentActive {
		return fmt.Errorf("agent governance: pause at seq %d targets %s deployment", event.Seq, deployment.Status)
	}
	deployment.Status, deployment.PausedBy = DeploymentPaused, payload.PausedBy
	deployment.PausedAt, deployment.Reason, deployment.Seq = payload.PausedAt, payload.Reason, event.Seq
	return store.PutDoc(ctx, st, CollectionDeployments, key, deployment)
}

func applyDeploymentResumed(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload DeploymentResumedEvent
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	key := store.Key(event.Org, event.Workspace, payload.DeploymentID)
	deployment, found, err := store.GetDoc[DeploymentView](ctx, st, CollectionDeployments, key)
	if err != nil {
		return err
	}
	if !found || deployment.Status != DeploymentPaused ||
		strings.TrimSpace(payload.Reason) == "" ||
		strings.TrimSpace(payload.ResumedBy) == "" {
		return fmt.Errorf(
			"agent governance: resume at seq %d has stale deployment lineage", event.Seq,
		)
	}
	release, err := requireRelease(
		ctx, st, event, deployment.TemplateID, deployment.Release,
	)
	if err != nil {
		return err
	}
	if release.Status != ReleaseApproved ||
		(deployment.ExpiresAt != nil && !payload.ResumedAt.Before(*deployment.ExpiresAt)) {
		return fmt.Errorf(
			"agent governance: resume at seq %d violates release or time gate", event.Seq,
		)
	}
	deployment.Status, deployment.Reason = DeploymentActive, payload.Reason
	deployment.ResumedBy, deployment.ResumedAt = payload.ResumedBy, payload.ResumedAt
	deployment.Seq = event.Seq
	return store.PutDoc(ctx, st, CollectionDeployments, key, deployment)
}

func applyDeploymentRolledBack(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload DeploymentRolledBackEvent
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	key := store.Key(event.Org, event.Workspace, payload.DeploymentID)
	deployment, found, err := store.GetDoc[DeploymentView](ctx, st, CollectionDeployments, key)
	if err != nil {
		return err
	}
	if !found || deployment.Release != payload.FromRelease {
		return fmt.Errorf("agent governance: rollback at seq %d has stale deployment lineage", event.Seq)
	}
	if deployment.Status != DeploymentActive {
		return fmt.Errorf("agent governance: rollback at seq %d targets non-active deployment", event.Seq)
	}
	target, err := requireRelease(ctx, st, event, deployment.TemplateID, payload.ToRelease)
	if err != nil {
		return err
	}
	if target.Status != ReleaseApproved {
		return fmt.Errorf("agent governance: rollback at seq %d targets unapproved release", event.Seq)
	}
	deployment.PreviousRelease, deployment.Release = payload.FromRelease, payload.ToRelease
	deployment.Status, deployment.Reason, deployment.Seq = DeploymentActive, payload.Reason, event.Seq
	return store.PutDoc(ctx, st, CollectionDeployments, key, deployment)
}

func applyAssistRequested(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload AssistRequested
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	if payload.AssistID == "" || payload.CaseID == "" || !payload.Kind.Valid() ||
		payload.TemplateID == "" || payload.Release < 1 ||
		!engine.ValidEnvironment(string(payload.Environment)) ||
		payload.EvidenceSeq < 1 || len(payload.EvidenceIDs) == 0 ||
		payload.InvocationID == "" ||
		((payload.InputSubject == "") != (len(payload.SealedInput) == 0)) ||
		(payload.InputSubject != "" && payload.InputSubject != payload.Subject) {
		return fmt.Errorf(
			"agent governance: invalid assist request at seq %d", event.Seq,
		)
	}
	if payload.PolicySource != nil {
		if err := payload.PolicySource.Validate(); err != nil {
			return fmt.Errorf(
				"agent governance: invalid assist policy source at seq %d: %w",
				event.Seq, err,
			)
		}
	}
	return store.PutDoc(ctx, st, CollectionAssists, store.Key(event.Org, event.Workspace, payload.AssistID), AssistView{
		Org: event.Org, Workspace: event.Workspace, AssistID: payload.AssistID, CaseID: payload.CaseID,
		Kind: payload.Kind, TemplateID: payload.TemplateID, Release: payload.Release,
		Environment: payload.Environment, EvidenceIDs: payload.EvidenceIDs,
		EvidenceSeq: payload.EvidenceSeq, CurrentEvidenceSeq: payload.EvidenceSeq,
		InvocationID: payload.InvocationID, Status: AssistRequestedStatus, RequestedBy: payload.RequestedBy,
		PolicySource: payload.PolicySource,
		RequestedAt:  payload.RequestedAt, Seq: event.Seq,
	})
}

func applyAssistClaimed(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload AssistClaimed
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	return updateAssist(ctx, event, st, payload.AssistID, func(view *AssistView) error {
		if view.Status != AssistRequestedStatus || payload.Owner == "" ||
			payload.Attempt <= view.Attempt || !payload.LeaseUntil.After(event.Time) {
			return errors.New("assist claim is invalid or stale")
		}
		view.Status, view.WorkerOwner = AssistRunningStatus, payload.Owner
		view.Attempt, view.LeaseUntil = payload.Attempt, payload.LeaseUntil
		return nil
	})
}

func applyAssistHeartbeat(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload AssistHeartbeat
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	return updateAssist(ctx, event, st, payload.AssistID, func(view *AssistView) error {
		if view.Status != AssistRunningStatus || view.WorkerOwner != payload.Owner ||
			view.Attempt != payload.Attempt ||
			!payload.LeaseUntil.After(view.LeaseUntil) {
			return errors.New("assist heartbeat does not own the active lease")
		}
		view.LeaseUntil = payload.LeaseUntil
		return nil
	})
}

func applyAssistCompleted(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload AssistCompleted
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	return updateAssist(ctx, event, st, payload.Result.AssistID, func(view *AssistView) error {
		switch {
		case len(payload.SealedContent) > 0:
			if payload.ContentSubject == "" {
				return errors.New("sealed assist content has no subject")
			}
			if len(payload.Result.Suggestion) > 0 || len(payload.Result.Citations) > 0 ||
				len(payload.Result.Unsupported) > 0 || len(payload.Result.Limitations) > 0 {
				return fmt.Errorf(
					"sealed assist event also contains cleartext content "+
						"(suggestion=%d citations=%d unsupported=%d limitations=%d)",
					len(payload.Result.Suggestion), len(payload.Result.Citations),
					len(payload.Result.Unsupported), len(payload.Result.Limitations),
				)
			}
			if err := payload.Result.validateMetadata(); err != nil {
				return err
			}
		default:
			if payload.ContentSubject != "" {
				return errors.New("assist content subject has no sealed content")
			}
			if err := payload.Result.Validate(); err != nil {
				return err
			}
		}
		if view.Status == AssistRunningStatus {
			if view.WorkerOwner != payload.ClaimOwner ||
				view.Attempt != payload.ClaimAttempt ||
				!payload.CompletedAt.Before(view.LeaseUntil) {
				return errors.New("assist completion does not own the active lease")
			}
		} else if view.Status != AssistRequestedStatus &&
			view.Status != AssistAwaitingApprovalStatus {
			return fmt.Errorf("assist is already %s", view.Status)
		}
		view.Status, view.Result, view.CompletedAt = AssistCompletedStatus, &payload.Result, payload.CompletedAt
		view.WorkerOwner, view.LeaseUntil = "", time.Time{}
		view.ContentSubject = payload.ContentSubject
		view.SealedContent = append([]byte(nil), payload.SealedContent...)
		return nil
	})
}

func applyAssistFailed(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload AssistFailed
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	return updateAssist(ctx, event, st, payload.AssistID, func(view *AssistView) error {
		if view.Status == AssistRunningStatus {
			if view.WorkerOwner != payload.ClaimOwner ||
				view.Attempt != payload.ClaimAttempt ||
				!payload.FailedAt.Before(view.LeaseUntil) {
				return errors.New("assist failure does not own the active lease")
			}
		} else if view.Status != AssistRequestedStatus &&
			view.Status != AssistAwaitingApprovalStatus {
			return fmt.Errorf("assist is already %s", view.Status)
		}
		view.Status, view.Failure, view.CompletedAt = AssistFailedStatus, payload.Reason, payload.FailedAt
		view.WorkerOwner, view.LeaseUntil = "", time.Time{}
		return nil
	})
}

func applyAssistDeadLettered(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload AssistDeadLettered
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	return updateAssist(ctx, event, st, payload.AssistID, func(view *AssistView) error {
		if view.Status != AssistRunningStatus || view.Attempt != payload.Attempt ||
			payload.Reason == "" || payload.At.Before(view.LeaseUntil) {
			return errors.New("assist dead letter does not match an expired active lease")
		}
		view.Status, view.Failure, view.CompletedAt = AssistDeadLetterStatus, payload.Reason, payload.At
		view.WorkerOwner, view.LeaseUntil = "", time.Time{}
		return nil
	})
}

func applyAssistRetryRequested(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload AssistRetryRequested
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	approvals, err := store.ListDocs[ToolApprovalView](
		ctx, st, CollectionToolApprovals, store.Key(event.Org, event.Workspace, ""),
	)
	if err != nil {
		return err
	}
	for _, approval := range approvals {
		if approval.AssistID == payload.AssistID &&
			approval.Status != ToolApprovalApproved {
			return errors.New(
				"rejected or expired tool request cannot be retried",
			)
		}
	}
	return updateAssist(ctx, event, st, payload.AssistID, func(view *AssistView) error {
		if (view.Status != AssistFailedStatus && view.Status != AssistDeadLetterStatus) ||
			payload.Reason == "" || payload.RequestedBy == "" {
			return errors.New("assist retry does not target a retryable terminal assist")
		}
		if !payload.AcknowledgeAtLeastOnce {
			return errors.New("assist retry lacks at-least-once acknowledgement")
		}
		view.Status, view.Failure, view.CompletedAt = AssistRequestedStatus, "", time.Time{}
		return nil
	})
}

func applyAssistCancelRequested(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload AssistCancelRequested
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	return updateAssist(ctx, event, st, payload.AssistID, func(view *AssistView) error {
		if view.Status != AssistRequestedStatus && view.Status != AssistRunningStatus {
			return errors.New("assist cancellation targets a non-cancellable assist")
		}
		if payload.Reason == "" || payload.RequestedBy == "" {
			return errors.New("assist cancellation reason and actor are required")
		}
		view.Status, view.Failure, view.CompletedAt = AssistCancelledStatus, payload.Reason, payload.At
		view.WorkerOwner, view.LeaseUntil = "", time.Time{}
		return nil
	})
}

func applyAssistActed(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload AssistReviewerActed
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	return updateAssist(ctx, event, st, payload.Action.AssistID, func(view *AssistView) error {
		if view.Status != AssistCompletedStatus || view.Action != nil {
			return fmt.Errorf("assist is not awaiting reviewer action")
		}
		if err := payload.Validate(AssistRequested{
			AssistID: view.AssistID, EvidenceSeq: view.EvidenceSeq,
			Subject: view.ContentSubject,
		}); err != nil {
			return err
		}
		view.Action, view.ActionActor = &payload.Action, payload.Actor
		view.SuggestionHash, view.FinalHash = payload.SuggestionHash, payload.FinalHash
		view.Differences = append(
			[]SuggestionDifference(nil), payload.Differences...,
		)
		view.ActionContentSubject = payload.ContentSubject
		view.SealedActionFinal = append([]byte(nil), payload.SealedFinal...)
		return nil
	})
}

func applyToolApprovalRequested(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload ToolApprovalRequested
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	assistKey := store.Key(event.Org, event.Workspace, payload.AssistID)
	assist, found, err := store.GetDoc[AssistView](ctx, st, CollectionAssists, assistKey)
	if err != nil {
		return err
	}
	if !found ||
		(assist.Status != AssistRequestedStatus && assist.Status != AssistRunningStatus) ||
		payload.ApprovalID == "" ||
		payload.InvocationID == "" || payload.InvocationID != assist.InvocationID ||
		payload.CallID == "" || payload.Name == "" ||
		payload.Purpose == "" || payload.ArgumentsHash == "" ||
		!payload.ExpiresAt.After(payload.RequestedAt) {
		return fmt.Errorf(
			"agent governance: invalid tool approval request at seq %d", event.Seq,
		)
	}
	approvalKey := store.Key(event.Org, event.Workspace, payload.ApprovalID)
	if _, exists, err := store.GetDoc[ToolApprovalView](
		ctx, st, CollectionToolApprovals, approvalKey,
	); err != nil {
		return err
	} else if exists {
		return fmt.Errorf(
			"agent governance: duplicate tool approval %q at seq %d",
			payload.ApprovalID, event.Seq,
		)
	}
	assist.Status, assist.Seq = AssistAwaitingApprovalStatus, event.Seq
	assist.WorkerOwner, assist.LeaseUntil = "", time.Time{}
	if err := store.PutDoc(ctx, st, CollectionAssists, assistKey, assist); err != nil {
		return err
	}
	return store.PutDoc(ctx, st, CollectionToolApprovals, approvalKey, ToolApprovalView{
		Org: event.Org, Workspace: event.Workspace,
		ApprovalID: payload.ApprovalID, AssistID: payload.AssistID,
		InvocationID: payload.InvocationID, CallID: payload.CallID,
		Name: payload.Name, Purpose: payload.Purpose, ArgumentsHash: payload.ArgumentsHash,
		Status: ToolApprovalPending, RequestedBy: payload.RequestedBy,
		RequestedAt: payload.RequestedAt, ExpiresAt: payload.ExpiresAt, Seq: event.Seq,
	})
}

func applyToolApprovalDecided(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload ToolApprovalDecided
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	key := store.Key(event.Org, event.Workspace, payload.ApprovalID)
	approval, found, err := store.GetDoc[ToolApprovalView](
		ctx, st, CollectionToolApprovals, key,
	)
	if err != nil {
		return err
	}
	if !found || approval.Status != ToolApprovalPending ||
		approval.AssistID != payload.AssistID ||
		(payload.Decision != ToolApprovalApproved &&
			payload.Decision != ToolApprovalRejected) ||
		payload.Reason == "" || payload.DecidedBy == "" ||
		!payload.DecidedAt.Before(approval.ExpiresAt) {
		return fmt.Errorf(
			"agent governance: invalid tool approval decision at seq %d", event.Seq,
		)
	}
	approval.Status, approval.DecidedBy, approval.DecidedAt =
		payload.Decision, payload.DecidedBy, payload.DecidedAt
	approval.Reason, approval.Seq = payload.Reason, event.Seq
	if err := store.PutDoc(ctx, st, CollectionToolApprovals, key, approval); err != nil {
		return err
	}
	if payload.Decision == ToolApprovalRejected {
		return terminalToolApprovalAssist(
			ctx, event, st, payload.AssistID, "tool approval rejected: "+payload.Reason,
		)
	}
	return updateAssist(ctx, event, st, payload.AssistID, func(view *AssistView) error {
		if view.Status != AssistAwaitingApprovalStatus {
			return errors.New("approved tool call has no waiting assist")
		}
		view.Status, view.Failure, view.CompletedAt = AssistRequestedStatus, "", time.Time{}
		return nil
	})
}

func applyToolApprovalExpired(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
) error {
	var payload ToolApprovalExpired
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	key := store.Key(event.Org, event.Workspace, payload.ApprovalID)
	approval, found, err := store.GetDoc[ToolApprovalView](
		ctx, st, CollectionToolApprovals, key,
	)
	if err != nil {
		return err
	}
	if !found || approval.Status != ToolApprovalPending ||
		approval.AssistID != payload.AssistID ||
		payload.ExpiredAt.Before(approval.ExpiresAt) {
		return fmt.Errorf(
			"agent governance: invalid tool approval expiry at seq %d", event.Seq,
		)
	}
	approval.Status, approval.DecidedAt, approval.Seq =
		ToolApprovalExpiredStatus, payload.ExpiredAt, event.Seq
	approval.Reason = "approval window expired"
	if err := store.PutDoc(ctx, st, CollectionToolApprovals, key, approval); err != nil {
		return err
	}
	return terminalToolApprovalAssist(
		ctx, event, st, payload.AssistID, "tool approval expired",
	)
}

func terminalToolApprovalAssist(
	ctx context.Context,
	event eventlog.Envelope,
	st store.Store,
	assistID, failure string,
) error {
	key := store.Key(event.Org, event.Workspace, assistID)
	assist, found, err := store.GetDoc[AssistView](ctx, st, CollectionAssists, key)
	if err != nil {
		return err
	}
	if !found || assist.Status != AssistAwaitingApprovalStatus {
		return fmt.Errorf(
			"agent governance: tool approval at seq %d references no waiting assist",
			event.Seq,
		)
	}
	assist.Status, assist.Failure, assist.CompletedAt, assist.Seq =
		"failed", failure, event.Time, event.Seq
	return store.PutDoc(ctx, st, CollectionAssists, key, assist)
}

func applyIncidentOpened(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload SafetyIncidentOpened
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	if payload.IncidentID == "" || payload.TemplateID == "" || payload.Release < 1 ||
		payload.Kind == "" || !payload.Severity.Valid() || payload.Summary == "" {
		return fmt.Errorf("agent governance: incomplete safety incident at seq %d", event.Seq)
	}
	return store.PutDoc(ctx, st, CollectionIncidents, store.Key(event.Org, event.Workspace, payload.IncidentID), IncidentView{
		Org: event.Org, Workspace: event.Workspace, IncidentID: payload.IncidentID,
		TemplateID: payload.TemplateID, Release: payload.Release, DeploymentID: payload.DeploymentID,
		RunID: payload.RunID, Kind: payload.Kind, Severity: payload.Severity, Summary: payload.Summary,
		Evidence: payload.Evidence, Status: "open", OpenedAt: payload.OpenedAt, Seq: event.Seq,
	})
}

func applyIncidentResolved(ctx context.Context, event eventlog.Envelope, st store.Store) error {
	var payload SafetyIncidentResolved
	if err := decodeEvent(event, &payload); err != nil {
		return err
	}
	key := store.Key(event.Org, event.Workspace, payload.IncidentID)
	view, found, err := store.GetDoc[IncidentView](ctx, st, CollectionIncidents, key)
	if err != nil {
		return err
	}
	if !found || view.Status != "open" {
		return fmt.Errorf("agent governance: resolution at seq %d references no open incident", event.Seq)
	}
	view.Status, view.Resolution, view.ResolvedAt, view.Seq = "resolved", payload.Resolution, payload.ResolvedAt, event.Seq
	return store.PutDoc(ctx, st, CollectionIncidents, key, view)
}

func updateAssist(ctx context.Context, event eventlog.Envelope, st store.Store, assistID string, mutate func(*AssistView) error) error {
	key := store.Key(event.Org, event.Workspace, assistID)
	view, found, err := store.GetDoc[AssistView](ctx, st, CollectionAssists, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("agent governance: event at seq %d references unknown assist %q", event.Seq, assistID)
	}
	if err := mutate(&view); err != nil {
		return fmt.Errorf("agent governance: apply assist event seq %d: %w", event.Seq, err)
	}
	view.Seq = event.Seq
	return store.PutDoc(ctx, st, CollectionAssists, key, view)
}

func requireRelease(ctx context.Context, st store.Store, event eventlog.Envelope, templateID string, release int) (ReleaseView, error) {
	view, found, err := store.GetDoc[ReleaseView](ctx, st, CollectionReleases, releaseKey(event.Org, event.Workspace, templateID, release))
	if err != nil {
		return ReleaseView{}, err
	}
	if !found {
		return ReleaseView{}, fmt.Errorf("agent governance: event at seq %d references unknown release %s@%d", event.Seq, templateID, release)
	}
	return view, nil
}

func decodeEvent(event eventlog.Envelope, value any) error {
	if err := json.Unmarshal(event.Payload, value); err != nil {
		return fmt.Errorf("agent governance: decode %s seq %d: %w", event.Type, event.Seq, err)
	}
	return nil
}

func releaseKey(org, workspace, templateID string, release int) string {
	return store.Key(org, workspace, templateID+":"+strconv.Itoa(release))
}

func suiteKey(org, workspace, suiteID string, version int) string {
	return store.Key(org, workspace, suiteID+":"+strconv.Itoa(version))
}

func ReadTemplate(ctx context.Context, st store.Store, id identity.Identity, templateID string) (TemplateView, bool, error) {
	return store.GetDoc[TemplateView](ctx, st, CollectionTemplates, store.Key(id.Org, id.Workspace, templateID))
}

func ListTemplates(ctx context.Context, st store.Store, id identity.Identity) ([]TemplateView, error) {
	return store.QueryDocs(ctx, st, CollectionTemplates, store.Key(id.Org, id.Workspace, ""), nil,
		func(a, b TemplateView) bool { return a.Slug < b.Slug })
}

func ReadRelease(ctx context.Context, st store.Store, id identity.Identity, templateID string, release int) (ReleaseView, bool, error) {
	return store.GetDoc[ReleaseView](ctx, st, CollectionReleases, releaseKey(id.Org, id.Workspace, templateID, release))
}

func ListReleases(ctx context.Context, st store.Store, id identity.Identity, templateID string) ([]ReleaseView, error) {
	return store.QueryDocs(ctx, st, CollectionReleases, store.Key(id.Org, id.Workspace, templateID+":"),
		nil, func(a, b ReleaseView) bool { return a.Release > b.Release })
}

func ListAllReleases(ctx context.Context, st store.Store) ([]ReleaseView, error) {
	return store.QueryDocs(
		ctx, st, CollectionReleases, "", nil,
		func(a, b ReleaseView) bool {
			if a.CreatedAt.Equal(b.CreatedAt) {
				if a.TemplateID == b.TemplateID {
					return a.Release < b.Release
				}
				return a.TemplateID < b.TemplateID
			}
			return a.CreatedAt.Before(b.CreatedAt)
		},
	)
}

func ReadSuite(ctx context.Context, st store.Store, id identity.Identity, suiteID string, version int) (SuiteView, bool, error) {
	return store.GetDoc[SuiteView](ctx, st, CollectionSuites, suiteKey(id.Org, id.Workspace, suiteID, version))
}

func ListSuites(ctx context.Context, st store.Store, id identity.Identity) ([]SuiteView, error) {
	return store.QueryDocs(ctx, st, CollectionSuites, store.Key(id.Org, id.Workspace, ""), nil,
		func(a, b SuiteView) bool {
			if a.SuiteID == b.SuiteID {
				return a.Version > b.Version
			}
			return a.Name < b.Name
		})
}

func ListCampaigns(ctx context.Context, st store.Store, id identity.Identity, templateID string, release int) ([]CampaignView, error) {
	return store.QueryDocs(ctx, st, CollectionCampaigns, store.Key(id.Org, id.Workspace, ""),
		func(view CampaignView) bool { return view.TemplateID == templateID && view.Release == release },
		func(a, b CampaignView) bool { return a.RecordedAt.After(b.RecordedAt) })
}

func ReadCampaign(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	campaignID string,
) (CampaignView, bool, error) {
	return store.GetDoc[CampaignView](
		ctx, st, CollectionCampaigns, store.Key(id.Org, id.Workspace, campaignID),
	)
}

func ListDeployments(ctx context.Context, st store.Store, id identity.Identity) ([]DeploymentView, error) {
	return store.QueryDocs(ctx, st, CollectionDeployments, store.Key(id.Org, id.Workspace, ""), nil,
		func(a, b DeploymentView) bool { return a.RequestedAt.After(b.RequestedAt) })
}

func ReadDeployment(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	deploymentID string,
) (DeploymentView, bool, error) {
	return store.GetDoc[DeploymentView](
		ctx, st, CollectionDeployments, store.Key(id.Org, id.Workspace, deploymentID),
	)
}

func ListAllDeployments(ctx context.Context, st store.Store) ([]DeploymentView, error) {
	return store.QueryDocs(ctx, st, CollectionDeployments, "", nil,
		func(a, b DeploymentView) bool {
			if a.RequestedAt.Equal(b.RequestedAt) {
				return a.DeploymentID < b.DeploymentID
			}
			return a.RequestedAt.Before(b.RequestedAt)
		})
}

func ListCaseAssists(ctx context.Context, st store.Store, id identity.Identity, caseID string) ([]AssistView, error) {
	return store.QueryDocs(ctx, st, CollectionAssists, store.Key(id.Org, id.Workspace, ""),
		func(view AssistView) bool { return view.CaseID == caseID },
		func(a, b AssistView) bool { return a.RequestedAt.After(b.RequestedAt) })
}

func ListAssists(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]AssistView, error) {
	return store.QueryDocs(
		ctx, st, CollectionAssists, store.Key(id.Org, id.Workspace, ""), nil,
		func(a, b AssistView) bool { return a.RequestedAt.After(b.RequestedAt) },
	)
}

func ReadAssist(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	assistID string,
) (AssistView, bool, error) {
	return store.GetDoc[AssistView](
		ctx, st, CollectionAssists, store.Key(id.Org, id.Workspace, assistID),
	)
}

func ListToolApprovals(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]ToolApprovalView, error) {
	return store.QueryDocs(
		ctx, st, CollectionToolApprovals, store.Key(id.Org, id.Workspace, ""), nil,
		func(a, b ToolApprovalView) bool { return a.RequestedAt.After(b.RequestedAt) },
	)
}

func ReadToolApproval(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	approvalID string,
) (ToolApprovalView, bool, error) {
	return store.GetDoc[ToolApprovalView](
		ctx, st, CollectionToolApprovals, store.Key(id.Org, id.Workspace, approvalID),
	)
}

func ListAllToolApprovals(
	ctx context.Context,
	st store.Store,
) ([]ToolApprovalView, error) {
	return store.QueryDocs(
		ctx, st, CollectionToolApprovals, "",
		func(view ToolApprovalView) bool { return view.Status == ToolApprovalPending },
		func(a, b ToolApprovalView) bool {
			if a.ExpiresAt.Equal(b.ExpiresAt) {
				return a.ApprovalID < b.ApprovalID
			}
			return a.ExpiresAt.Before(b.ExpiresAt)
		},
	)
}

func ListIncidents(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]IncidentView, error) {
	return store.QueryDocs(ctx, st, CollectionIncidents, store.Key(id.Org, id.Workspace, ""), nil,
		func(a, b IncidentView) bool { return a.OpenedAt.After(b.OpenedAt) })
}

func ListAllIncidents(ctx context.Context, st store.Store) ([]IncidentView, error) {
	return store.QueryDocs(ctx, st, CollectionIncidents, "",
		func(view IncidentView) bool { return view.Status == "open" },
		func(a, b IncidentView) bool {
			if a.OpenedAt.Equal(b.OpenedAt) {
				return a.IncidentID < b.IncidentID
			}
			return a.OpenedAt.Before(b.OpenedAt)
		})
}
