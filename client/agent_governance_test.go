// SPDX-License-Identifier: AGPL-3.0-or-later

package client_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	agentgovernance "github.com/e6qu/intraktible/agent-manager/governance"
	"github.com/e6qu/intraktible/client"
)

type governanceRoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip governanceRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestGoSDKGovernedAgentPathsAndIdempotency(t *testing.T) {
	var paths []string
	transport := governanceRoundTrip(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.Method+" "+request.URL.EscapedPath())
		body := `{}`
		switch request.URL.Path {
		case "/v1/agent-templates":
			body = `{"templates":[{"template_id":"template/1","slug":"case-assist","name":"Case assist","task":"summary"}]}`
		case "/v1/agent-templates/template/1":
			body = `{"template_id":"template/1","slug":"case-assist","name":"Case assist","task":"summary"}`
		case "/v1/agent-templates/template/1/releases":
			body = `{"release":2,"spec_hash":"hash"}`
		case "/v1/agent-templates/template/1/releases/2":
			body = `{"template_id":"template/1","release":2,"status":"approved"}`
		case "/v1/agent-templates/template/1/releases/2/retire":
			body = `{"event_id":"retire","seq":2}`
		case "/v1/agent-eval-suites/suite/1/versions/2":
			body = `{"suite_id":"suite/1","version":2,"name":"Adversarial"}`
		case "/v1/agent-deployments/deployment/1":
			body = `{"deployment_id":"deployment/1","template_id":"template/1","release":2}`
		case "/v1/agent-assists/assist/1":
			body = `{"assist_id":"assist/1","case_id":"case/1","status":"failed"}`
		case "/v1/agent-tool-approvals/approval/1":
			body = `{"approval_id":"approval/1","assist_id":"assist/1","status":"pending"}`
		case "/v1/cases/case/1/agent-assists":
			if request.Header.Get("Idempotency-Key") != "assist-key" {
				t.Fatalf("assist idempotency header = %q", request.Header.Get("Idempotency-Key"))
			}
			body = `{"assist_id":"assist-1","status":"awaiting_tool_approval"}`
		case "/v1/agent-assists/assist/1/retry":
			body = `{"event_id":"retry","seq":3}`
		case "/v1/agent-assists/assist/1/cancel":
			body = `{"event_id":"cancel","seq":4}`
		case "/v1/agent-deployments/deployment/1/resume":
			body = `{"event_id":"resume","seq":5}`
		case "/v1/agent-tool-approvals/approval/1/decision":
			body = `{"status":"requested"}`
		case "/v1/agent-eval-campaigns/campaign/1/trials/attack/1/2/adjudication":
			body = `{"event_id":"event-1","seq":4}`
		case "/v1/agent-eval-comparisons":
			if request.URL.Query().Get("baseline_campaign_id") != "baseline" ||
				request.URL.Query().Get("challenger_campaign_id") != "challenger" {
				t.Fatalf("comparison query = %q", request.URL.RawQuery)
			}
			body = `{"baseline_campaign_id":"baseline","challenger_campaign_id":"challenger","rows":[]}`
		case "/v1/agent-eval-campaigns/campaign/1/export":
			body = "campaign,trial\n"
		case "/v1/agent-governance/analytics":
			body = `{"totals":{"assists":1},"groups":[],"segments":[]}`
		default:
			t.Fatalf("unexpected SDK request %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	sdk := client.New(
		"https://intraktible.example", "key",
		client.WithHTTPClient(&http.Client{Transport: transport}),
	)
	ctx := context.Background()
	templates, err := sdk.ListAgentTemplates(ctx)
	if err != nil || len(templates) != 1 {
		t.Fatalf("templates = %+v err=%v", templates, err)
	}
	template, err := sdk.GetAgentTemplate(ctx, "template/1")
	if err != nil || template.TemplateID != "template/1" {
		t.Fatalf("template = %+v err=%v", template, err)
	}
	spec := agentgovernance.ReleaseSpec{
		Instructions: "Cite evidence", Provider: "provider", Model: "model",
	}
	release, hash, err := sdk.CreateAgentRelease(ctx, "template/1", spec)
	if err != nil || release != 2 || hash != "hash" {
		t.Fatalf("release = %d hash=%q err=%v", release, hash, err)
	}
	releaseView, err := sdk.GetAgentRelease(ctx, "template/1", 2)
	if err != nil || releaseView.Release != 2 {
		t.Fatalf("release view = %+v err=%v", releaseView, err)
	}
	if err := sdk.RetireAgentRelease(ctx, "template/1", 2, "superseded"); err != nil {
		t.Fatal(err)
	}
	suite, err := sdk.GetAgentEvalSuite(ctx, "suite/1", 2)
	if err != nil || suite.Version != 2 {
		t.Fatalf("suite = %+v err=%v", suite, err)
	}
	deployment, err := sdk.GetAgentDeployment(ctx, "deployment/1")
	if err != nil || deployment.DeploymentID != "deployment/1" {
		t.Fatalf("deployment = %+v err=%v", deployment, err)
	}
	assist, err := sdk.GetAgentAssist(ctx, "assist/1")
	if err != nil || assist.AssistID != "assist/1" {
		t.Fatalf("assist view = %+v err=%v", assist, err)
	}
	approval, err := sdk.GetAgentToolApproval(ctx, "approval/1")
	if err != nil || approval.ApprovalID != "approval/1" {
		t.Fatalf("approval = %+v err=%v", approval, err)
	}
	assistID, status, err := sdk.RequestCaseAgentAssist(
		ctx, "case/1", client.AgentAssistRequest{
			Kind: agentgovernance.AssistSummary, TemplateID: "template/1", Release: 2,
			Environment: "production", EvidenceIDs: []string{"evidence-1"},
		}, "assist-key",
	)
	if err != nil || assistID != "assist-1" || status != "awaiting_tool_approval" {
		t.Fatalf("assist = %q/%q err=%v", assistID, status, err)
	}
	if err := sdk.RetryAgentAssist(
		ctx, "assist/1", "operator retry", true,
	); err != nil {
		t.Fatal(err)
	}
	if err := sdk.CancelAgentAssist(
		ctx, "assist/1", "reviewer no longer needs it",
	); err != nil {
		t.Fatal(err)
	}
	if err := sdk.ResumeAgentDeployment(
		ctx, "deployment/1", "critical incident resolved",
	); err != nil {
		t.Fatal(err)
	}
	if status, err = sdk.DecideAgentToolApproval(
		ctx, "approval/1", agentgovernance.ToolApprovalApproved, "necessary",
	); err != nil || status != "requested" {
		t.Fatalf("approval status = %q err=%v", status, err)
	}
	if err := sdk.AdjudicateAgentCampaignTrial(
		ctx, "campaign/1", "attack/1", 2, true, "semantically equivalent",
	); err != nil {
		t.Fatal(err)
	}
	comparison, err := sdk.CompareAgentCampaigns(ctx, "baseline", "challenger")
	if err != nil || comparison.BaselineCampaignID != "baseline" {
		t.Fatalf("comparison = %+v err=%v", comparison, err)
	}
	exported, err := sdk.ExportAgentCampaign(ctx, "campaign/1", "csv")
	if err != nil || string(exported) != "campaign,trial\n" {
		t.Fatalf("export = %q err=%v", exported, err)
	}
	report, err := sdk.AgentGovernanceAnalytics(ctx)
	if err != nil || report.Totals.Assists != 1 {
		t.Fatalf("analytics = %+v err=%v", report, err)
	}
	want := []string{
		"GET /v1/agent-templates",
		"GET /v1/agent-templates/template%2F1",
		"POST /v1/agent-templates/template%2F1/releases",
		"GET /v1/agent-templates/template%2F1/releases/2",
		"POST /v1/agent-templates/template%2F1/releases/2/retire",
		"GET /v1/agent-eval-suites/suite%2F1/versions/2",
		"GET /v1/agent-deployments/deployment%2F1",
		"GET /v1/agent-assists/assist%2F1",
		"GET /v1/agent-tool-approvals/approval%2F1",
		"POST /v1/cases/case%2F1/agent-assists",
		"POST /v1/agent-assists/assist%2F1/retry",
		"POST /v1/agent-assists/assist%2F1/cancel",
		"POST /v1/agent-deployments/deployment%2F1/resume",
		"POST /v1/agent-tool-approvals/approval%2F1/decision",
		"POST /v1/agent-eval-campaigns/campaign%2F1/trials/attack%2F1/2/adjudication",
		"GET /v1/agent-eval-comparisons",
		"GET /v1/agent-eval-campaigns/campaign%2F1/export",
		"GET /v1/agent-governance/analytics",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths = %q, want %q", paths, want)
	}
}
