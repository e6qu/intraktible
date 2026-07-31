// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/case-manager/cases"
	engine "github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Service exposes the complete governed-agent lifecycle. Legacy /v1/agents
// remains the low-level execution surface; this surface is the enterprise
// template, evidence, review, and deployment control plane.
type Service struct {
	cmd           *Handler
	store         store.Store
	runner        *AssistRunner
	evaluator     *EvalRunner
	remote        *RemoteClient
	toolbox       agents.Toolbox
	contentSealer ContentSealer
	now           func() time.Time
	workerOwner   string
	assistJobs    chan assistJob
	workerWG      sync.WaitGroup
	workerMu      sync.Mutex
	workerHealth  sync.RWMutex
	workerErrors  map[string]error
	assistCursor  uint64
	assistOwners  map[assistResource]identity.Identity
	activeAssists map[assistResource]bool
}

type ServiceOption func(*Service)

func WithAssistRegistry(registry *ai.Registry) ServiceOption {
	return func(service *Service) {
		service.runner = NewAssistRunner(registry)
		service.evaluator = NewEvalRunner(registry)
	}
}

func WithRemoteClient(remote *RemoteClient) ServiceOption {
	return func(service *Service) { service.remote = remote }
}

func WithToolbox(toolbox agents.Toolbox) ServiceOption {
	return func(service *Service) { service.toolbox = toolbox }
}

func WithContentSealer(sealer ContentSealer) ServiceOption {
	return func(service *Service) { service.contentSealer = sealer }
}

func WithNow(now func() time.Time) ServiceOption {
	return func(service *Service) { service.now = now }
}

func NewService(cmd *Handler, st store.Store, options ...ServiceOption) *Service {
	service := &Service{
		cmd: cmd, store: st, now: func() time.Time { return time.Now().UTC() },
		workerOwner: governanceID(), assistJobs: make(chan assistJob, assistQueueSize),
		assistOwners:  make(map[assistResource]identity.Identity),
		activeAssists: make(map[assistResource]bool),
		workerErrors:  make(map[string]error),
	}
	for _, option := range options {
		option(service)
	}
	if service.remote != nil {
		if service.runner != nil {
			service.runner.WithRemote(service.remote)
		}
		if service.evaluator != nil {
			service.evaluator.WithRemote(service.remote)
		}
	}
	if service.runner != nil && service.toolbox != nil {
		service.runner.WithToolbox(service.toolbox)
	}
	if service.contentSealer != nil {
		service.cmd.WithContentSealer(service.contentSealer)
	}
	return service
}

func (s *Service) Routes(mux *http.ServeMux) {
	httpx.Register(mux, []httpx.Route{
		{Method: "POST", Pattern: "/v1/agent-templates", Handler: s.registerTemplate},
		{Method: "GET", Pattern: "/v1/agent-templates", Handler: s.listTemplates},
		{Method: "GET", Pattern: "/v1/agent-templates/{template_id}", Handler: s.getTemplate},
		{Method: "POST", Pattern: "/v1/agent-templates/{template_id}/releases", Handler: s.createRelease},
		{Method: "GET", Pattern: "/v1/agent-templates/{template_id}/releases", Handler: s.listReleases},
		{Method: "GET", Pattern: "/v1/agent-templates/{template_id}/releases/{release}", Handler: s.getRelease},
		{Method: "POST", Pattern: "/v1/agent-templates/{template_id}/releases/{release}/campaigns", Handler: s.recordCampaign},
		{Method: "GET", Pattern: "/v1/agent-templates/{template_id}/releases/{release}/campaigns", Handler: s.listCampaigns},
		{Method: "POST", Pattern: "/v1/agent-eval-campaigns/{campaign_id}/trials/{case_id}/{trial}/adjudication", Handler: s.adjudicateCampaignTrial},
		{Method: "GET", Pattern: "/v1/agent-eval-campaigns/{campaign_id}/export", Handler: s.exportCampaign},
		{Method: "GET", Pattern: "/v1/agent-eval-comparisons", Handler: s.compareCampaigns},
		{Method: "POST", Pattern: "/v1/agent-templates/{template_id}/releases/{release}/review-request", Handler: s.requestReview},
		{Method: "POST", Pattern: "/v1/agent-templates/{template_id}/releases/{release}/review", Handler: s.reviewRelease},
		{Method: "POST", Pattern: "/v1/agent-templates/{template_id}/releases/{release}/retire", Handler: s.retireRelease},
		{Method: "POST", Pattern: "/v1/agent-eval-suites", Handler: s.publishSuite},
		{Method: "GET", Pattern: "/v1/agent-eval-suites", Handler: s.listSuites},
		{Method: "GET", Pattern: "/v1/agent-eval-suites/{suite_id}/versions/{version}", Handler: s.getSuite},
		{Method: "POST", Pattern: "/v1/agent-deployments", Handler: s.requestDeployment},
		{Method: "GET", Pattern: "/v1/agent-deployments", Handler: s.listDeployments},
		{Method: "GET", Pattern: "/v1/agent-deployments/{deployment_id}", Handler: s.getDeployment},
		{Method: "POST", Pattern: "/v1/agent-deployments/{deployment_id}/activate", Handler: s.activateDeployment},
		{Method: "POST", Pattern: "/v1/agent-deployments/{deployment_id}/pause", Handler: s.pauseDeployment},
		{Method: "POST", Pattern: "/v1/agent-deployments/{deployment_id}/resume", Handler: s.resumeDeployment},
		{Method: "POST", Pattern: "/v1/agent-deployments/{deployment_id}/rollback", Handler: s.rollbackDeployment},
		{Method: "POST", Pattern: "/v1/cases/{case_id}/agent-assists", Handler: s.requestAssist},
		{Method: "GET", Pattern: "/v1/cases/{case_id}/agent-assists", Handler: s.listCaseAssists},
		{Method: "GET", Pattern: "/v1/agent-assists/{assist_id}", Handler: s.getAssist},
		{Method: "POST", Pattern: "/v1/agent-assists/{assist_id}/reviewer-action", Handler: s.recordAssistAction},
		{Method: "POST", Pattern: "/v1/agent-assists/{assist_id}/retry", Handler: s.retryAssist},
		{Method: "POST", Pattern: "/v1/agent-assists/{assist_id}/cancel", Handler: s.cancelAssist},
		{Method: "GET", Pattern: "/v1/agent-tool-approvals", Handler: s.listToolApprovals},
		{Method: "GET", Pattern: "/v1/agent-tool-approvals/{approval_id}", Handler: s.getToolApproval},
		{Method: "POST", Pattern: "/v1/agent-tool-approvals/{approval_id}/decision", Handler: s.decideToolApproval},
		{Method: "GET", Pattern: "/v1/agent-governance/analytics", Handler: s.getAnalytics},
		{Method: "POST", Pattern: "/v1/agent-safety-incidents", Handler: s.openIncident},
		{Method: "GET", Pattern: "/v1/agent-safety-incidents", Handler: s.listIncidents},
		{Method: "POST", Pattern: "/v1/agent-safety-incidents/{incident_id}/resolve", Handler: s.resolveIncident},
	})
}

func (s *Service) registerTemplate(w http.ResponseWriter, r *http.Request) {
	createCommand(w, r, func(
		ctx context.Context,
		id identity.Identity,
		request Template,
	) (map[string]any, error) {
		template, event, err := s.cmd.RegisterTemplate(ctx, id, request)
		return map[string]any{
			"template": template, "event_id": event.ID, "seq": event.Seq,
		}, err
	})
}

func (s *Service) listTemplates(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := ListTemplates(r.Context(), s.store, id)
	httpx.WriteList(w, "templates", views, err)
}

func (s *Service) getTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := ReadTemplate(
		r.Context(), s.store, id, r.PathValue("template_id"),
	)
	httpx.WriteOne(w, view, found, err, "agent template not found")
}

func (s *Service) createRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Spec ReleaseSpec `json:"spec"`
	}
	if !decode(w, r, &request) {
		return
	}
	release, hash, event, err := s.cmd.CreateRelease(
		r.Context(), id, r.PathValue("template_id"), request.Spec,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"release": release, "spec_hash": hash, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) listReleases(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := ListReleases(
		r.Context(), s.store, id, r.PathValue("template_id"),
	)
	httpx.WriteList(w, "releases", views, err)
}

func (s *Service) getRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	release, valid := pathInt(w, r, "release")
	if !valid {
		return
	}
	view, found, err := ReadRelease(
		r.Context(), s.store, id, r.PathValue("template_id"), release,
	)
	httpx.WriteOne(w, view, found, err, "agent release not found")
}

func (s *Service) publishSuite(w http.ResponseWriter, r *http.Request) {
	createCommand(w, r, func(
		ctx context.Context,
		id identity.Identity,
		request EvalSuite,
	) (map[string]any, error) {
		suite, event, err := s.cmd.PublishSuite(ctx, id, request)
		return map[string]any{
			"suite": suite, "event_id": event.ID, "seq": event.Seq,
		}, err
	})
}

func (s *Service) listSuites(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := ListSuites(r.Context(), s.store, id)
	httpx.WriteList(w, "suites", views, err)
}

func (s *Service) getSuite(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	version, valid := pathInt(w, r, "version")
	if !valid {
		return
	}
	view, found, err := ReadSuite(
		r.Context(), s.store, id, r.PathValue("suite_id"), version,
	)
	httpx.WriteOne(w, view, found, err, "agent evaluation suite not found")
}

func (s *Service) recordCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	release, valid := pathInt(w, r, "release")
	if !valid {
		return
	}
	var request struct {
		SuiteID      string        `json:"suite_id"`
		SuiteVersion int           `json:"suite_version"`
		Trials       []TrialResult `json:"trials"`
	}
	if !decode(w, r, &request) {
		return
	}
	if len(request.Trials) != 0 {
		httpx.Error(
			w, http.StatusBadRequest,
			fmt.Errorf("agent governance: trial evidence is produced by the configured evaluator"),
		)
		return
	}
	if s.evaluator == nil {
		httpx.Error(
			w, http.StatusServiceUnavailable,
			fmt.Errorf("agent governance: no evaluation worker is configured"),
		)
		return
	}
	releaseView, err := s.cmd.ReleaseSnapshot(
		r.Context(), id, r.PathValue("template_id"), release,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	suite, err := s.cmd.SuiteSnapshot(
		r.Context(), id, request.SuiteID, request.SuiteVersion,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	request.Trials, err = s.evaluator.Run(r.Context(), id.Actor, releaseView, suite)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err)
		return
	}
	result, event, err := s.cmd.RecordCampaign(
		r.Context(), id, r.PathValue("template_id"), release,
		request.SuiteID, request.SuiteVersion, request.Trials,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"campaign": result, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) listCampaigns(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	release, valid := pathInt(w, r, "release")
	if !valid {
		return
	}
	views, err := ListCampaigns(
		r.Context(), s.store, id, r.PathValue("template_id"), release,
	)
	httpx.WriteList(w, "campaigns", views, err)
}

func (s *Service) adjudicateCampaignTrial(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	trial, valid := pathInt(w, r, "trial")
	if !valid {
		return
	}
	var request struct {
		Passed bool   `json:"passed"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &request) {
		return
	}
	event, err := s.cmd.AdjudicateCampaignTrial(
		r.Context(), id, r.PathValue("campaign_id"), r.PathValue("case_id"),
		trial, request.Passed, request.Reason,
	)
	writeEvent(w, event.ID, event.Seq, err)
}

func (s *Service) compareCampaigns(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	baselineID := strings.TrimSpace(r.URL.Query().Get("baseline_campaign_id"))
	challengerID := strings.TrimSpace(r.URL.Query().Get("challenger_campaign_id"))
	if baselineID == "" || challengerID == "" || baselineID == challengerID {
		httpx.Error(
			w, http.StatusBadRequest,
			errors.New(
				"agent governance: distinct baseline_campaign_id and challenger_campaign_id are required",
			),
		)
		return
	}
	baseline, found, err := ReadCampaign(r.Context(), s.store, id, baselineID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("baseline campaign not found"))
		return
	}
	challenger, found, err := ReadCampaign(r.Context(), s.store, id, challengerID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("challenger campaign not found"))
		return
	}
	suite, found, err := ReadSuite(
		r.Context(), s.store, id, baseline.SuiteID, baseline.SuiteVersion,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusInternalServerError, errors.New("campaign suite not found"))
		return
	}
	comparison, err := CompareCampaigns(baseline, challenger, suite.EvalSuite)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, comparison)
}

func (s *Service) exportCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	campaign, found, err := ReadCampaign(
		r.Context(), s.store, id, r.PathValue("campaign_id"),
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("campaign not found"))
		return
	}
	suite, found, err := ReadSuite(
		r.Context(), s.store, id, campaign.SuiteID, campaign.SuiteVersion,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusInternalServerError, errors.New("campaign suite not found"))
		return
	}
	release, found, err := ReadRelease(
		r.Context(), s.store, id, campaign.TemplateID, campaign.Release,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusInternalServerError, errors.New("campaign release not found"))
		return
	}
	switch r.URL.Query().Get("format") {
	case "", "json":
		body, exportErr := CampaignExportJSON(
			campaign, suite.EvalSuite, release.SpecHash,
		)
		if exportErr != nil {
			httpx.Error(w, http.StatusInternalServerError, exportErr)
			return
		}
		httpx.Download(
			w, "application/json; charset=utf-8",
			"agent-campaign-"+campaign.CampaignID+".json", body,
		)
	case "csv":
		body, exportErr := CampaignExportCSV(campaign, suite.EvalSuite)
		if exportErr != nil {
			httpx.Error(w, http.StatusInternalServerError, exportErr)
			return
		}
		httpx.Download(
			w, "text/csv; charset=utf-8",
			"agent-campaign-"+campaign.CampaignID+".csv", body,
		)
	default:
		httpx.Error(
			w, http.StatusBadRequest,
			errors.New("agent governance: campaign export format must be json or csv"),
		)
	}
}

func (s *Service) requestReview(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	release, valid := pathInt(w, r, "release")
	if !valid {
		return
	}
	var request struct {
		CampaignIDs []string  `json:"campaign_ids"`
		EvidenceIDs []string  `json:"evidence_ids"`
		Reviewers   []string  `json:"reviewers,omitempty"`
		ExpiresAt   time.Time `json:"expires_at"`
	}
	if !decode(w, r, &request) {
		return
	}
	requestID, event, err := s.cmd.RequestReview(
		r.Context(), id, r.PathValue("template_id"), release,
		request.CampaignIDs, request.EvidenceIDs, request.Reviewers, request.ExpiresAt,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"request_id": requestID, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) reviewRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	release, valid := pathInt(w, r, "release")
	if !valid {
		return
	}
	var request struct {
		RequestID string         `json:"request_id"`
		Decision  ReviewDecision `json:"decision"`
		Reason    string         `json:"reason"`
	}
	if !decode(w, r, &request) {
		return
	}
	event, err := s.cmd.ReviewRelease(
		r.Context(), id, r.PathValue("template_id"), release,
		request.RequestID, request.Decision, request.Reason,
	)
	writeEvent(w, event.ID, event.Seq, err)
}

func (s *Service) retireRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	release, valid := pathInt(w, r, "release")
	if !valid {
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &request) {
		return
	}
	event, err := s.cmd.RetireRelease(
		r.Context(), id, r.PathValue("template_id"), release, request.Reason,
	)
	writeEvent(w, event.ID, event.Seq, err)
}

func (s *Service) requestDeployment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request DeploymentRequest
	if !decode(w, r, &request) {
		return
	}
	event, err := s.cmd.RequestDeployment(r.Context(), id, request)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	var recorded DeploymentRequestedEvent
	if err := json.Unmarshal(event.Payload, &recorded); err != nil {
		httpx.Error(w, http.StatusInternalServerError, fmt.Errorf("decode recorded deployment: %w", err))
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"deployment_id": recorded.Request.DeploymentID, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) listDeployments(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := ListDeployments(r.Context(), s.store, id)
	httpx.WriteList(w, "deployments", views, err)
}

func (s *Service) getDeployment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := ReadDeployment(
		r.Context(), s.store, id, r.PathValue("deployment_id"),
	)
	httpx.WriteOne(w, view, found, err, "agent deployment not found")
}

func (s *Service) activateDeployment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	event, err := s.cmd.ActivateDeployment(r.Context(), id, r.PathValue("deployment_id"))
	writeEvent(w, event.ID, event.Seq, err)
}

func (s *Service) pauseDeployment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &request) {
		return
	}
	event, err := s.cmd.PauseDeployment(
		r.Context(), id, r.PathValue("deployment_id"), request.Reason,
	)
	writeEvent(w, event.ID, event.Seq, err)
}

func (s *Service) resumeDeployment(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &request) {
		return
	}
	event, err := s.cmd.ResumeDeployment(
		r.Context(), id, r.PathValue("deployment_id"), request.Reason,
	)
	writeEvent(w, event.ID, event.Seq, err)
}

func (s *Service) rollbackDeployment(w http.ResponseWriter, r *http.Request) {
	type rollbackRequest struct {
		ToRelease int    `json:"to_release"`
		Reason    string `json:"reason"`
	}
	id, request, ok := commandInput[rollbackRequest](w, r)
	if !ok {
		return
	}
	event, err := s.cmd.RollbackDeployment(
		r.Context(), id, r.PathValue("deployment_id"), request.ToRelease, request.Reason,
	)
	writeEvent(w, event.ID, event.Seq, err)
}

func (s *Service) requestAssist(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Kind        AssistKind         `json:"kind"`
		TemplateID  string             `json:"template_id"`
		Release     int                `json:"release"`
		Environment engine.Environment `json:"environment"`
		EvidenceIDs []string           `json:"evidence_ids"`
	}
	if !decode(w, r, &request) {
		return
	}
	caseID := r.PathValue("case_id")
	view, found, err := cases.Read(r.Context(), s.store, id, caseID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("case not found"))
		return
	}
	available := make(map[string]cases.EvidenceLink, len(view.Evidence))
	for _, evidence := range view.Evidence {
		available[evidence.EvidenceID] = evidence
	}
	var evidenceSeq uint64
	for _, evidenceID := range request.EvidenceIDs {
		evidence, exists := available[evidenceID]
		if !exists {
			httpx.Error(
				w, http.StatusBadRequest,
				fmt.Errorf("case evidence %q is not linked to this case", evidenceID),
			)
			return
		}
		if evidence.LinkedSeq > evidenceSeq {
			evidenceSeq = evidence.LinkedSeq
		}
	}
	if evidenceSeq < 1 {
		httpx.Error(
			w, http.StatusBadRequest,
			errors.New("case evidence snapshot has no authoritative sequence"),
		)
		return
	}
	idempotency := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotency == "" || len(idempotency) > 256 {
		httpx.Error(
			w, http.StatusBadRequest,
			errors.New(
				"agent governance: Idempotency-Key is required and must be at most 256 bytes",
			),
		)
		return
	}
	sum := sha256.Sum256([]byte(idempotency))
	idempotencyHash := hex.EncodeToString(sum[:])
	assistRequest := AssistRequested{
		CaseID: caseID, Kind: request.Kind, TemplateID: request.TemplateID,
		Release: request.Release, Environment: request.Environment,
		EvidenceIDs: request.EvidenceIDs, EvidenceSeq: evidenceSeq,
		IdempotencyHash: idempotencyHash,
		Subject:         view.Subject,
	}
	if strings.TrimSpace(assistRequest.Subject) == "" {
		assistRequest.Subject = "case/" + caseID
	}
	if s.contentSealer != nil {
		input, inputErr := assistInputFromCase(view, request.EvidenceIDs)
		if inputErr != nil {
			httpx.Error(w, http.StatusBadRequest, inputErr)
			return
		}
		encoded, encodeErr := json.Marshal(input)
		if encodeErr != nil {
			httpx.Error(
				w, http.StatusInternalServerError,
				fmt.Errorf("agent governance: encode assist input snapshot: %w", encodeErr),
			)
			return
		}
		assistRequest.SealedInput, err = s.contentSealer.Seal(
			r.Context(), id, assistRequest.Subject, encoded,
		)
		if err != nil {
			httpx.Error(
				w, http.StatusBadRequest,
				fmt.Errorf("agent governance: seal assist input snapshot: %w", err),
			)
			return
		}
		assistRequest.InputSubject = assistRequest.Subject
	}
	assistID, event, err := s.cmd.RequestAssist(r.Context(), id, assistRequest)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	status, found, statusErr := s.cmd.AssistStatus(r.Context(), id, assistID)
	if statusErr != nil {
		httpx.Error(w, http.StatusInternalServerError, statusErr)
		return
	}
	if !found {
		httpx.Error(
			w, http.StatusInternalServerError,
			errors.New("agent governance: accepted assist disappeared"),
		)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"assist_id": assistID, "status": status, "event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) listCaseAssists(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := ListCaseAssists(r.Context(), s.store, id, r.PathValue("case_id"))
	if err == nil {
		var caseView cases.CaseView
		if len(views) > 0 {
			var found bool
			caseView, found, err = cases.Read(
				r.Context(), s.store, id, r.PathValue("case_id"),
			)
			if err == nil && !found {
				err = errors.New(
					"agent governance: assist references a missing case",
				)
			}
		}
		for index := range views {
			if err != nil {
				break
			}
			views[index], err = annotateAssistEvidence(views[index], caseView)
			if err != nil {
				break
			}
			views[index], err = s.openAssistContent(r.Context(), id, views[index])
			if err != nil {
				break
			}
		}
	}
	httpx.WriteList(w, "assists", views, err)
}

func (s *Service) getAssist(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := ReadAssist(
		r.Context(), s.store, id, r.PathValue("assist_id"),
	)
	if err == nil && found {
		var caseView cases.CaseView
		var caseFound bool
		caseView, caseFound, err = cases.Read(
			r.Context(), s.store, id, view.CaseID,
		)
		if err == nil && !caseFound {
			err = errors.New(
				"agent governance: assist references a missing case",
			)
		}
		if err == nil {
			view, err = annotateAssistEvidence(view, caseView)
		}
		if err == nil {
			view, err = s.openAssistContent(r.Context(), id, view)
		}
	}
	httpx.WriteOne(w, view, found, err, "agent assist not found")
}

func (s *Service) retryAssist(w http.ResponseWriter, r *http.Request) {
	type retryRequest struct {
		Reason                 string `json:"reason"`
		AcknowledgeAtLeastOnce bool   `json:"acknowledge_at_least_once"`
	}
	id, request, ok := commandInput[retryRequest](w, r)
	if !ok {
		return
	}
	event, err := s.cmd.RetryAssist(
		r.Context(), id, r.PathValue("assist_id"), request.Reason,
		request.AcknowledgeAtLeastOnce,
	)
	writeEvent(w, event.ID, event.Seq, err)
}

func (s *Service) cancelAssist(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &request) {
		return
	}
	event, err := s.cmd.CancelAssist(
		r.Context(), id, r.PathValue("assist_id"), request.Reason,
	)
	writeEvent(w, event.ID, event.Seq, err)
}

func (s *Service) openAssistContent(
	ctx context.Context,
	id identity.Identity,
	view AssistView,
) (AssistView, error) {
	if len(view.SealedContent) == 0 {
		return view, nil
	}
	if s.contentSealer == nil {
		return AssistView{}, errors.New(
			"agent governance: sealed assist content has no configured content sealer",
		)
	}
	plain, err := s.contentSealer.Open(
		ctx, id, view.ContentSubject, view.SealedContent,
	)
	if errors.Is(err, erasure.ErrErased) {
		hadActionFinal := len(view.SealedActionFinal) > 0
		view.ContentSubject, view.SealedContent = "", nil
		view.ContentErased = true
		if view.Result != nil {
			view.Result.clearContent()
		}
		if view.Action != nil {
			view.Action.Final = nil
		}
		view.ActionContentSubject, view.SealedActionFinal = "", nil
		view.ActionFinalErased = hadActionFinal
		return view, nil
	}
	if err != nil {
		return AssistView{}, fmt.Errorf("agent governance: open assist content: %w", err)
	}
	var content AssistContent
	if err := json.Unmarshal(plain, &content); err != nil {
		return AssistView{}, fmt.Errorf("agent governance: decode assist content: %w", err)
	}
	if view.Result == nil {
		return AssistView{}, errors.New(
			"agent governance: sealed assist content has no result metadata",
		)
	}
	view.Result.restoreContent(content)
	if err := view.Result.Validate(); err != nil {
		return AssistView{}, fmt.Errorf("agent governance: invalid opened assist content: %w", err)
	}
	view.ContentSubject, view.SealedContent = "", nil
	if len(view.SealedActionFinal) > 0 {
		plain, err = s.contentSealer.Open(
			ctx, id, view.ActionContentSubject, view.SealedActionFinal,
		)
		if errors.Is(err, erasure.ErrErased) {
			if view.Action != nil {
				view.Action.Final = nil
			}
			view.ActionContentSubject, view.SealedActionFinal = "", nil
			view.ActionFinalErased = true
			return view, nil
		}
		if err != nil {
			return AssistView{}, fmt.Errorf(
				"agent governance: open reviewer-edited assist: %w", err,
			)
		}
		if view.Action == nil || view.Action.Action != AssistEdited {
			return AssistView{}, errors.New(
				"agent governance: sealed reviewer final has no edited action",
			)
		}
		view.Action.Final = append(json.RawMessage(nil), plain...)
		if err := validateJSONObject(
			"opened reviewer-edited assist", view.Action.Final,
		); err != nil {
			return AssistView{}, err
		}
		finalHash, err := hashJSON(view.Action.Final)
		if err != nil {
			return AssistView{}, err
		}
		if finalHash != view.FinalHash {
			return AssistView{}, errors.New(
				"agent governance: opened reviewer final does not match its hash",
			)
		}
		view.ActionContentSubject, view.SealedActionFinal = "", nil
	}
	return view, nil
}

func annotateAssistEvidence(
	view AssistView,
	caseView cases.CaseView,
) (AssistView, error) {
	if view.CaseID != caseView.CaseID {
		return AssistView{}, errors.New(
			"agent governance: assist and case evidence lineage do not match",
		)
	}
	var current uint64
	for _, evidence := range caseView.Evidence {
		if evidence.LinkedSeq > current {
			current = evidence.LinkedSeq
		}
	}
	for _, attachment := range caseView.Attachments {
		if attachment.RegisteredSeq > current {
			current = attachment.RegisteredSeq
		}
	}
	if current < view.EvidenceSeq {
		return AssistView{}, errors.New(
			"agent governance: current case evidence predates assist snapshot",
		)
	}
	view.CurrentEvidenceSeq = current
	view.EvidenceStale = current > view.EvidenceSeq
	return view, nil
}

func (s *Service) listToolApprovals(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := ListToolApprovals(r.Context(), s.store, id)
	httpx.WriteList(w, "approvals", views, err)
}

func (s *Service) getToolApproval(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	view, found, err := ReadToolApproval(
		r.Context(), s.store, id, r.PathValue("approval_id"),
	)
	httpx.WriteOne(w, view, found, err, "agent tool approval not found")
}

func (s *Service) getAnalytics(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	report, err := BuildAgentAnalytics(r.Context(), s.store, id)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}

func (s *Service) decideToolApproval(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Decision ToolApprovalStatus `json:"decision"`
		Reason   string             `json:"reason"`
	}
	if !decode(w, r, &request) {
		return
	}
	approvalID := r.PathValue("approval_id")
	event, err := s.cmd.DecideToolApproval(
		r.Context(), id, approvalID, request.Decision, request.Reason,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	if request.Decision == ToolApprovalRejected {
		httpx.JSON(w, http.StatusOK, map[string]any{
			"approval_id": approvalID, "status": ToolApprovalRejected,
			"event_id": event.ID, "seq": event.Seq,
		})
		return
	}
	approval, status, _, err := s.cmd.ToolApprovalSnapshot(
		r.Context(), id, approvalID,
	)
	if err != nil || status != ToolApprovalApproved {
		if err == nil {
			err = errors.New("agent governance: tool approval was not persisted as approved")
		}
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	s.enqueueAssist(assistJob{id: id, assistID: approval.AssistID})
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"approval_id": approvalID, "assist_id": approval.AssistID,
		"status":   AssistRequestedStatus,
		"event_id": event.ID, "seq": event.Seq,
	})
}

func (s *Service) recordAssistAction(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var action ReviewerAction
	if !decode(w, r, &action) {
		return
	}
	if action.AssistID == "" {
		action.AssistID = r.PathValue("assist_id")
	}
	if action.AssistID != r.PathValue("assist_id") {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("assist_id does not match path"))
		return
	}
	assist, found, err := ReadAssist(
		r.Context(), s.store, id, action.AssistID,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("agent assist not found"))
		return
	}
	caseView, found, err := cases.Read(
		r.Context(), s.store, id, assist.CaseID,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(
			w, http.StatusInternalServerError,
			errors.New("agent governance: assist references a missing case"),
		)
		return
	}
	annotated, err := annotateAssistEvidence(assist, caseView)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	event, err := s.cmd.RecordAssistActionAtEvidence(
		r.Context(), id, action, annotated.CurrentEvidenceSeq,
	)
	writeEvent(w, event.ID, event.Seq, err)
}

func (s *Service) openIncident(w http.ResponseWriter, r *http.Request) {
	createCommand(w, r, func(
		ctx context.Context,
		id identity.Identity,
		incident SafetyIncidentOpened,
	) (map[string]any, error) {
		incidentID, event, err := s.cmd.OpenSafetyIncident(ctx, id, incident)
		return map[string]any{
			"incident_id": incidentID, "event_id": event.ID, "seq": event.Seq,
		}, err
	})
}

func (s *Service) listIncidents(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	views, err := ListIncidents(r.Context(), s.store, id)
	httpx.WriteList(w, "incidents", views, err)
}

func (s *Service) resolveIncident(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request struct {
		Resolution string `json:"resolution"`
	}
	if !decode(w, r, &request) {
		return
	}
	event, err := s.cmd.ResolveSafetyIncident(
		r.Context(), id, r.PathValue("incident_id"), request.Resolution,
	)
	writeEvent(w, event.ID, event.Seq, err)
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := httpx.DecodeJSON(r, value); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func commandInput[T any](
	w http.ResponseWriter,
	r *http.Request,
) (identity.Identity, T, bool) {
	var request T
	id, ok := httpx.Caller(w, r)
	if !ok || !decode(w, r, &request) {
		return identity.Identity{}, request, false
	}
	return id, request, true
}

func createCommand[T any](
	w http.ResponseWriter,
	r *http.Request,
	create func(context.Context, identity.Identity, T) (map[string]any, error),
) {
	id, request, ok := commandInput[T](w, r)
	if !ok {
		return
	}
	response, err := create(r.Context(), id, request)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, response)
}

func pathInt(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	value, err := strconv.Atoi(r.PathValue(name))
	if err != nil || value < 1 {
		httpx.Error(w, http.StatusBadRequest, fmt.Errorf("%s must be a positive integer", name))
		return 0, false
	}
	return value, true
}

func writeEvent(w http.ResponseWriter, eventID string, seq uint64, err error) {
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"event_id": eventID, "seq": seq})
}
