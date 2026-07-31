// SPDX-License-Identifier: AGPL-3.0-or-later

package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	agentgovernance "github.com/e6qu/intraktible/agent-manager/governance"
	engine "github.com/e6qu/intraktible/decision-engine/domain"
)

// Public aliases keep the Go SDK wire contract identical to the server's named
// enums and validated lifecycle types.
type (
	AgentTemplate           = agentgovernance.TemplateView
	AgentRelease            = agentgovernance.ReleaseView
	AgentEvalSuite          = agentgovernance.SuiteView
	AgentCampaign           = agentgovernance.CampaignView
	AgentCampaignComparison = agentgovernance.CampaignComparison
	AgentDeployment         = agentgovernance.DeploymentView
	AgentAssist             = agentgovernance.AssistView
	AgentToolApproval       = agentgovernance.ToolApprovalView
	AgentSafetyIncident     = agentgovernance.IncidentView
	AgentAnalytics          = agentgovernance.AgentAnalyticsReport
)

type AgentAssistRequest struct {
	Kind        agentgovernance.AssistKind `json:"kind"`
	TemplateID  string                     `json:"template_id"`
	Release     int                        `json:"release"`
	Environment engine.Environment         `json:"environment"`
	EvidenceIDs []string                   `json:"evidence_ids"`
}

func (c *Client) ListAgentTemplates(ctx context.Context) ([]AgentTemplate, error) {
	out, err := do[struct {
		Templates []AgentTemplate `json:"templates"`
	}](ctx, c, http.MethodGet, "/v1/agent-templates", nil)
	return out.Templates, err
}

func (c *Client) CreateAgentTemplate(
	ctx context.Context,
	template agentgovernance.Template,
) (AgentTemplate, error) {
	out, err := do[struct {
		Template AgentTemplate `json:"template"`
	}](ctx, c, http.MethodPost, "/v1/agent-templates", template)
	return out.Template, err
}

func (c *Client) GetAgentTemplate(
	ctx context.Context,
	templateID string,
) (AgentTemplate, error) {
	return do[AgentTemplate](
		ctx, c, http.MethodGet, "/v1/agent-templates/"+url.PathEscape(templateID), nil,
	)
}

func (c *Client) ListAgentReleases(
	ctx context.Context,
	templateID string,
) ([]AgentRelease, error) {
	out, err := do[struct {
		Releases []AgentRelease `json:"releases"`
	}](
		ctx, c, http.MethodGet,
		"/v1/agent-templates/"+url.PathEscape(templateID)+"/releases", nil,
	)
	return out.Releases, err
}

func (c *Client) CreateAgentRelease(
	ctx context.Context,
	templateID string,
	spec agentgovernance.ReleaseSpec,
) (int, string, error) {
	out, err := do[struct {
		Release  int    `json:"release"`
		SpecHash string `json:"spec_hash"`
	}](
		ctx, c, http.MethodPost,
		"/v1/agent-templates/"+url.PathEscape(templateID)+"/releases",
		struct {
			Spec agentgovernance.ReleaseSpec `json:"spec"`
		}{Spec: spec},
	)
	return out.Release, out.SpecHash, err
}

func (c *Client) GetAgentRelease(
	ctx context.Context,
	templateID string,
	release int,
) (AgentRelease, error) {
	return do[AgentRelease](
		ctx, c, http.MethodGet, agentReleasePath(templateID, release), nil,
	)
}

func (c *Client) RetireAgentRelease(
	ctx context.Context,
	templateID string,
	release int,
	reason string,
) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPost, agentReleasePath(templateID, release)+"/retire",
		map[string]string{"reason": reason},
	)
	return err
}

func (c *Client) ListAgentEvalSuites(ctx context.Context) ([]AgentEvalSuite, error) {
	out, err := do[struct {
		Suites []AgentEvalSuite `json:"suites"`
	}](ctx, c, http.MethodGet, "/v1/agent-eval-suites", nil)
	return out.Suites, err
}

func (c *Client) PublishAgentEvalSuite(
	ctx context.Context,
	suite agentgovernance.EvalSuite,
) (agentgovernance.EvalSuite, error) {
	out, err := do[struct {
		Suite agentgovernance.EvalSuite `json:"suite"`
	}](ctx, c, http.MethodPost, "/v1/agent-eval-suites", suite)
	return out.Suite, err
}

func (c *Client) GetAgentEvalSuite(
	ctx context.Context,
	suiteID string,
	version int,
) (AgentEvalSuite, error) {
	return do[AgentEvalSuite](
		ctx, c, http.MethodGet,
		"/v1/agent-eval-suites/"+url.PathEscape(suiteID)+"/versions/"+
			strconv.Itoa(version),
		nil,
	)
}

func (c *Client) RunAgentCampaign(
	ctx context.Context,
	templateID string,
	release int,
	suiteID string,
	suiteVersion int,
) (agentgovernance.CampaignResult, error) {
	out, err := do[struct {
		Campaign agentgovernance.CampaignResult `json:"campaign"`
	}](
		ctx, c, http.MethodPost, agentReleasePath(templateID, release)+"/campaigns",
		map[string]any{"suite_id": suiteID, "suite_version": suiteVersion},
	)
	return out.Campaign, err
}

func (c *Client) ListAgentCampaigns(
	ctx context.Context,
	templateID string,
	release int,
) ([]AgentCampaign, error) {
	out, err := do[struct {
		Campaigns []AgentCampaign `json:"campaigns"`
	}](
		ctx, c, http.MethodGet, agentReleasePath(templateID, release)+"/campaigns", nil,
	)
	return out.Campaigns, err
}

func (c *Client) AdjudicateAgentCampaignTrial(
	ctx context.Context,
	campaignID, caseID string,
	trial int,
	passed bool,
	reason string,
) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/agent-eval-campaigns/"+url.PathEscape(campaignID)+
			"/trials/"+url.PathEscape(caseID)+"/"+strconv.Itoa(trial)+
			"/adjudication",
		map[string]any{"passed": passed, "reason": reason},
	)
	return err
}

func (c *Client) CompareAgentCampaigns(
	ctx context.Context,
	baselineCampaignID, challengerCampaignID string,
) (AgentCampaignComparison, error) {
	query := url.Values{}
	query.Set("baseline_campaign_id", baselineCampaignID)
	query.Set("challenger_campaign_id", challengerCampaignID)
	return do[AgentCampaignComparison](
		ctx, c, http.MethodGet, "/v1/agent-eval-comparisons?"+query.Encode(), nil,
	)
}

func (c *Client) ExportAgentCampaign(
	ctx context.Context,
	campaignID, format string,
) ([]byte, error) {
	query := url.Values{}
	query.Set("format", format)
	return doBytes(
		ctx, c, "/v1/agent-eval-campaigns/"+url.PathEscape(campaignID)+
			"/export?"+query.Encode(),
	)
}

func (c *Client) RequestAgentReleaseReview(
	ctx context.Context,
	templateID string,
	release int,
	campaignIDs, evidenceIDs, reviewers []string,
	expiresAt time.Time,
) (string, error) {
	out, err := do[struct {
		RequestID string `json:"request_id"`
	}](
		ctx, c, http.MethodPost, agentReleasePath(templateID, release)+"/review-request",
		map[string]any{
			"campaign_ids": campaignIDs, "evidence_ids": evidenceIDs,
			"reviewers": reviewers, "expires_at": expiresAt,
		},
	)
	return out.RequestID, err
}

func (c *Client) ReviewAgentRelease(
	ctx context.Context,
	templateID string,
	release int,
	requestID string,
	decision agentgovernance.ReviewDecision,
	reason string,
) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPost, agentReleasePath(templateID, release)+"/review",
		map[string]any{
			"request_id": requestID, "decision": decision, "reason": reason,
		},
	)
	return err
}

func (c *Client) ListAgentDeployments(ctx context.Context) ([]AgentDeployment, error) {
	out, err := do[struct {
		Deployments []AgentDeployment `json:"deployments"`
	}](ctx, c, http.MethodGet, "/v1/agent-deployments", nil)
	return out.Deployments, err
}

func (c *Client) RequestAgentDeployment(
	ctx context.Context,
	request agentgovernance.DeploymentRequest,
) (string, error) {
	out, err := do[struct {
		DeploymentID string `json:"deployment_id"`
	}](ctx, c, http.MethodPost, "/v1/agent-deployments", request)
	return out.DeploymentID, err
}

func (c *Client) GetAgentDeployment(
	ctx context.Context,
	deploymentID string,
) (AgentDeployment, error) {
	return do[AgentDeployment](
		ctx, c, http.MethodGet,
		"/v1/agent-deployments/"+url.PathEscape(deploymentID), nil,
	)
}

func (c *Client) ActivateAgentDeployment(ctx context.Context, deploymentID string) error {
	return c.agentDeploymentAction(ctx, deploymentID, "activate", map[string]any{})
}

func (c *Client) PauseAgentDeployment(
	ctx context.Context,
	deploymentID, reason string,
) error {
	return c.agentDeploymentAction(
		ctx, deploymentID, "pause", map[string]string{"reason": reason},
	)
}

func (c *Client) ResumeAgentDeployment(
	ctx context.Context,
	deploymentID, reason string,
) error {
	return c.agentDeploymentAction(
		ctx, deploymentID, "resume", map[string]string{"reason": reason},
	)
}

func (c *Client) RollbackAgentDeployment(
	ctx context.Context,
	deploymentID string,
	toRelease int,
	reason string,
) error {
	return c.agentDeploymentAction(
		ctx, deploymentID, "rollback",
		map[string]any{"to_release": toRelease, "reason": reason},
	)
}

func (c *Client) agentDeploymentAction(
	ctx context.Context,
	deploymentID, action string,
	body any,
) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/agent-deployments/"+url.PathEscape(deploymentID)+"/"+action, body,
	)
	return err
}

func (c *Client) RequestCaseAgentAssist(
	ctx context.Context,
	caseID string,
	request AgentAssistRequest,
	idempotencyKey string,
) (string, string, error) {
	out, err := doWithHeaders[struct {
		AssistID string `json:"assist_id"`
		Status   string `json:"status"`
	}](
		ctx, c, http.MethodPost,
		"/v1/cases/"+url.PathEscape(caseID)+"/agent-assists", request,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	return out.AssistID, out.Status, err
}

func (c *Client) ListCaseAgentAssists(
	ctx context.Context,
	caseID string,
) ([]AgentAssist, error) {
	out, err := do[struct {
		Assists []AgentAssist `json:"assists"`
	}](
		ctx, c, http.MethodGet,
		"/v1/cases/"+url.PathEscape(caseID)+"/agent-assists", nil,
	)
	return out.Assists, err
}

func (c *Client) GetAgentAssist(ctx context.Context, assistID string) (AgentAssist, error) {
	return do[AgentAssist](
		ctx, c, http.MethodGet, "/v1/agent-assists/"+url.PathEscape(assistID), nil,
	)
}

func (c *Client) RecordAgentAssistAction(
	ctx context.Context,
	assistID string,
	action agentgovernance.ReviewerAction,
) error {
	action.AssistID = assistID
	_, err := do[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/agent-assists/"+url.PathEscape(assistID)+"/reviewer-action", action,
	)
	return err
}

func (c *Client) RetryAgentAssist(
	ctx context.Context,
	assistID, reason string,
	acknowledgeAtLeastOnce bool,
) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/agent-assists/"+url.PathEscape(assistID)+"/retry",
		map[string]any{
			"reason":                    reason,
			"acknowledge_at_least_once": acknowledgeAtLeastOnce,
		},
	)
	return err
}

func (c *Client) CancelAgentAssist(
	ctx context.Context,
	assistID, reason string,
) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/agent-assists/"+url.PathEscape(assistID)+"/cancel",
		map[string]string{"reason": reason},
	)
	return err
}

func (c *Client) ListAgentToolApprovals(
	ctx context.Context,
) ([]AgentToolApproval, error) {
	out, err := do[struct {
		Approvals []AgentToolApproval `json:"approvals"`
	}](ctx, c, http.MethodGet, "/v1/agent-tool-approvals", nil)
	return out.Approvals, err
}

func (c *Client) GetAgentToolApproval(
	ctx context.Context,
	approvalID string,
) (AgentToolApproval, error) {
	return do[AgentToolApproval](
		ctx, c, http.MethodGet,
		"/v1/agent-tool-approvals/"+url.PathEscape(approvalID), nil,
	)
}

func (c *Client) DecideAgentToolApproval(
	ctx context.Context,
	approvalID string,
	decision agentgovernance.ToolApprovalStatus,
	reason string,
) (string, error) {
	out, err := do[struct {
		Status string `json:"status"`
	}](
		ctx, c, http.MethodPost,
		"/v1/agent-tool-approvals/"+url.PathEscape(approvalID)+"/decision",
		map[string]any{"decision": decision, "reason": reason},
	)
	return out.Status, err
}

func (c *Client) ListAgentSafetyIncidents(
	ctx context.Context,
) ([]AgentSafetyIncident, error) {
	out, err := do[struct {
		Incidents []AgentSafetyIncident `json:"incidents"`
	}](ctx, c, http.MethodGet, "/v1/agent-safety-incidents", nil)
	return out.Incidents, err
}

func (c *Client) OpenAgentSafetyIncident(
	ctx context.Context,
	incident agentgovernance.SafetyIncidentOpened,
) (string, error) {
	out, err := do[struct {
		IncidentID string `json:"incident_id"`
	}](ctx, c, http.MethodPost, "/v1/agent-safety-incidents", incident)
	return out.IncidentID, err
}

func (c *Client) ResolveAgentSafetyIncident(
	ctx context.Context,
	incidentID, resolution string,
) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/agent-safety-incidents/"+url.PathEscape(incidentID)+"/resolve",
		map[string]string{"resolution": resolution},
	)
	return err
}

func (c *Client) AgentGovernanceAnalytics(ctx context.Context) (AgentAnalytics, error) {
	return do[AgentAnalytics](
		ctx, c, http.MethodGet, "/v1/agent-governance/analytics", nil,
	)
}

func agentReleasePath(templateID string, release int) string {
	return "/v1/agent-templates/" + url.PathEscape(templateID) +
		"/releases/" + strconv.Itoa(release)
}
