// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	engine "github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

const maxClaimRetries = 4

// Handler is the governed-agent write side. Every decision folds the
// authoritative tenant stream; projections are never used to admit a release or
// deployment, so lag and process boundaries cannot weaken governance.
type Handler struct {
	log           eventlog.Log
	mu            sync.Mutex
	now           func() time.Time
	newID         func() string
	contentSealer ContentSealer
}

type ContentSealer interface {
	Seal(
		ctx context.Context,
		id identity.Identity,
		subject string,
		plain []byte,
	) ([]byte, error)
	Open(
		ctx context.Context,
		id identity.Identity,
		subject string,
		sealed []byte,
	) ([]byte, error)
}

func NewHandler(log eventlog.Log) *Handler {
	return &Handler{
		log:   log,
		now:   func() time.Time { return time.Now().UTC() },
		newID: governanceID,
	}
}

// WithNow overrides the clock used by every command, scheduler, and expiry
// decision. It is required for deterministic replay fixtures and tests.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

func (h *Handler) WithContentSealer(sealer ContentSealer) *Handler {
	h.contentSealer = sealer
	return h
}

func governanceID() string {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		panic("agent governance: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(value[:])
}

func (h *Handler) RegisterTemplate(
	ctx context.Context,
	id identity.Identity,
	template Template,
) (Template, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return Template{}, eventlog.Envelope{}, err
	}
	if template.TemplateID == "" {
		template.TemplateID = h.newID()
	}
	template.Slug = strings.TrimSpace(template.Slug)
	template.Name = strings.TrimSpace(template.Name)
	template.Task = strings.TrimSpace(template.Task)
	if err := template.Validate(); err != nil {
		return Template{}, eventlog.Envelope{}, err
	}
	event, err := h.appendUnique(
		ctx, id, TypeTemplateRegistered, TemplateRegistered{Template: template},
		h.claim(id, "template.slug", template.Slug),
	)
	if err != nil {
		if errors.Is(err, eventlog.ErrConflict) {
			return Template{}, eventlog.Envelope{}, fmt.Errorf(
				"agent governance: template slug %q already exists: %w", template.Slug, err,
			)
		}
		return Template{}, eventlog.Envelope{}, err
	}
	return template, event, nil
}

func (h *Handler) CreateRelease(
	ctx context.Context,
	id identity.Identity,
	templateID string,
	spec ReleaseSpec,
) (int, string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	spec.Provider = strings.TrimSpace(spec.Provider)
	spec.Model = strings.TrimSpace(spec.Model)
	spec.Dependencies = CanonicalPins(spec.Dependencies)
	if err := spec.Validate(); err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	hash, err := hashJSON(spec)
	if err != nil {
		return 0, "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < maxClaimRetries; attempt++ {
		state, err := h.fold(ctx, id)
		if err != nil {
			return 0, "", eventlog.Envelope{}, err
		}
		template, ok := state.templates[templateID]
		if !ok {
			return 0, "", eventlog.Envelope{}, fmt.Errorf(
				"agent governance: unknown template %q", templateID,
			)
		}
		if template.HighImpact && !spec.RequireHumanGate {
			return 0, "", eventlog.Envelope{}, errors.New(
				"agent governance: high-impact template releases require a human gate",
			)
		}
		release := state.latestRelease(templateID) + 1
		payload := ReleaseCreated{
			TemplateID: templateID, Release: release, Spec: spec, SpecHash: hash,
			CreatedBy: id.Actor, CreatedAt: h.now(),
		}
		event, err := h.appendUnique(
			ctx, id, TypeReleaseCreated, payload,
			h.claim(id, "release", templateID, strconv.Itoa(release)),
		)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return release, hash, event, err
	}
	return 0, "", eventlog.Envelope{}, errors.New(
		"agent governance: release version changed concurrently; retry",
	)
}

// PublishSuite assigns the next immutable version and computes the dataset hash
// from its full governed definition.
func (h *Handler) PublishSuite(
	ctx context.Context,
	id identity.Identity,
	suite EvalSuite,
) (EvalSuite, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return EvalSuite{}, eventlog.Envelope{}, err
	}
	if strings.TrimSpace(suite.SuiteID) == "" {
		suite.SuiteID = h.newID()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < maxClaimRetries; attempt++ {
		state, err := h.fold(ctx, id)
		if err != nil {
			return EvalSuite{}, eventlog.Envelope{}, err
		}
		suite.Version = state.latestSuite(suite.SuiteID) + 1
		suite.DatasetHash = ""
		hash, err := hashJSON(suite)
		if err != nil {
			return EvalSuite{}, eventlog.Envelope{}, err
		}
		suite.DatasetHash = hash
		if err := suite.Validate(); err != nil {
			return EvalSuite{}, eventlog.Envelope{}, err
		}
		event, err := h.appendUnique(
			ctx, id, TypeSuitePublished,
			SuitePublished{Suite: suite, PublishedBy: id.Actor, PublishedAt: h.now()},
			h.claim(id, "suite", suite.SuiteID, strconv.Itoa(suite.Version)),
		)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return suite, event, err
	}
	return EvalSuite{}, eventlog.Envelope{}, errors.New(
		"agent governance: evaluation suite version changed concurrently; retry",
	)
}

func (h *Handler) RecordCampaign(
	ctx context.Context,
	id identity.Identity,
	templateID string,
	release int,
	suiteID string,
	suiteVersion int,
	trials []TrialResult,
) (CampaignResult, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return CampaignResult{}, eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return CampaignResult{}, eventlog.Envelope{}, err
	}
	current, ok := state.releases[releaseRef(templateID, release)]
	if !ok {
		return CampaignResult{}, eventlog.Envelope{}, fmt.Errorf(
			"agent governance: unknown release %s@%d", templateID, release,
		)
	}
	if current.status != ReleaseDraft && current.status != ReleaseEvaluated {
		return CampaignResult{}, eventlog.Envelope{}, fmt.Errorf(
			"agent governance: cannot evaluate %s release", current.status,
		)
	}
	suite, ok := state.suites[suiteRef(suiteID, suiteVersion)]
	if !ok {
		return CampaignResult{}, eventlog.Envelope{}, fmt.Errorf(
			"agent governance: unknown evaluation suite %s@%d", suiteID, suiteVersion,
		)
	}
	for index, trial := range trials {
		if trial.Provider != current.spec.Provider || trial.Model != current.spec.Model {
			return CampaignResult{}, eventlog.Envelope{}, fmt.Errorf(
				"agent governance: trial %d provider/model lineage does not match exact release",
				index+1,
			)
		}
		cost, budgetErr := enforceInvocationBudget(
			current.spec.Budget, trial.PromptTokens, trial.OutputTokens, len(trial.ToolCalls),
		)
		if budgetErr != nil && trial.Status != "budget_exceeded" {
			return CampaignResult{}, eventlog.Envelope{}, fmt.Errorf(
				"agent governance: trial %d: %w", index+1, budgetErr,
			)
		}
		if trial.CostUSD != cost {
			return CampaignResult{}, eventlog.Envelope{}, fmt.Errorf(
				"agent governance: trial %d cost %.8f does not match reviewed pricing %.8f",
				index+1, trial.CostUSD, cost,
			)
		}
	}
	result, err := BuildCampaign(h.newID(), templateID, release, suite, trials)
	if err != nil {
		return CampaignResult{}, eventlog.Envelope{}, err
	}
	event, err := h.appendUnique(
		ctx, id, TypeCampaignRecorded,
		CampaignRecorded{Result: result, RecordedBy: id.Actor, RecordedAt: h.now()},
		h.claim(id, "campaign", result.CampaignID),
	)
	return result, event, err
}

func (h *Handler) AdjudicateCampaignTrial(
	ctx context.Context,
	id identity.Identity,
	campaignID, caseID string,
	trial int,
	passed bool,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	adjudication := TrialAdjudication{
		CampaignID: campaignID, CaseID: caseID, Trial: trial, Passed: passed,
		Reason: strings.TrimSpace(reason), AdjudicatedBy: id.Actor, AdjudicatedAt: h.now(),
	}
	if err := adjudication.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	campaign := state.campaigns[campaignID]
	if campaign == nil {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent governance: unknown campaign %q", campaignID,
		)
	}
	release := state.releases[releaseRef(campaign.result.TemplateID, campaign.result.Release)]
	if release == nil || release.status != ReleaseEvaluated {
		return eventlog.Envelope{}, errors.New(
			"agent governance: trials can only be adjudicated before release review",
		)
	}
	if campaign.recordedBy == id.Actor {
		return eventlog.Envelope{}, errors.New(
			"agent governance: campaign recorder cannot adjudicate their own trial",
		)
	}
	found := false
	for _, candidate := range campaign.result.Trials {
		if candidate.CaseID == caseID && candidate.Trial == trial {
			found = true
			break
		}
	}
	if !found {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent governance: campaign %q has no trial %s",
			campaignID, trialRef(caseID, trial),
		)
	}
	for _, prior := range campaign.adjudications {
		if prior.CaseID == caseID && prior.Trial == trial {
			return eventlog.Envelope{}, fmt.Errorf(
				"agent governance: trial %s was already adjudicated",
				trialRef(caseID, trial),
			)
		}
	}
	suite := state.suites[suiteRef(
		campaign.result.SuiteID, campaign.result.SuiteVersion,
	)]
	previousAssessment, err := AssessCampaign(
		campaign.result, suite, campaign.adjudications,
	)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	nextAdjudications := append(
		append([]TrialAdjudication(nil), campaign.adjudications...), adjudication,
	)
	assessment, err := AssessCampaign(
		campaign.result, suite, nextAdjudications,
	)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	return h.appendUnique(
		ctx, id, TypeCampaignTrialAdjudicated,
		CampaignTrialAdjudicated{
			Adjudication: adjudication, TemplateID: campaign.result.TemplateID,
			Release: campaign.result.Release, PreviousAssessment: previousAssessment,
			Assessment: assessment,
		},
		h.claim(id, "campaign.adjudication", campaignID, caseID, strconv.Itoa(trial)),
	)
}

func (h *Handler) RequestReview(
	ctx context.Context,
	id identity.Identity,
	templateID string,
	release int,
	campaignIDs, evidenceIDs, reviewers []string,
	expiresAt time.Time,
) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if expiresAt.IsZero() || !expiresAt.After(h.now()) {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: review expiry must be in the future",
		)
	}
	if err := uniqueNonBlank("campaign id", campaignIDs, MaxCases); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if len(campaignIDs) == 0 {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: release review requires evaluation campaigns",
		)
	}
	if err := uniqueNonBlank("review evidence id", evidenceIDs, MaxCases); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := uniqueNonBlank("reviewer", reviewers, 100); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := rejectTextPII(
		"review evidence reference",
		append(append([]string{}, campaignIDs...), evidenceIDs...)...,
	); err != nil {
		return "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	current, ok := state.releases[releaseRef(templateID, release)]
	if !ok || current.status != ReleaseEvaluated {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: only an evaluated release can enter review",
		)
	}
	template := state.templates[templateID]
	requiredSeen, adversarialSeen := false, false
	for _, campaignID := range campaignIDs {
		campaign, ok := state.campaigns[campaignID]
		if !ok || campaign.result.TemplateID != templateID ||
			campaign.result.Release != release {
			return "", eventlog.Envelope{}, fmt.Errorf(
				"agent governance: campaign %q does not belong to release %s@%d",
				campaignID, templateID, release,
			)
		}
		suite := state.suites[suiteRef(
			campaign.result.SuiteID, campaign.result.SuiteVersion,
		)]
		assessment, err := AssessCampaign(
			campaign.result, suite, campaign.adjudications,
		)
		if err != nil {
			return "", eventlog.Envelope{}, err
		}
		if assessment.Blocking {
			return "", eventlog.Envelope{}, fmt.Errorf(
				"agent governance: campaign %q blocks release review", campaignID,
			)
		}
		requiredSeen = requiredSeen || suite.Required
		adversarialSeen = adversarialSeen || suite.Adversarial
	}
	if !requiredSeen {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: review requires a passing required evaluation suite",
		)
	}
	if template.HighImpact && !adversarialSeen {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: high-impact release review requires adversarial evaluation",
		)
	}
	requestID := h.newID()
	sort.Strings(campaignIDs)
	sort.Strings(evidenceIDs)
	sort.Strings(reviewers)
	event, err := h.appendUnique(
		ctx, id, TypeReleaseReviewRequested, ReleaseReviewRequestedEvent{
			RequestID: requestID, TemplateID: templateID, Release: release,
			CampaignIDs: campaignIDs, EvidenceIDs: evidenceIDs, Reviewers: reviewers,
			RequestedBy: id.Actor, RequestedAt: h.now(), ExpiresAt: expiresAt,
		},
		h.claim(
			id, "review", templateID, strconv.Itoa(release),
			strconv.Itoa(current.reviewRevision),
		),
	)
	return requestID, event, err
}

func (h *Handler) ExpireReview(
	ctx context.Context,
	id identity.Identity,
	templateID string,
	release int,
	requestID string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	current := state.releases[releaseRef(templateID, release)]
	if current == nil || current.status != ReleaseReviewRequested ||
		current.review == nil || current.review.RequestID != requestID {
		return eventlog.Envelope{}, errors.New(
			"agent governance: release review is not awaiting expiry",
		)
	}
	now := h.now()
	if now.Before(current.review.ExpiresAt) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: release review has not expired",
		)
	}
	return h.appendUnique(
		ctx, id, TypeReleaseReviewExpired,
		ReleaseReviewExpired{
			RequestID: requestID, TemplateID: templateID, Release: release, ExpiredAt: now,
		},
		h.claim(id, "review.expire", requestID),
	)
}

func (h *Handler) ReviewRelease(
	ctx context.Context,
	id identity.Identity,
	templateID string,
	release int,
	requestID string,
	decision ReviewDecision,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if !decision.Valid() || strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New(
			"agent governance: valid review decision and reason are required",
		)
	}
	if err := rejectTextPII("release review reason", reason); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	current, ok := state.releases[releaseRef(templateID, release)]
	if !ok || current.status != ReleaseReviewRequested || current.review == nil ||
		current.review.RequestID != requestID {
		return eventlog.Envelope{}, errors.New(
			"agent governance: review request is not open for this release",
		)
	}
	if !h.now().Before(current.review.ExpiresAt) {
		return eventlog.Envelope{}, errors.New("agent governance: review request has expired")
	}
	if decision == ReviewApprove {
		if id.Actor == current.createdBy || id.Actor == current.review.RequestedBy {
			return eventlog.Envelope{}, fmt.Errorf(
				"agent governance: four-eyes — %q cannot approve this release", id.Actor,
			)
		}
		if len(current.review.Reviewers) > 0 && !contains(current.review.Reviewers, id.Actor) {
			return eventlog.Envelope{}, fmt.Errorf(
				"agent governance: %q is not assigned to this review", id.Actor,
			)
		}
	}
	return h.appendUnique(
		ctx, id, TypeReleaseReviewed, ReleaseReviewed{
			RequestID: requestID, TemplateID: templateID, Release: release,
			Decision: decision, Reason: strings.TrimSpace(reason),
			ReviewedBy: id.Actor, ReviewedAt: h.now(),
		},
		h.claim(id, "review.decision", requestID),
	)
}

func (h *Handler) RequestDeployment(
	ctx context.Context,
	id identity.Identity,
	request DeploymentRequest,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if request.DeploymentID == "" {
		request.DeploymentID = h.newID()
	}
	if err := request.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	current, ok := state.releases[releaseRef(request.TemplateID, request.Release)]
	if !ok || current.status != ReleaseApproved {
		return eventlog.Envelope{}, errors.New(
			"agent governance: deployments require an approved exact release",
		)
	}
	if request.ExpiresAt != nil && !request.ExpiresAt.After(h.now()) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: deployment expiry must be in the future",
		)
	}
	if state.hasOpenCriticalIncident(request.TemplateID, request.Release) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: release has an open critical safety incident",
		)
	}
	if active := state.environmentBinding(request.TemplateID, request.Environment); active != nil {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent governance: environment already has %s deployment %q; pause it before replacing",
			active.status, active.request.DeploymentID,
		)
	}
	return h.appendUnique(
		ctx, id, TypeDeploymentRequested,
		DeploymentRequestedEvent{Request: request, RequestedBy: id.Actor, RequestedAt: h.now()},
		h.claim(id, "deployment", request.DeploymentID),
	)
}

func (h *Handler) ActivateDeployment(
	ctx context.Context,
	id identity.Identity,
	deploymentID string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	deployment, ok := state.deployments[deploymentID]
	if !ok || deployment.status != DeploymentScheduled {
		return eventlog.Envelope{}, errors.New(
			"agent governance: deployment is not scheduled",
		)
	}
	now := h.now()
	if deployment.request.At != nil && now.Before(*deployment.request.At) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: deployment activation time has not arrived",
		)
	}
	if deployment.request.ExpiresAt != nil && !now.Before(*deployment.request.ExpiresAt) {
		return eventlog.Envelope{}, errors.New("agent governance: deployment has expired")
	}
	release := state.releases[releaseRef(deployment.request.TemplateID, deployment.request.Release)]
	if release.status != ReleaseApproved {
		return eventlog.Envelope{}, errors.New(
			"agent governance: deployment release is no longer approved",
		)
	}
	if state.hasOpenCriticalIncident(deployment.request.TemplateID, deployment.request.Release) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: release has an open critical safety incident",
		)
	}
	if active := state.environmentBindingExcept(
		deployment.request.TemplateID, deployment.request.Environment, deploymentID,
	); active != nil {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent governance: environment already has %s deployment %q",
			active.status, active.request.DeploymentID,
		)
	}
	return h.appendUnique(
		ctx, id, TypeDeploymentActivated, DeploymentActivatedEvent{
			DeploymentID: deploymentID, TemplateID: deployment.request.TemplateID,
			Release: deployment.request.Release, Environment: deployment.request.Environment,
			ActivatedBy: id.Actor, ActivatedAt: now,
		},
		h.claim(id, "deployment.activate", deploymentID),
	)
}

func (h *Handler) PauseDeployment(
	ctx context.Context,
	id identity.Identity,
	deploymentID, reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("agent governance: pause reason is required")
	}
	if err := rejectTextPII("deployment pause reason", reason); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	deployment, ok := state.deployments[deploymentID]
	if !ok || (deployment.status != DeploymentScheduled && deployment.status != DeploymentActive) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: only a scheduled or active deployment can be paused",
		)
	}
	return h.appendUnique(
		ctx, id, TypeDeploymentPaused, DeploymentPausedEvent{
			DeploymentID: deploymentID, Reason: strings.TrimSpace(reason),
			PausedBy: id.Actor, PausedAt: h.now(),
		},
		h.claim(id, "deployment.pause", deploymentID, strconv.Itoa(deployment.revision)),
	)
}

func (h *Handler) ResumeDeployment(
	ctx context.Context,
	id identity.Identity,
	deploymentID, reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("agent governance: resume reason is required")
	}
	if err := rejectTextPII("deployment resume reason", reason); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	deployment, ok := state.deployments[deploymentID]
	if !ok || deployment.status != DeploymentPaused {
		return eventlog.Envelope{}, errors.New(
			"agent governance: only a paused deployment can be resumed",
		)
	}
	now := h.now()
	if deployment.request.ExpiresAt != nil && !now.Before(*deployment.request.ExpiresAt) {
		return eventlog.Envelope{}, errors.New("agent governance: deployment has expired")
	}
	release := state.releases[releaseRef(
		deployment.request.TemplateID, deployment.request.Release,
	)]
	if release == nil || release.status != ReleaseApproved {
		return eventlog.Envelope{}, errors.New(
			"agent governance: deployment release is no longer approved",
		)
	}
	if state.hasOpenCriticalIncident(
		deployment.request.TemplateID, deployment.request.Release,
	) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: resolve critical safety incidents before resuming",
		)
	}
	if assessment := state.circuitAssessment(deployment.request, release.spec, now); assessment.Tripped {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent governance: circuit breaker remains open (%d/%d failures, rate %.4f >= %.4f)",
			assessment.Failures, assessment.Samples, assessment.ObservedRate,
			assessment.Threshold,
		)
	}
	if active := state.environmentBindingExcept(
		deployment.request.TemplateID, deployment.request.Environment, deploymentID,
	); active != nil {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent governance: environment already has %s deployment %q",
			active.status, active.request.DeploymentID,
		)
	}
	return h.appendUnique(
		ctx, id, TypeDeploymentResumed, DeploymentResumedEvent{
			DeploymentID: deploymentID, Reason: strings.TrimSpace(reason),
			ResumedBy: id.Actor, ResumedAt: now,
		},
		h.claim(
			id, "deployment.resume", deploymentID, strconv.Itoa(deployment.revision),
		),
	)
}

func (h *Handler) RollbackDeployment(
	ctx context.Context,
	id identity.Identity,
	deploymentID string,
	toRelease int,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("agent governance: rollback reason is required")
	}
	if err := rejectTextPII("deployment rollback reason", reason); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	deployment, ok := state.deployments[deploymentID]
	if !ok || deployment.status != DeploymentActive {
		return eventlog.Envelope{}, errors.New(
			"agent governance: only an active deployment can be rolled back",
		)
	}
	if toRelease == deployment.request.Release {
		return eventlog.Envelope{}, errors.New(
			"agent governance: rollback release must differ from active release",
		)
	}
	target, ok := state.releases[releaseRef(deployment.request.TemplateID, toRelease)]
	if !ok || target.status != ReleaseApproved {
		return eventlog.Envelope{}, errors.New(
			"agent governance: rollback target must be an approved exact release",
		)
	}
	if state.hasOpenCriticalIncident(deployment.request.TemplateID, toRelease) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: rollback target has an open critical safety incident",
		)
	}
	return h.appendUnique(
		ctx, id, TypeDeploymentRolledBack, DeploymentRolledBackEvent{
			DeploymentID: deploymentID, FromRelease: deployment.request.Release,
			ToRelease: toRelease, Reason: strings.TrimSpace(reason),
			RolledBackBy: id.Actor, RolledBackAt: h.now(),
		},
		h.claim(id, "deployment.rollback", deploymentID, strconv.Itoa(deployment.revision)),
	)
}

func (h *Handler) RetireRelease(
	ctx context.Context,
	id identity.Identity,
	templateID string,
	release int,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("agent governance: retirement reason is required")
	}
	if err := rejectTextPII("release retirement reason", reason); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	current, ok := state.releases[releaseRef(templateID, release)]
	if !ok || current.status == ReleaseRetired {
		return eventlog.Envelope{}, errors.New(
			"agent governance: release is unknown or already retired",
		)
	}
	for _, deployment := range state.deployments {
		if deployment.request.TemplateID == templateID &&
			deployment.request.Release == release &&
			(deployment.status == DeploymentActive || deployment.status == DeploymentScheduled) {
			return eventlog.Envelope{}, fmt.Errorf(
				"agent governance: pause deployment %q before retiring release",
				deployment.request.DeploymentID,
			)
		}
	}
	return h.appendUnique(
		ctx, id, TypeReleaseRetired, ReleaseRetiredEvent{
			TemplateID: templateID, Release: release,
			Reason: strings.TrimSpace(reason), RetiredAt: h.now(),
		},
		h.claim(id, "release.retire", templateID, strconv.Itoa(release)),
	)
}

// RequestAssist records a bounded suggestion request against an exact active
// deployment and immutable case-evidence snapshot. It does not execute a model
// or mutate the case; a worker records the terminal result separately.
func (h *Handler) RequestAssist(
	ctx context.Context,
	id identity.Identity,
	request AssistRequested,
) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if h.contentSealer != nil && strings.TrimSpace(request.Subject) == "" {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: case subject is required for erasable assist content",
		)
	}
	if h.contentSealer != nil &&
		(request.InputSubject != request.Subject || len(request.SealedInput) == 0) {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: durable assist requires a sealed case input snapshot",
		)
	}
	if (request.InputSubject == "") != (len(request.SealedInput) == 0) {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: assist input subject and ciphertext must be recorded together",
		)
	}
	if !request.Kind.Valid() || request.CaseID == "" || request.TemplateID == "" ||
		request.Release < 1 || !engine.ValidEnvironment(string(request.Environment)) ||
		request.EvidenceSeq < 1 {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: assist request lineage is incomplete",
		)
	}
	if request.IdempotencyHash != "" && !validSHA256(request.IdempotencyHash) {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: assist idempotency hash must be SHA-256",
		)
	}
	if err := uniqueNonBlank("assist evidence id", request.EvidenceIDs, MaxCases); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if len(request.EvidenceIDs) == 0 {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: assist request requires governed case evidence",
		)
	}
	if request.PolicySource != nil {
		if err := request.PolicySource.Validate(); err != nil {
			return "", eventlog.Envelope{}, err
		}
	}
	request.RequestedBy, request.RequestedAt = id.Actor, h.now()
	request.AssistID = h.newID()
	if request.InvocationID == "" {
		request.InvocationID = h.newID()
	}
	sort.Strings(request.EvidenceIDs)
	requestHash, err := hashJSON(struct {
		CaseID       string
		Kind         AssistKind
		TemplateID   string
		Release      int
		Environment  engine.Environment
		EvidenceIDs  []string
		EvidenceSeq  uint64
		PolicySource *AssistPolicySource
	}{
		request.CaseID, request.Kind, request.TemplateID, request.Release,
		request.Environment, request.EvidenceIDs, request.EvidenceSeq, request.PolicySource,
	})
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	request.RequestHash = requestHash
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < maxClaimRetries; attempt++ {
		state, err := h.fold(ctx, id)
		if err != nil {
			return "", eventlog.Envelope{}, err
		}
		release, ok := state.releases[releaseRef(request.TemplateID, request.Release)]
		if !ok || release.status != ReleaseApproved {
			return "", eventlog.Envelope{}, errors.New(
				"agent governance: assist requires an approved exact release",
			)
		}
		deployment := state.environmentBinding(request.TemplateID, request.Environment)
		if deployment == nil || deployment.status != DeploymentActive ||
			deployment.request.Release != request.Release {
			return "", eventlog.Envelope{}, errors.New(
				"agent governance: assist release is not active in the requested environment",
			)
		}
		if state.hasOpenCriticalIncident(request.TemplateID, request.Release) {
			return "", eventlog.Envelope{}, errors.New(
				"agent governance: assist release has an open critical safety incident",
			)
		}
		assessment := state.circuitAssessment(deployment.request, release.spec, h.now())
		if assessment.Tripped {
			if err := h.openCircuitIncident(
				ctx, id, deployment.request, assessment,
			); err != nil && !errors.Is(err, eventlog.ErrConflict) {
				return "", eventlog.Envelope{}, err
			}
			return "", eventlog.Envelope{}, fmt.Errorf(
				"agent governance: circuit breaker opened (%d/%d failures, rate %.4f >= %.4f)",
				assessment.Failures, assessment.Samples, assessment.ObservedRate,
				assessment.Threshold,
			)
		}
		claimParts := []string{"assist", request.AssistID}
		if request.IdempotencyHash != "" {
			for _, prior := range state.assists {
				if prior.request.IdempotencyHash != request.IdempotencyHash {
					continue
				}
				if prior.request.RequestHash != request.RequestHash {
					return "", eventlog.Envelope{}, errors.New(
						"agent governance: idempotency key was reused for a different assist request",
					)
				}
				return prior.request.AssistID, prior.envelope, nil
			}
			claimParts = []string{"assist.idempotency", request.IdempotencyHash}
		}
		if release.spec.Budget.Period != "" {
			revision, err := h.admitPeriodBudget(
				ctx, id, state, request, release.spec.Budget,
				deployment.request.DeploymentID,
			)
			if err != nil {
				return "", eventlog.Envelope{}, err
			}
			claimParts = periodBudgetClaimParts(request, release.spec.Budget, h.now(), revision)
		}
		event, err := h.appendUnique(
			ctx, id, TypeAssistRequested, request, h.claim(id, claimParts...),
		)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		if err != nil {
			return "", eventlog.Envelope{}, err
		}
		return request.AssistID, event, nil
	}
	return "", eventlog.Envelope{}, errors.New(
		"agent governance: assist admission changed concurrently; retry",
	)
}

func (h *Handler) CompleteAssist(
	ctx context.Context,
	id identity.Identity,
	result AssistResult,
) (eventlog.Envelope, error) {
	return h.completeAssist(ctx, id, result, "", 0)
}

func (h *Handler) CompleteClaimedAssist(
	ctx context.Context,
	id identity.Identity,
	result AssistResult,
	owner string,
	attempt int,
) (eventlog.Envelope, error) {
	return h.completeAssist(ctx, id, result, owner, attempt)
}

func (h *Handler) completeAssist(
	ctx context.Context,
	id identity.Identity,
	result AssistResult,
	owner string,
	attempt int,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := result.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	assist, ok := state.assists[result.AssistID]
	if !ok || (assist.status != AssistRequestedStatus &&
		assist.status != AssistRunningStatus &&
		assist.status != AssistAwaitingApprovalStatus) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist is not awaiting a result",
		)
	}
	if assist.status == AssistRunningStatus &&
		(assist.claim.Owner != owner || assist.claim.Attempt != attempt ||
			!h.now().Before(assist.leaseUntil)) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist completion does not own the active lease",
		)
	}
	if assist.status != AssistRunningStatus && (owner != "" || attempt != 0) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: non-running assist completion cannot claim a worker lease",
		)
	}
	request := assist.request
	if result.CaseID != request.CaseID || result.Kind != request.Kind ||
		result.TemplateID != request.TemplateID || result.Release != request.Release ||
		result.Environment != request.Environment || result.EvidenceSeq != request.EvidenceSeq ||
		result.InvocationID != request.InvocationID {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist result lineage does not match its request",
		)
	}
	allowed := make(map[string]bool, len(request.EvidenceIDs))
	for _, evidenceID := range request.EvidenceIDs {
		allowed[evidenceID] = true
	}
	for _, citation := range result.Citations {
		if !allowed[citation.EvidenceID] {
			return eventlog.Envelope{}, fmt.Errorf(
				"agent governance: citation %q is outside the governed evidence snapshot",
				citation.EvidenceID,
			)
		}
	}
	release := state.releases[releaseRef(request.TemplateID, request.Release)]
	if release.spec.RequireCitations && len(result.Citations) == 0 {
		return eventlog.Envelope{}, errors.New(
			"agent governance: release requires cited assist results",
		)
	}
	if result.Provider != release.spec.Provider || result.Model != release.spec.Model {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist provider/model lineage does not match the exact release",
		)
	}
	derivedCost, err := enforceInvocationBudget(
		release.spec.Budget, result.PromptTokens, result.OutputTokens, len(result.ToolCalls),
	)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if result.CostUSD != derivedCost {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent governance: assist cost %.8f does not match reviewed pricing %.8f",
			result.CostUSD, derivedCost,
		)
	}
	policies := make(map[string]ToolPolicy, len(release.spec.Tools))
	for _, policy := range release.spec.Tools {
		policies[policy.Name] = policy
	}
	seenCalls := make(map[string]bool, len(result.ToolCalls))
	humanExecutions := 0
	for _, execution := range result.ToolCalls {
		if seenCalls[execution.CallID] {
			return eventlog.Envelope{}, fmt.Errorf(
				"agent governance: duplicate tool call %q", execution.CallID,
			)
		}
		seenCalls[execution.CallID] = true
		policy, found := policies[execution.Name]
		if !found || policy.Mode == ToolForbidden || policy.Mode != execution.Mode ||
			policy.Purpose != execution.Purpose {
			return eventlog.Envelope{}, fmt.Errorf(
				"agent governance: tool execution %q does not match the reviewed release",
				execution.Name,
			)
		}
		if execution.Mode != ToolHumanBeforeCall {
			if execution.ApprovedBy != "" {
				return eventlog.Envelope{}, errors.New(
					"agent governance: automatic tool execution cannot claim a human approver",
				)
			}
			continue
		}
		humanExecutions++
		approval := state.approvalForAssist(result.AssistID)
		if approval == nil || approval.status != ToolApprovalApproved ||
			approval.request.InvocationID != result.InvocationID ||
			approval.request.CallID != execution.CallID ||
			approval.request.Name != execution.Name ||
			approval.request.Purpose != execution.Purpose ||
			approval.request.ArgumentsHash != execution.ArgumentsHash ||
			approval.decidedBy != execution.ApprovedBy {
			return eventlog.Envelope{}, errors.New(
				"agent governance: human tool execution does not match its durable approval",
			)
		}
	}
	approval := state.approvalForAssist(result.AssistID)
	if approval != nil && approval.status == ToolApprovalApproved && humanExecutions != 1 {
		return eventlog.Envelope{}, errors.New(
			"agent governance: approved assist completion must include its human-gated tool execution",
		)
	}
	if (approval == nil || approval.status != ToolApprovalApproved) && humanExecutions != 0 {
		return eventlog.Envelope{}, errors.New(
			"agent governance: human-gated tool execution has no durable approval",
		)
	}
	completed := AssistCompleted{
		Result: result, CompletedAt: h.now(),
		ClaimOwner: owner, ClaimAttempt: attempt,
	}
	if h.contentSealer != nil {
		if strings.TrimSpace(request.Subject) == "" {
			return eventlog.Envelope{}, errors.New(
				"agent governance: assist content sealing requires a case subject",
			)
		}
		content, marshalErr := json.Marshal(result.content())
		if marshalErr != nil {
			return eventlog.Envelope{}, fmt.Errorf(
				"agent governance: encode assist content: %w", marshalErr,
			)
		}
		completed.SealedContent, err = h.contentSealer.Seal(
			ctx, id, request.Subject, content,
		)
		if err != nil {
			return eventlog.Envelope{}, fmt.Errorf(
				"agent governance: seal assist content: %w", err,
			)
		}
		completed.ContentSubject = request.Subject
		completed.Result.clearContent()
	}
	return h.appendUnique(
		ctx, id, TypeAssistCompleted,
		completed,
		h.claim(id, "assist.complete", result.AssistID),
	)
}

func (h *Handler) FailAssist(
	ctx context.Context,
	id identity.Identity,
	assistID, reason string,
) (eventlog.Envelope, error) {
	return h.failAssist(ctx, id, assistID, reason, "", 0)
}

func (h *Handler) FailClaimedAssist(
	ctx context.Context,
	id identity.Identity,
	assistID, reason, owner string,
	attempt int,
) (eventlog.Envelope, error) {
	return h.failAssist(ctx, id, assistID, reason, owner, attempt)
}

func (h *Handler) failAssist(
	ctx context.Context,
	id identity.Identity,
	assistID, reason, owner string,
	attempt int,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("agent governance: assist failure reason is required")
	}
	if err := rejectTextPII("assist failure reason", reason); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	assist, ok := state.assists[assistID]
	if !ok || (assist.status != AssistRequestedStatus &&
		assist.status != AssistRunningStatus &&
		assist.status != AssistAwaitingApprovalStatus) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist is not awaiting a result",
		)
	}
	if assist.status == AssistRunningStatus &&
		(assist.claim.Owner != owner || assist.claim.Attempt != attempt ||
			!h.now().Before(assist.leaseUntil)) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist failure does not own the active lease",
		)
	}
	if assist.status != AssistRunningStatus && (owner != "" || attempt != 0) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: non-running assist failure cannot claim a worker lease",
		)
	}
	return h.appendUnique(
		ctx, id, TypeAssistFailed,
		AssistFailed{
			AssistID: assistID, Reason: strings.TrimSpace(reason), FailedAt: h.now(),
			ClaimOwner: owner, ClaimAttempt: attempt,
		},
		h.claim(id, "assist.fail", assistID, strconv.Itoa(max(attempt, assist.claim.Attempt))),
	)
}

func (h *Handler) ClaimAssist(
	ctx context.Context,
	id identity.Identity,
	assistID, owner string,
	lease time.Duration,
) (AssistClaimed, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return AssistClaimed{}, eventlog.Envelope{}, err
	}
	if assistID == "" || owner == "" || lease <= 0 {
		return AssistClaimed{}, eventlog.Envelope{}, errors.New(
			"agent governance: assist claim identity and positive lease are required",
		)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return AssistClaimed{}, eventlog.Envelope{}, err
	}
	assist := state.assists[assistID]
	if assist == nil || assist.status != AssistRequestedStatus {
		return AssistClaimed{}, eventlog.Envelope{}, errors.New(
			"agent governance: assist is not available to claim",
		)
	}
	claim := AssistClaimed{
		AssistID: assistID, Owner: owner, Attempt: assist.claim.Attempt + 1,
		LeaseUntil: h.now().Add(lease),
	}
	event, err := h.appendUnique(
		ctx, id, TypeAssistClaimed, claim,
		h.claim(id, "assist.claim", assistID, strconv.Itoa(claim.Attempt)),
	)
	return claim, event, err
}

func (h *Handler) HeartbeatAssist(
	ctx context.Context,
	id identity.Identity,
	assistID, owner string,
	attempt int,
	lease time.Duration,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	assist := state.assists[assistID]
	if assist == nil || assist.status != AssistRunningStatus ||
		assist.claim.Owner != owner || assist.claim.Attempt != attempt ||
		!h.now().Before(assist.leaseUntil) || lease <= 0 {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist heartbeat lost its active lease",
		)
	}
	return h.append(
		ctx, id, TypeAssistHeartbeat, AssistHeartbeat{
			AssistID: assistID, Owner: owner, Attempt: attempt,
			LeaseUntil: h.now().Add(lease),
		},
	)
}

func (h *Handler) DeadLetterExpiredAssist(
	ctx context.Context,
	id identity.Identity,
	assistID string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	assist := state.assists[assistID]
	if assist == nil || assist.status != AssistRunningStatus ||
		h.now().Before(assist.leaseUntil) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist has no expired active lease",
		)
	}
	payload := AssistDeadLettered{
		AssistID: assistID, Attempt: assist.claim.Attempt,
		Reason: "worker lease expired with an indeterminate provider/tool outcome",
		At:     h.now(),
	}
	return h.appendUnique(
		ctx, id, TypeAssistDeadLettered, payload,
		h.claim(id, "assist.dead_letter", assistID, strconv.Itoa(payload.Attempt)),
	)
}

func (h *Handler) RetryAssist(
	ctx context.Context,
	id identity.Identity,
	assistID, reason string,
	acknowledgeAtLeastOnce bool,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist retry reason is required",
		)
	}
	if err := rejectTextPII("assist retry reason", reason); err != nil {
		return eventlog.Envelope{}, err
	}
	if !acknowledgeAtLeastOnce {
		return eventlog.Envelope{}, errors.New(
			"agent governance: retrying an assist requires at-least-once acknowledgement",
		)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < maxClaimRetries; attempt++ {
		state, err := h.fold(ctx, id)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		assist := state.assists[assistID]
		if assist == nil ||
			(assist.status != AssistFailedStatus &&
				assist.status != AssistDeadLetterStatus) {
			return eventlog.Envelope{}, errors.New(
				"agent governance: only a failed or dead-lettered assist can be retried",
			)
		}
		if approval := state.approvalForAssist(assistID); approval != nil &&
			approval.status != ToolApprovalApproved {
			return eventlog.Envelope{}, errors.New(
				"agent governance: a rejected or expired tool request cannot be retried; request a new assist",
			)
		}
		release := state.releases[releaseRef(
			assist.request.TemplateID, assist.request.Release,
		)]
		if release == nil || release.status != ReleaseApproved {
			return eventlog.Envelope{}, errors.New(
				"agent governance: assist retry requires an approved exact release",
			)
		}
		deployment := state.environmentBinding(
			assist.request.TemplateID, assist.request.Environment,
		)
		if deployment == nil || deployment.status != DeploymentActive ||
			deployment.request.Release != assist.request.Release {
			return eventlog.Envelope{}, errors.New(
				"agent governance: assist retry requires the exact active deployment",
			)
		}
		if state.hasOpenCriticalIncident(
			assist.request.TemplateID, assist.request.Release,
		) {
			return eventlog.Envelope{}, errors.New(
				"agent governance: assist retry is blocked by an open critical safety incident",
			)
		}
		assessment := state.circuitAssessment(
			deployment.request, release.spec, h.now(),
		)
		if assessment.Tripped {
			if err := h.openCircuitIncident(
				ctx, id, deployment.request, assessment,
			); err != nil && !errors.Is(err, eventlog.ErrConflict) {
				return eventlog.Envelope{}, err
			}
			return eventlog.Envelope{}, errors.New(
				"agent governance: assist retry is blocked by the release circuit breaker",
			)
		}
		claimParts := []string{
			"assist.retry", assistID, strconv.Itoa(assist.claim.Attempt),
		}
		if release.spec.Budget.Period != "" {
			revision, err := h.admitPeriodBudget(
				ctx, id, state, assist.request, release.spec.Budget,
				deployment.request.DeploymentID,
			)
			if err != nil {
				return eventlog.Envelope{}, err
			}
			claimParts = periodBudgetClaimParts(
				assist.request, release.spec.Budget, h.now(), revision,
			)
		}
		event, err := h.appendUnique(
			ctx, id, TypeAssistRetryRequested, AssistRetryRequested{
				AssistID: assistID, Reason: reason, RequestedBy: id.Actor,
				AcknowledgeAtLeastOnce: acknowledgeAtLeastOnce, At: h.now(),
			},
			h.claim(id, claimParts...),
		)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return event, err
	}
	return eventlog.Envelope{}, errors.New(
		"agent governance: assist retry admission changed concurrently; retry",
	)
}

func (h *Handler) CancelAssist(
	ctx context.Context,
	id identity.Identity,
	assistID, reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist cancellation reason is required",
		)
	}
	if err := rejectTextPII("assist cancellation reason", reason); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	assist := state.assists[assistID]
	if assist == nil ||
		(assist.status != AssistRequestedStatus &&
			assist.status != AssistRunningStatus) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist is not cancellable",
		)
	}
	return h.appendUnique(
		ctx, id, TypeAssistCancelRequested, AssistCancelRequested{
			AssistID: assistID, Reason: reason, RequestedBy: id.Actor, At: h.now(),
		},
		h.claim(id, "assist.cancel", assistID),
	)
}

func (h *Handler) RequestToolApproval(
	ctx context.Context,
	id identity.Identity,
	assistID string,
	needed ToolApprovalNeededError,
	expiresAt time.Time,
) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if strings.TrimSpace(assistID) == "" ||
		strings.TrimSpace(needed.InvocationID) == "" ||
		strings.TrimSpace(needed.CallID) == "" ||
		strings.TrimSpace(needed.Name) == "" ||
		strings.TrimSpace(needed.Purpose) == "" ||
		!validSHA256(needed.ArgumentsHash) ||
		!expiresAt.After(h.now()) {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: valid tool approval lineage and future expiry are required",
		)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	assist := state.assists[assistID]
	if assist == nil ||
		(assist.status != AssistRequestedStatus && assist.status != AssistRunningStatus) {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: assist is not awaiting a tool approval request",
		)
	}
	if needed.InvocationID != assist.request.InvocationID {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: tool approval invocation does not match its assist",
		)
	}
	release := state.releases[releaseRef(assist.request.TemplateID, assist.request.Release)]
	var reviewed *ToolPolicy
	for index := range release.spec.Tools {
		policy := &release.spec.Tools[index]
		if policy.Name == needed.Name {
			reviewed = policy
			break
		}
	}
	if reviewed == nil || reviewed.Mode != ToolHumanBeforeCall ||
		reviewed.Purpose != needed.Purpose {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: requested tool is not human-gated by the exact release",
		)
	}
	approvalID := h.newID()
	payload := ToolApprovalRequested{
		ApprovalID: approvalID, AssistID: assistID,
		InvocationID: needed.InvocationID, CallID: needed.CallID,
		Name: needed.Name, Purpose: needed.Purpose,
		ArgumentsHash: needed.ArgumentsHash, RequestedBy: id.Actor,
		RequestedAt: h.now(), ExpiresAt: expiresAt,
	}
	event, err := h.appendUnique(
		ctx, id, TypeToolApprovalRequested, payload,
		h.claim(id, "tool.approval", assistID),
	)
	return approvalID, event, err
}

func (h *Handler) DecideToolApproval(
	ctx context.Context,
	id identity.Identity,
	approvalID string,
	decision ToolApprovalStatus,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	reason = strings.TrimSpace(reason)
	if (decision != ToolApprovalApproved && decision != ToolApprovalRejected) || reason == "" {
		return eventlog.Envelope{}, errors.New(
			"agent governance: approval or rejection and a reason are required",
		)
	}
	if err := rejectTextPII("tool approval reason", reason); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	approval := state.toolApprovals[approvalID]
	if approval == nil || approval.status != ToolApprovalPending {
		return eventlog.Envelope{}, errors.New(
			"agent governance: tool approval is not pending",
		)
	}
	if !h.now().Before(approval.request.ExpiresAt) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: tool approval window has expired",
		)
	}
	return h.appendUnique(
		ctx, id, TypeToolApprovalDecided,
		ToolApprovalDecided{
			ApprovalID: approvalID, AssistID: approval.request.AssistID,
			Decision: decision, Reason: reason, DecidedBy: id.Actor, DecidedAt: h.now(),
		},
		h.claim(id, "tool.approval.decision", approvalID),
	)
}

func (h *Handler) ExpireToolApproval(
	ctx context.Context,
	id identity.Identity,
	approvalID string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	approval := state.toolApprovals[approvalID]
	if approval == nil || approval.status != ToolApprovalPending ||
		h.now().Before(approval.request.ExpiresAt) {
		return eventlog.Envelope{}, errors.New(
			"agent governance: tool approval is not due for expiry",
		)
	}
	return h.appendUnique(
		ctx, id, TypeToolApprovalExpired,
		ToolApprovalExpired{
			ApprovalID: approvalID, AssistID: approval.request.AssistID, ExpiredAt: h.now(),
		},
		h.claim(id, "tool.approval.expire", approvalID),
	)
}

func (h *Handler) RecordAssistAction(
	ctx context.Context,
	id identity.Identity,
	action ReviewerAction,
) (eventlog.Envelope, error) {
	return h.RecordAssistActionAtEvidence(ctx, id, action, 0)
}

// RecordAssistActionAtEvidence records the reviewer judgment against the
// authoritative current case-evidence head supplied by the case service.
// A zero head is accepted only for direct command callers and is normalized to
// the assist's immutable request snapshot.
func (h *Handler) RecordAssistActionAtEvidence(
	ctx context.Context,
	id identity.Identity,
	action ReviewerAction,
	currentEvidenceSeq uint64,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := action.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	assist, ok := state.assists[action.AssistID]
	if !ok || assist.status != AssistCompletedStatus || assist.acted {
		return eventlog.Envelope{}, errors.New(
			"agent governance: assist is not awaiting reviewer action",
		)
	}
	if currentEvidenceSeq == 0 {
		currentEvidenceSeq = assist.request.EvidenceSeq
	}
	if currentEvidenceSeq < assist.request.EvidenceSeq {
		return eventlog.Envelope{}, errors.New(
			"agent governance: current case evidence predates the assist snapshot",
		)
	}
	action.EvidenceHeadSeq = currentEvidenceSeq
	action.EvidenceStale = currentEvidenceSeq > assist.request.EvidenceSeq
	suggestion, err := h.openAssistSuggestion(ctx, id, assist)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	suggestionHash, err := hashJSON(suggestion)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	var (
		finalHash      string
		differences    []SuggestionDifference
		contentSubject string
		sealedFinal    []byte
	)
	switch action.Action {
	case AssistAccepted:
		finalHash = suggestionHash
	case AssistEdited:
		finalHash, err = hashJSON(action.Final)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		differences, err = suggestionDifferences(suggestion, action.Final)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if h.contentSealer != nil {
			contentSubject = assist.request.Subject
			if strings.TrimSpace(contentSubject) == "" {
				return eventlog.Envelope{}, errors.New(
					"agent governance: edited assist content has no case subject",
				)
			}
			sealedFinal, err = h.contentSealer.Seal(
				ctx, id, contentSubject, action.Final,
			)
			if err != nil {
				return eventlog.Envelope{}, fmt.Errorf(
					"agent governance: seal reviewer-edited assist: %w", err,
				)
			}
			action.Final = nil
		}
	}
	return h.appendUnique(
		ctx, id, TypeAssistReviewerActed,
		AssistReviewerActed{
			Action: action, Actor: id.Actor, ActedAt: h.now(),
			SuggestionHash: suggestionHash, FinalHash: finalHash,
			Differences: differences, ContentSubject: contentSubject,
			SealedFinal: sealedFinal,
		},
		h.claim(id, "assist.action", action.AssistID),
	)
}

func (h *Handler) openAssistSuggestion(
	ctx context.Context,
	id identity.Identity,
	assist *assistState,
) (json.RawMessage, error) {
	if assist.result == nil {
		return nil, errors.New("agent governance: completed assist has no result")
	}
	if len(assist.sealedContent) == 0 {
		if err := validateJSONObject(
			"assist suggestion", assist.result.Suggestion,
		); err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), assist.result.Suggestion...), nil
	}
	if h.contentSealer == nil {
		return nil, errors.New(
			"agent governance: sealed assist content has no configured content sealer",
		)
	}
	plain, err := h.contentSealer.Open(
		ctx, id, assist.contentSubject, assist.sealedContent,
	)
	if err != nil {
		return nil, fmt.Errorf("agent governance: open assist for reviewer action: %w", err)
	}
	var content AssistContent
	if err := json.Unmarshal(plain, &content); err != nil {
		return nil, fmt.Errorf(
			"agent governance: decode assist for reviewer action: %w", err,
		)
	}
	if err := validateJSONObject("assist suggestion", content.Suggestion); err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), content.Suggestion...), nil
}

func (h *Handler) OpenSafetyIncident(
	ctx context.Context,
	id identity.Identity,
	incident SafetyIncidentOpened,
) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if incident.IncidentID == "" {
		incident.IncidentID = h.newID()
	}
	incident.OpenedAt = h.now()
	if incident.TemplateID == "" || incident.Release < 1 || incident.Kind == "" ||
		!incident.Severity.Valid() || strings.TrimSpace(incident.Summary) == "" {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: safety incident lineage, kind, severity, and summary are required",
		)
	}
	if err := rejectTextPII(
		"safety incident text", incident.Kind, incident.Summary,
	); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := rejectJSONPII("safety incident evidence", incident.Evidence); err != nil {
		return "", eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if _, ok := state.releases[releaseRef(incident.TemplateID, incident.Release)]; !ok {
		return "", eventlog.Envelope{}, errors.New(
			"agent governance: safety incident references unknown release",
		)
	}
	event, err := h.appendUnique(
		ctx, id, TypeSafetyIncidentOpened, incident,
		h.claim(id, "incident", incident.IncidentID),
	)
	return incident.IncidentID, event, err
}

func (h *Handler) ResolveSafetyIncident(
	ctx context.Context,
	id identity.Identity,
	incidentID, resolution string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(resolution) == "" {
		return eventlog.Envelope{}, errors.New(
			"agent governance: safety incident resolution is required",
		)
	}
	if err := rejectTextPII("safety incident resolution", resolution); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	incident, ok := state.incidents[incidentID]
	if !ok || !incident.open {
		return eventlog.Envelope{}, errors.New(
			"agent governance: safety incident is not open",
		)
	}
	return h.appendUnique(
		ctx, id, TypeSafetyIncidentResolved,
		SafetyIncidentResolved{
			IncidentID: incidentID, Resolution: strings.TrimSpace(resolution),
			ResolvedAt: h.now(),
		},
		h.claim(id, "incident.resolve", incidentID),
	)
}

const circuitBreakerIncidentKind = "circuit_breaker"

type circuitAssessment struct {
	Tripped      bool
	Samples      int
	Failures     int
	ObservedRate float64
	Threshold    float64
	WindowStart  time.Time
	WindowEnd    time.Time
	TerminalSeq  uint64
}

// EvaluateDeploymentCircuit derives the reviewed failure-rate policy from the
// tenant event stream and latches a critical incident exactly once for the
// newest terminal sample. It is safe to call from multiple scheduler replicas.
func (h *Handler) EvaluateDeploymentCircuit(
	ctx context.Context,
	id identity.Identity,
	deploymentID string,
) (bool, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return false, eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return false, eventlog.Envelope{}, err
	}
	deployment := state.deployments[deploymentID]
	if deployment == nil ||
		(deployment.status != DeploymentScheduled &&
			deployment.status != DeploymentActive) {
		return false, eventlog.Envelope{}, nil
	}
	release := state.releases[releaseRef(
		deployment.request.TemplateID, deployment.request.Release,
	)]
	if release == nil {
		return false, eventlog.Envelope{}, errors.New(
			"agent governance: deployment references unknown release",
		)
	}
	if state.hasOpenCircuitIncident(deploymentID) {
		return false, eventlog.Envelope{}, nil
	}
	assessment := state.circuitAssessment(deployment.request, release.spec, h.now())
	if !assessment.Tripped {
		return false, eventlog.Envelope{}, nil
	}
	event, err := h.appendCircuitIncident(ctx, id, deployment.request, assessment)
	if errors.Is(err, eventlog.ErrConflict) {
		return false, eventlog.Envelope{}, nil
	}
	if err != nil {
		return false, eventlog.Envelope{}, err
	}
	return true, event, nil
}

func (h *Handler) openCircuitIncident(
	ctx context.Context,
	id identity.Identity,
	deployment DeploymentRequest,
	assessment circuitAssessment,
) error {
	_, err := h.appendCircuitIncident(ctx, id, deployment, assessment)
	return err
}

func (h *Handler) appendCircuitIncident(
	ctx context.Context,
	id identity.Identity,
	deployment DeploymentRequest,
	assessment circuitAssessment,
) (eventlog.Envelope, error) {
	evidence, err := json.Marshal(struct {
		Environment  engine.Environment `json:"environment"`
		WindowStart  time.Time          `json:"window_start"`
		WindowEnd    time.Time          `json:"window_end"`
		Samples      int                `json:"samples"`
		Failures     int                `json:"failures"`
		ObservedRate float64            `json:"observed_failure_rate"`
		Threshold    float64            `json:"threshold"`
		TerminalSeq  uint64             `json:"terminal_seq"`
	}{
		Environment: deployment.Environment, WindowStart: assessment.WindowStart,
		WindowEnd: assessment.WindowEnd, Samples: assessment.Samples,
		Failures: assessment.Failures, ObservedRate: assessment.ObservedRate,
		Threshold: assessment.Threshold, TerminalSeq: assessment.TerminalSeq,
	})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf(
			"agent governance: encode circuit breaker evidence: %w", err,
		)
	}
	return h.appendUnique(
		ctx, id, TypeSafetyIncidentOpened,
		SafetyIncidentOpened{
			IncidentID: h.newID(), TemplateID: deployment.TemplateID,
			Release: deployment.Release, DeploymentID: deployment.DeploymentID,
			Kind: circuitBreakerIncidentKind, Severity: SeverityCritical,
			Summary:  "Governed agent execution failure-rate circuit opened",
			Evidence: evidence, OpenedAt: h.now(),
		},
		h.claim(
			id, "circuit.open", deployment.DeploymentID,
			strconv.FormatUint(assessment.TerminalSeq, 10),
		),
	)
}

func (h *Handler) ReleaseSnapshot(
	ctx context.Context,
	id identity.Identity,
	templateID string,
	release int,
) (ReleaseView, error) {
	if err := id.Valid(); err != nil {
		return ReleaseView{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return ReleaseView{}, err
	}
	current, ok := state.releases[releaseRef(templateID, release)]
	if !ok {
		return ReleaseView{}, fmt.Errorf(
			"agent governance: unknown release %s@%d", templateID, release,
		)
	}
	return ReleaseView{
		Org: id.Org, Workspace: id.Workspace, TemplateID: templateID,
		Release: release, Status: current.status, Spec: current.spec,
		SpecHash: current.specHash, CreatedBy: current.createdBy,
	}, nil
}

func (h *Handler) SuiteSnapshot(
	ctx context.Context,
	id identity.Identity,
	suiteID string,
	version int,
) (EvalSuite, error) {
	if err := id.Valid(); err != nil {
		return EvalSuite{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return EvalSuite{}, err
	}
	suite, ok := state.suites[suiteRef(suiteID, version)]
	if !ok {
		return EvalSuite{}, fmt.Errorf(
			"agent governance: unknown evaluation suite %s@%d", suiteID, version,
		)
	}
	return suite, nil
}

func (h *Handler) AssistStatus(
	ctx context.Context,
	id identity.Identity,
	assistID string,
) (AssistStatus, bool, error) {
	if err := id.Valid(); err != nil {
		return "", false, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return "", false, err
	}
	assist, found := state.assists[assistID]
	if !found {
		return "", false, nil
	}
	return assist.status, true, nil
}

func (h *Handler) AssistSnapshot(
	ctx context.Context,
	id identity.Identity,
	assistID string,
) (AssistRequested, error) {
	if err := id.Valid(); err != nil {
		return AssistRequested{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return AssistRequested{}, err
	}
	assist := state.assists[assistID]
	if assist == nil {
		return AssistRequested{}, fmt.Errorf(
			"agent governance: unknown assist %q", assistID,
		)
	}
	return assist.request, nil
}

type assistWork struct {
	request    AssistRequested
	status     AssistStatus
	claim      AssistClaimed
	leaseUntil time.Time
}

func (h *Handler) assistWorkSnapshot(
	ctx context.Context,
	id identity.Identity,
	assistID string,
) (assistWork, bool, error) {
	if err := id.Valid(); err != nil {
		return assistWork{}, false, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return assistWork{}, false, err
	}
	assist := state.assists[assistID]
	if assist == nil {
		return assistWork{}, false, nil
	}
	return assistWork{
		request: assist.request, status: assist.status,
		claim: assist.claim, leaseUntil: assist.leaseUntil,
	}, true, nil
}

func (h *Handler) ToolApprovalSnapshot(
	ctx context.Context,
	id identity.Identity,
	approvalID string,
) (ToolApprovalRequested, ToolApprovalStatus, string, error) {
	if err := id.Valid(); err != nil {
		return ToolApprovalRequested{}, "", "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return ToolApprovalRequested{}, "", "", err
	}
	approval := state.toolApprovals[approvalID]
	if approval == nil {
		return ToolApprovalRequested{}, "", "", fmt.Errorf(
			"agent governance: unknown tool approval %q", approvalID,
		)
	}
	return approval.request, approval.status, approval.decidedBy, nil
}

func (h *Handler) ApprovedToolCallSnapshot(
	ctx context.Context,
	id identity.Identity,
	assistID string,
) (ApprovedToolCall, bool, error) {
	if err := id.Valid(); err != nil {
		return ApprovedToolCall{}, false, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.fold(ctx, id)
	if err != nil {
		return ApprovedToolCall{}, false, err
	}
	assist := state.assists[assistID]
	if assist == nil {
		return ApprovedToolCall{}, false, fmt.Errorf(
			"agent governance: unknown assist %q", assistID,
		)
	}
	approval := state.approvalForAssist(assistID)
	if approval == nil || approval.status != ToolApprovalApproved {
		return ApprovedToolCall{}, false, nil
	}
	if approval.request.InvocationID != assist.request.InvocationID {
		return ApprovedToolCall{}, false, errors.New(
			"agent governance: approved tool call has stale invocation lineage",
		)
	}
	return ApprovedToolCall{
		ApprovalID:    approval.request.ApprovalID,
		InvocationID:  approval.request.InvocationID,
		CallID:        approval.request.CallID,
		Name:          approval.request.Name,
		ArgumentsHash: approval.request.ArgumentsHash,
		ApprovedBy:    approval.decidedBy,
	}, true, nil
}

type aggregate struct {
	templates       map[string]Template
	releases        map[string]*releaseState
	suites          map[string]EvalSuite
	campaigns       map[string]*campaignState
	deployments     map[string]*deploymentState
	assists         map[string]*assistState
	toolApprovals   map[string]*toolApprovalState
	incidents       map[string]*incidentState
	circuitOutcomes []circuitOutcome
}

type releaseState struct {
	status         ReleaseStatus
	createdBy      string
	spec           ReleaseSpec
	specHash       string
	review         *ReleaseReviewRequestedEvent
	reviewRevision int
}

type deploymentState struct {
	request  DeploymentRequest
	status   DeploymentStatus
	revision int
}

type incidentState struct {
	templateID   string
	release      int
	deploymentID string
	kind         string
	severity     Severity
	open         bool
	resolvedAt   time.Time
	resolvedSeq  uint64
}

type circuitOutcome struct {
	templateID  string
	release     int
	environment engine.Environment
	at          time.Time
	failed      bool
	seq         uint64
}

type campaignState struct {
	result        CampaignResult
	recordedBy    string
	adjudications []TrialAdjudication
}

type assistState struct {
	request        AssistRequested
	envelope       eventlog.Envelope
	status         AssistStatus
	result         *AssistResult
	contentSubject string
	sealedContent  []byte
	completedAt    time.Time
	acted          bool
	claim          AssistClaimed
	leaseUntil     time.Time
	retryCount     int
}

type toolApprovalState struct {
	request   ToolApprovalRequested
	status    ToolApprovalStatus
	decidedBy string
}

func newAggregate() aggregate {
	return aggregate{
		templates: map[string]Template{}, releases: map[string]*releaseState{},
		suites: map[string]EvalSuite{}, campaigns: map[string]*campaignState{},
		deployments: map[string]*deploymentState{}, assists: map[string]*assistState{},
		toolApprovals:   map[string]*toolApprovalState{},
		incidents:       map[string]*incidentState{},
		circuitOutcomes: []circuitOutcome{},
	}
}

func (h *Handler) fold(ctx context.Context, id identity.Identity) (aggregate, error) {
	events, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, Stream, 0)
	if err != nil {
		return aggregate{}, fmt.Errorf("agent governance: read stream: %w", err)
	}
	state := newAggregate()
	for _, event := range events {
		if err := state.apply(event); err != nil {
			return aggregate{}, fmt.Errorf(
				"agent governance: fold %s at seq %d: %w", event.Type, event.Seq, err,
			)
		}
	}
	return state, nil
}

func (s *aggregate) apply(event eventlog.Envelope) error {
	switch event.Type {
	case TypeTemplateRegistered:
		var payload TemplateRegistered
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if err := payload.Template.Validate(); err != nil {
			return err
		}
		if _, exists := s.templates[payload.Template.TemplateID]; exists {
			return fmt.Errorf("duplicate template %q", payload.Template.TemplateID)
		}
		s.templates[payload.Template.TemplateID] = payload.Template
	case TypeReleaseCreated:
		var payload ReleaseCreated
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if _, exists := s.templates[payload.TemplateID]; !exists {
			return fmt.Errorf("release references unknown template %q", payload.TemplateID)
		}
		if payload.Release != s.latestRelease(payload.TemplateID)+1 {
			return fmt.Errorf("release %d is not the next version", payload.Release)
		}
		if err := payload.Spec.Validate(); err != nil {
			return err
		}
		specHash, err := hashJSON(payload.Spec)
		if err != nil {
			return err
		}
		if payload.SpecHash != specHash {
			return errors.New("release spec hash does not match immutable spec")
		}
		s.releases[releaseRef(payload.TemplateID, payload.Release)] = &releaseState{
			status: ReleaseDraft, createdBy: payload.CreatedBy, spec: payload.Spec,
			specHash: payload.SpecHash,
		}
	case TypeSuitePublished:
		var payload SuitePublished
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if err := payload.Suite.Validate(); err != nil {
			return err
		}
		if _, exists := s.suites[suiteRef(payload.Suite.SuiteID, payload.Suite.Version)]; exists {
			return fmt.Errorf(
				"duplicate evaluation suite %s@%d", payload.Suite.SuiteID, payload.Suite.Version,
			)
		}
		s.suites[suiteRef(payload.Suite.SuiteID, payload.Suite.Version)] = payload.Suite
	case TypeCampaignRecorded:
		var payload CampaignRecorded
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if err := payload.Result.Validate(); err != nil {
			return err
		}
		release := s.releases[releaseRef(payload.Result.TemplateID, payload.Result.Release)]
		if release == nil {
			return errors.New("campaign references unknown release")
		}
		if release.status != ReleaseDraft && release.status != ReleaseEvaluated {
			return fmt.Errorf("campaign cannot change %s release", release.status)
		}
		suite, exists := s.suites[suiteRef(payload.Result.SuiteID, payload.Result.SuiteVersion)]
		if !exists {
			return errors.New("campaign references unknown suite")
		}
		derived, err := BuildCampaign(
			payload.Result.CampaignID, payload.Result.TemplateID, payload.Result.Release,
			suite, payload.Result.Trials,
		)
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(derived)
		actual, _ := json.Marshal(payload.Result)
		if !bytes.Equal(expected, actual) {
			return errors.New("campaign aggregates do not match trial evidence")
		}
		if _, exists := s.campaigns[payload.Result.CampaignID]; exists {
			return fmt.Errorf("duplicate campaign %q", payload.Result.CampaignID)
		}
		s.campaigns[payload.Result.CampaignID] = &campaignState{
			result: payload.Result, recordedBy: payload.RecordedBy,
		}
		release.status = ReleaseEvaluated
	case TypeCampaignTrialAdjudicated:
		var payload CampaignTrialAdjudicated
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if err := payload.Adjudication.Validate(); err != nil {
			return err
		}
		campaign := s.campaigns[payload.Adjudication.CampaignID]
		if campaign == nil {
			return errors.New("trial adjudication references unknown campaign")
		}
		if payload.TemplateID != campaign.result.TemplateID ||
			payload.Release != campaign.result.Release {
			return errors.New("trial adjudication release lineage is invalid")
		}
		release := s.releases[releaseRef(
			campaign.result.TemplateID, campaign.result.Release,
		)]
		if release == nil || release.status != ReleaseEvaluated ||
			campaign.recordedBy == payload.Adjudication.AdjudicatedBy {
			return errors.New("trial adjudication violates release or independence")
		}
		found := false
		for _, trial := range campaign.result.Trials {
			if trial.CaseID == payload.Adjudication.CaseID &&
				trial.Trial == payload.Adjudication.Trial {
				found = true
				break
			}
		}
		if !found {
			return errors.New("trial adjudication references unknown trial")
		}
		for _, prior := range campaign.adjudications {
			if prior.CaseID == payload.Adjudication.CaseID &&
				prior.Trial == payload.Adjudication.Trial {
				return errors.New("trial was adjudicated more than once")
			}
		}
		suite := s.suites[suiteRef(
			campaign.result.SuiteID, campaign.result.SuiteVersion,
		)]
		previousAssessment, err := AssessCampaign(
			campaign.result, suite, campaign.adjudications,
		)
		if err != nil {
			return err
		}
		expectedPrevious, _ := json.Marshal(previousAssessment)
		recordedPrevious, _ := json.Marshal(payload.PreviousAssessment)
		if !bytes.Equal(expectedPrevious, recordedPrevious) {
			return errors.New("trial adjudication previous assessment is not reproducible")
		}
		nextAdjudications := append(
			append([]TrialAdjudication(nil), campaign.adjudications...),
			payload.Adjudication,
		)
		assessment, err := AssessCampaign(
			campaign.result, suite, nextAdjudications,
		)
		if err != nil {
			return err
		}
		expectedAssessment, _ := json.Marshal(assessment)
		recordedAssessment, _ := json.Marshal(payload.Assessment)
		if !bytes.Equal(expectedAssessment, recordedAssessment) {
			return errors.New("trial adjudication assessment is not reproducible")
		}
		campaign.adjudications = append(
			campaign.adjudications, payload.Adjudication,
		)
	case TypeReleaseReviewRequested:
		var payload ReleaseReviewRequestedEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		release := s.releases[releaseRef(payload.TemplateID, payload.Release)]
		if release == nil || release.status != ReleaseEvaluated {
			return errors.New("review request references no evaluated release")
		}
		if payload.RequestID == "" || !payload.ExpiresAt.After(payload.RequestedAt) {
			return errors.New("review request identity or expiry is invalid")
		}
		release.status, release.review = ReleaseReviewRequested, &payload
		release.reviewRevision++
	case TypeReleaseReviewed:
		var payload ReleaseReviewed
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		release := s.releases[releaseRef(payload.TemplateID, payload.Release)]
		if release == nil || release.status != ReleaseReviewRequested || release.review == nil ||
			release.review.RequestID != payload.RequestID {
			return errors.New("review decision references no open request")
		}
		if !payload.Decision.Valid() || strings.TrimSpace(payload.Reason) == "" {
			return errors.New("review decision is invalid")
		}
		if payload.Decision == ReviewApprove {
			if payload.ReviewedBy == release.createdBy ||
				payload.ReviewedBy == release.review.RequestedBy {
				return errors.New("review decision violates four-eyes")
			}
			if len(release.review.Reviewers) > 0 &&
				!contains(release.review.Reviewers, payload.ReviewedBy) {
				return errors.New("review decision actor was not assigned")
			}
			if !payload.ReviewedAt.Before(release.review.ExpiresAt) {
				return errors.New("review decision occurred after expiry")
			}
			release.status = ReleaseApproved
		} else {
			release.status = ReleaseRejected
		}
	case TypeReleaseReviewExpired:
		var payload ReleaseReviewExpired
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		release := s.releases[releaseRef(payload.TemplateID, payload.Release)]
		if release == nil || release.status != ReleaseReviewRequested || release.review == nil ||
			release.review.RequestID != payload.RequestID ||
			payload.ExpiredAt.Before(release.review.ExpiresAt) {
			return errors.New("review expiry has stale lineage")
		}
		release.status = ReleaseEvaluated
	case TypeReleaseRetired:
		var payload ReleaseRetiredEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		release := s.releases[releaseRef(payload.TemplateID, payload.Release)]
		if release == nil || release.status == ReleaseRetired {
			return errors.New("retirement references no active release")
		}
		release.status = ReleaseRetired
	case TypeDeploymentRequested:
		var payload DeploymentRequestedEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if err := payload.Request.Validate(); err != nil {
			return err
		}
		release := s.releases[releaseRef(payload.Request.TemplateID, payload.Request.Release)]
		if release == nil || release.status != ReleaseApproved {
			return errors.New("deployment references no approved release")
		}
		if _, exists := s.deployments[payload.Request.DeploymentID]; exists {
			return fmt.Errorf("duplicate deployment %q", payload.Request.DeploymentID)
		}
		if s.environmentBinding(payload.Request.TemplateID, payload.Request.Environment) != nil {
			return errors.New("deployment environment already bound")
		}
		s.deployments[payload.Request.DeploymentID] = &deploymentState{
			request: payload.Request, status: DeploymentScheduled, revision: 1,
		}
	case TypeDeploymentActivated:
		var payload DeploymentActivatedEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		deployment := s.deployments[payload.DeploymentID]
		if deployment == nil || deployment.status != DeploymentScheduled ||
			deployment.request.TemplateID != payload.TemplateID ||
			deployment.request.Release != payload.Release ||
			deployment.request.Environment != payload.Environment {
			return errors.New("activation has stale deployment lineage")
		}
		deployment.status = DeploymentActive
		deployment.revision++
	case TypeDeploymentPaused:
		var payload DeploymentPausedEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		deployment := s.deployments[payload.DeploymentID]
		if deployment == nil ||
			(deployment.status != DeploymentScheduled && deployment.status != DeploymentActive) {
			return errors.New("pause references no scheduled or active deployment")
		}
		deployment.status = DeploymentPaused
		deployment.revision++
	case TypeDeploymentResumed:
		var payload DeploymentResumedEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		deployment := s.deployments[payload.DeploymentID]
		if deployment == nil || deployment.status != DeploymentPaused ||
			strings.TrimSpace(payload.Reason) == "" ||
			strings.TrimSpace(payload.ResumedBy) == "" {
			return errors.New("resume references no paused deployment")
		}
		deployment.status = DeploymentActive
		deployment.revision++
	case TypeDeploymentRolledBack:
		var payload DeploymentRolledBackEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		deployment := s.deployments[payload.DeploymentID]
		target := s.releases[releaseRef(deploymentTemplate(deployment), payload.ToRelease)]
		if deployment == nil || deployment.status != DeploymentActive ||
			deployment.request.Release != payload.FromRelease ||
			target == nil || target.status != ReleaseApproved {
			return errors.New("rollback has stale or unapproved lineage")
		}
		deployment.request.Release = payload.ToRelease
		deployment.status = DeploymentActive
		deployment.revision++
	case TypeAssistRequested:
		var payload AssistRequested
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if _, exists := s.assists[payload.AssistID]; exists {
			return fmt.Errorf("duplicate assist %q", payload.AssistID)
		}
		if payload.AssistID == "" || payload.CaseID == "" || !payload.Kind.Valid() ||
			payload.TemplateID == "" || payload.Release < 1 ||
			!engine.ValidEnvironment(string(payload.Environment)) || payload.EvidenceSeq < 1 ||
			len(payload.EvidenceIDs) == 0 || payload.InvocationID == "" ||
			((payload.InputSubject == "") != (len(payload.SealedInput) == 0)) ||
			(payload.InputSubject != "" && payload.InputSubject != payload.Subject) {
			return errors.New("assist request lineage is invalid")
		}
		if payload.PolicySource != nil {
			if err := payload.PolicySource.Validate(); err != nil {
				return err
			}
		}
		s.assists[payload.AssistID] = &assistState{
			request: payload, envelope: event, status: AssistRequestedStatus,
		}
	case TypeAssistClaimed:
		var payload AssistClaimed
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		assist := s.assists[payload.AssistID]
		if assist == nil || assist.status != AssistRequestedStatus ||
			payload.Owner == "" || payload.Attempt <= assist.claim.Attempt ||
			!payload.LeaseUntil.After(event.Time) {
			return errors.New("assist claim is invalid or stale")
		}
		assist.status, assist.claim = AssistRunningStatus, payload
		assist.leaseUntil = payload.LeaseUntil
	case TypeAssistHeartbeat:
		var payload AssistHeartbeat
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		assist := s.assists[payload.AssistID]
		if assist == nil || assist.status != AssistRunningStatus ||
			assist.claim.Owner != payload.Owner ||
			assist.claim.Attempt != payload.Attempt ||
			!payload.LeaseUntil.After(assist.leaseUntil) {
			return errors.New("assist heartbeat does not own the active lease")
		}
		assist.leaseUntil = payload.LeaseUntil
	case TypeAssistCompleted:
		var payload AssistCompleted
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		assist := s.assists[payload.Result.AssistID]
		if assist == nil {
			return errors.New("assist completion references no pending request")
		}
		if assist.status == AssistRunningStatus {
			if assist.claim.Owner != payload.ClaimOwner ||
				assist.claim.Attempt != payload.ClaimAttempt ||
				!payload.CompletedAt.Before(assist.leaseUntil) {
				return errors.New("assist completion does not own its active lease")
			}
		} else if assist.status != AssistRequestedStatus &&
			assist.status != AssistAwaitingApprovalStatus {
			return errors.New("assist completion references no pending request")
		}
		switch {
		case len(payload.SealedContent) > 0:
			if payload.ContentSubject == "" || payload.ContentSubject != assist.request.Subject {
				return errors.New("sealed assist content has invalid subject lineage")
			}
			if len(payload.Result.Suggestion) > 0 || len(payload.Result.Citations) > 0 ||
				len(payload.Result.Unsupported) > 0 || len(payload.Result.Limitations) > 0 {
				return errors.New("sealed assist event also contains cleartext content")
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
		assist.status, assist.result, assist.completedAt =
			AssistCompletedStatus, &payload.Result, payload.CompletedAt
		assist.contentSubject = payload.ContentSubject
		assist.sealedContent = append([]byte(nil), payload.SealedContent...)
		s.circuitOutcomes = append(s.circuitOutcomes, circuitOutcome{
			templateID: assist.request.TemplateID, release: assist.request.Release,
			environment: assist.request.Environment, at: payload.CompletedAt, seq: event.Seq,
		})
	case TypeAssistFailed:
		var payload AssistFailed
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		assist := s.assists[payload.AssistID]
		if assist == nil || strings.TrimSpace(payload.Reason) == "" {
			return errors.New("assist failure references no pending request")
		}
		if assist.status == AssistRunningStatus {
			if assist.claim.Owner != payload.ClaimOwner ||
				assist.claim.Attempt != payload.ClaimAttempt ||
				!payload.FailedAt.Before(assist.leaseUntil) {
				return errors.New("assist failure does not own its active lease")
			}
		} else if assist.status != AssistRequestedStatus &&
			assist.status != AssistAwaitingApprovalStatus {
			return errors.New("assist failure references no pending request")
		}
		assist.status, assist.completedAt = AssistFailedStatus, payload.FailedAt
		s.circuitOutcomes = append(s.circuitOutcomes, circuitOutcome{
			templateID: assist.request.TemplateID, release: assist.request.Release,
			environment: assist.request.Environment, at: payload.FailedAt,
			failed: true, seq: event.Seq,
		})
	case TypeAssistDeadLettered:
		var payload AssistDeadLettered
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		assist := s.assists[payload.AssistID]
		if assist == nil || assist.status != AssistRunningStatus ||
			assist.claim.Attempt != payload.Attempt || payload.Reason == "" ||
			payload.At.Before(assist.leaseUntil) {
			return errors.New("assist dead letter does not match an expired claim")
		}
		assist.status, assist.completedAt = AssistDeadLetterStatus, payload.At
		s.circuitOutcomes = append(s.circuitOutcomes, circuitOutcome{
			templateID: assist.request.TemplateID, release: assist.request.Release,
			environment: assist.request.Environment, at: payload.At,
			failed: true, seq: event.Seq,
		})
	case TypeAssistRetryRequested:
		var payload AssistRetryRequested
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		assist := s.assists[payload.AssistID]
		if assist == nil ||
			(assist.status != AssistFailedStatus &&
				assist.status != AssistDeadLetterStatus) ||
			payload.Reason == "" || payload.RequestedBy == "" ||
			(assist.status == AssistDeadLetterStatus &&
				!payload.AcknowledgeAtLeastOnce) {
			return errors.New("assist retry is invalid")
		}
		assist.status = AssistRequestedStatus
		assist.retryCount++
	case TypeAssistCancelRequested:
		var payload AssistCancelRequested
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		assist := s.assists[payload.AssistID]
		if assist == nil ||
			(assist.status != AssistRequestedStatus &&
				assist.status != AssistRunningStatus) ||
			payload.Reason == "" || payload.RequestedBy == "" {
			return errors.New("assist cancellation is invalid")
		}
		assist.status = AssistCancelledStatus
	case TypeToolApprovalRequested:
		var payload ToolApprovalRequested
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		assist := s.assists[payload.AssistID]
		if assist == nil ||
			(assist.status != AssistRequestedStatus &&
				assist.status != AssistRunningStatus) ||
			payload.ApprovalID == "" || payload.InvocationID == "" ||
			payload.CallID == "" || payload.Name == "" || payload.Purpose == "" ||
			!validSHA256(payload.ArgumentsHash) ||
			!payload.ExpiresAt.After(payload.RequestedAt) {
			return errors.New("tool approval request has invalid lineage")
		}
		if _, exists := s.toolApprovals[payload.ApprovalID]; exists ||
			s.approvalForAssist(payload.AssistID) != nil {
			return errors.New("duplicate tool approval request")
		}
		release := s.releases[releaseRef(assist.request.TemplateID, assist.request.Release)]
		matchesPolicy := false
		for _, policy := range release.spec.Tools {
			if policy.Name == payload.Name &&
				policy.Mode == ToolHumanBeforeCall &&
				policy.Purpose == payload.Purpose {
				matchesPolicy = true
				break
			}
		}
		if !matchesPolicy {
			return errors.New("tool approval request is outside the reviewed release")
		}
		s.toolApprovals[payload.ApprovalID] = &toolApprovalState{
			request: payload, status: ToolApprovalPending,
		}
		assist.status = AssistAwaitingApprovalStatus
	case TypeToolApprovalDecided:
		var payload ToolApprovalDecided
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		approval := s.toolApprovals[payload.ApprovalID]
		if approval == nil || approval.status != ToolApprovalPending ||
			approval.request.AssistID != payload.AssistID ||
			(payload.Decision != ToolApprovalApproved &&
				payload.Decision != ToolApprovalRejected) ||
			strings.TrimSpace(payload.Reason) == "" ||
			strings.TrimSpace(payload.DecidedBy) == "" ||
			!payload.DecidedAt.Before(approval.request.ExpiresAt) {
			return errors.New("tool approval decision has invalid lineage")
		}
		approval.status, approval.decidedBy = payload.Decision, payload.DecidedBy
		if payload.Decision == ToolApprovalRejected {
			s.assists[payload.AssistID].status = AssistFailedStatus
		} else {
			s.assists[payload.AssistID].status = AssistRequestedStatus
		}
	case TypeToolApprovalExpired:
		var payload ToolApprovalExpired
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		approval := s.toolApprovals[payload.ApprovalID]
		if approval == nil || approval.status != ToolApprovalPending ||
			approval.request.AssistID != payload.AssistID ||
			payload.ExpiredAt.Before(approval.request.ExpiresAt) {
			return errors.New("tool approval expiry has invalid lineage")
		}
		approval.status = ToolApprovalExpiredStatus
		s.assists[payload.AssistID].status = AssistFailedStatus
	case TypeAssistReviewerActed:
		var payload AssistReviewerActed
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		assist := s.assists[payload.Action.AssistID]
		if assist == nil || assist.status != AssistCompletedStatus || assist.acted {
			return errors.New("assist action references no completed unreviewed assist")
		}
		if err := payload.Validate(assist.request); err != nil {
			return err
		}
		assist.acted = true
	case TypeSafetyIncidentOpened:
		var payload SafetyIncidentOpened
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if payload.IncidentID == "" || payload.TemplateID == "" || payload.Release < 1 ||
			!payload.Severity.Valid() || strings.TrimSpace(payload.Summary) == "" {
			return errors.New("safety incident is incomplete")
		}
		if s.releases[releaseRef(payload.TemplateID, payload.Release)] == nil {
			return errors.New("safety incident references unknown release")
		}
		if _, exists := s.incidents[payload.IncidentID]; exists {
			return fmt.Errorf("duplicate safety incident %q", payload.IncidentID)
		}
		s.incidents[payload.IncidentID] = &incidentState{
			templateID: payload.TemplateID, release: payload.Release,
			deploymentID: payload.DeploymentID, kind: payload.Kind,
			severity: payload.Severity, open: true,
		}
	case TypeSafetyIncidentResolved:
		var payload SafetyIncidentResolved
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		incident := s.incidents[payload.IncidentID]
		if incident == nil || !incident.open || strings.TrimSpace(payload.Resolution) == "" {
			return errors.New("resolution references no open incident")
		}
		incident.open, incident.resolvedAt = false, payload.ResolvedAt
		incident.resolvedSeq = event.Seq
	}
	return nil
}

func (s aggregate) periodBudgetUsage(
	request AssistRequested,
	budget Budget,
	now time.Time,
) (float64, int) {
	start := budgetPeriodStart(now, budget.Period)
	var consumed float64
	revision := 0
	for _, assist := range s.assists {
		if assist.request.TemplateID != request.TemplateID ||
			assist.request.Release != request.Release ||
			assist.request.Environment != request.Environment ||
			assist.request.RequestedAt.Before(start) {
			continue
		}
		revision += 1 + assist.retryCount
		consumed += float64(assist.retryCount) * budget.MaxCostUSD
		switch {
		case assist.status == AssistRequestedStatus ||
			assist.status == AssistRunningStatus ||
			assist.status == AssistAwaitingApprovalStatus:
			consumed += budget.MaxCostUSD
		case assist.status == AssistCompletedStatus && assist.result != nil:
			consumed += assist.result.CostUSD
		default:
			// A failed, dead-lettered, or cancelled attempt may already have
			// incurred provider cost or an external effect. Reserve its reviewed
			// maximum because the platform has no trustworthy lower amount.
			consumed += budget.MaxCostUSD
		}
	}
	return consumed, revision
}

func (h *Handler) admitPeriodBudget(
	ctx context.Context,
	id identity.Identity,
	state aggregate,
	request AssistRequested,
	budget Budget,
	deploymentID string,
) (int, error) {
	consumed, revision := state.periodBudgetUsage(request, budget, h.now())
	if consumed+budget.MaxCostUSD <= budget.PeriodCostUSD {
		return revision, nil
	}
	evidence, err := json.Marshal(map[string]any{
		"period":                budget.Period,
		"reserved_or_spent_usd": consumed,
		"requested_max_usd":     budget.MaxCostUSD,
		"period_limit_usd":      budget.PeriodCostUSD,
	})
	if err != nil {
		return 0, fmt.Errorf("agent governance: encode budget incident: %w", err)
	}
	start := budgetPeriodStart(h.now(), budget.Period)
	_, incidentErr := h.appendUnique(
		ctx, id, TypeSafetyIncidentOpened,
		SafetyIncidentOpened{
			IncidentID: h.newID(), TemplateID: request.TemplateID,
			Release: request.Release, DeploymentID: deploymentID,
			Kind: "budget_exhaustion", Severity: SeverityWarning,
			Summary:  "Governed agent period budget exhausted",
			Evidence: evidence, OpenedAt: h.now(),
		},
		h.claim(
			id, "budget.exhausted", request.TemplateID,
			strconv.Itoa(request.Release), string(request.Environment),
			budget.Period, start.Format(time.RFC3339),
		),
	)
	if incidentErr != nil && !errors.Is(incidentErr, eventlog.ErrConflict) {
		return 0, fmt.Errorf(
			"agent governance: record budget exhaustion: %w", incidentErr,
		)
	}
	return 0, fmt.Errorf(
		"agent governance: %s budget exhausted (reserved %.8f + next %.8f > %.8f USD)",
		budget.Period, consumed, budget.MaxCostUSD, budget.PeriodCostUSD,
	)
}

func periodBudgetClaimParts(
	request AssistRequested,
	budget Budget,
	now time.Time,
	revision int,
) []string {
	return []string{
		"assist.budget", request.TemplateID, strconv.Itoa(request.Release),
		string(request.Environment), budget.Period,
		budgetPeriodStart(now, budget.Period).Format(time.RFC3339),
		strconv.Itoa(revision),
	}
}

func (s aggregate) approvalForAssist(assistID string) *toolApprovalState {
	for _, approval := range s.toolApprovals {
		if approval.request.AssistID == assistID {
			return approval
		}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func deploymentTemplate(deployment *deploymentState) string {
	if deployment == nil {
		return ""
	}
	return deployment.request.TemplateID
}

func (s aggregate) latestRelease(templateID string) int {
	latest := 0
	prefix := templateID + "\x00"
	for ref := range s.releases {
		if strings.HasPrefix(ref, prefix) {
			version, _ := strconv.Atoi(strings.TrimPrefix(ref, prefix))
			if version > latest {
				latest = version
			}
		}
	}
	return latest
}

func (s aggregate) latestSuite(suiteID string) int {
	latest := 0
	prefix := suiteID + "\x00"
	for ref := range s.suites {
		if strings.HasPrefix(ref, prefix) {
			version, _ := strconv.Atoi(strings.TrimPrefix(ref, prefix))
			if version > latest {
				latest = version
			}
		}
	}
	return latest
}

func (s aggregate) environmentBinding(
	templateID string,
	environment engine.Environment,
) *deploymentState {
	return s.environmentBindingExcept(templateID, environment, "")
}

func (s aggregate) environmentBindingExcept(
	templateID string,
	environment engine.Environment,
	except string,
) *deploymentState {
	for id, deployment := range s.deployments {
		if id != except && deployment.request.TemplateID == templateID &&
			deployment.request.Environment == environment &&
			(deployment.status == DeploymentScheduled || deployment.status == DeploymentActive) {
			return deployment
		}
	}
	return nil
}

func (s aggregate) hasOpenCriticalIncident(templateID string, release int) bool {
	for _, incident := range s.incidents {
		if incident.open && incident.templateID == templateID && incident.release == release &&
			incident.severity == SeverityCritical {
			return true
		}
	}
	return false
}

func (s aggregate) hasOpenCircuitIncident(deploymentID string) bool {
	for _, incident := range s.incidents {
		if incident.open && incident.deploymentID == deploymentID &&
			incident.kind == circuitBreakerIncidentKind {
			return true
		}
	}
	return false
}

func (s aggregate) circuitAssessment(
	deployment DeploymentRequest,
	spec ReleaseSpec,
	now time.Time,
) circuitAssessment {
	assessment := circuitAssessment{WindowEnd: now}
	if spec.CircuitBreaker == nil {
		return assessment
	}
	policy := *spec.CircuitBreaker
	assessment.Threshold = policy.FailureRate
	assessment.WindowStart = now.Add(-time.Duration(policy.WindowMinutes) * time.Minute)
	var resetSeq uint64
	for _, incident := range s.incidents {
		if incident.kind == circuitBreakerIncidentKind &&
			incident.deploymentID == deployment.DeploymentID &&
			!incident.resolvedAt.IsZero() &&
			incident.resolvedAt.After(assessment.WindowStart) {
			assessment.WindowStart = incident.resolvedAt
			if incident.resolvedSeq > resetSeq {
				resetSeq = incident.resolvedSeq
			}
		}
	}
	for _, outcome := range s.circuitOutcomes {
		if outcome.templateID != deployment.TemplateID ||
			outcome.release != deployment.Release ||
			outcome.environment != deployment.Environment ||
			outcome.seq <= resetSeq ||
			outcome.at.Before(assessment.WindowStart) ||
			outcome.at.After(now) {
			continue
		}
		assessment.Samples++
		if outcome.failed {
			assessment.Failures++
		}
		if outcome.seq > assessment.TerminalSeq {
			assessment.TerminalSeq = outcome.seq
		}
	}
	if assessment.Samples > 0 {
		assessment.ObservedRate = float64(assessment.Failures) /
			float64(assessment.Samples)
	}
	assessment.Tripped = assessment.Samples >= policy.MinSamples &&
		assessment.ObservedRate >= policy.FailureRate
	return assessment
}

func (h *Handler) appendUnique(
	ctx context.Context,
	id identity.Identity,
	typ string,
	payload any,
	claim string,
) (eventlog.Envelope, error) {
	return eventlog.AppendJSONUnique(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, typ, h.now(), payload, claim,
	)
}

func (h *Handler) append(
	ctx context.Context,
	id identity.Identity,
	typ string,
	payload any,
) (eventlog.Envelope, error) {
	return eventlog.AppendJSON(
		ctx, h.log, id.Org, id.Workspace, id.Actor, Stream, typ, h.now(), payload,
	)
}

func (h *Handler) claim(id identity.Identity, parts ...string) string {
	return "agent.governance\x00" + id.Org + "\x00" + id.Workspace + "\x00" +
		strings.Join(parts, "\x00")
}

func releaseRef(templateID string, release int) string {
	return templateID + "\x00" + strconv.Itoa(release)
}

func suiteRef(suiteID string, version int) string {
	return suiteID + "\x00" + strconv.Itoa(version)
}

func hashJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("agent governance: canonical JSON: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
