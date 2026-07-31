// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	agentgovernance "github.com/e6qu/intraktible/agent-manager/governance"
	"github.com/e6qu/intraktible/client"
)

type agentConnectionFlags struct {
	serverURL *string
	apiKey    *string
}

func bindAgentConnection(fs *flag.FlagSet) agentConnectionFlags {
	return agentConnectionFlags{
		serverURL: fs.String("server", "http://localhost:8080", "intraktible server URL"),
		apiKey:    fs.String("api-key", os.Getenv("INTRAKTIBLE_API_KEY"), "API key"),
	}
}

func (flags agentConnectionFlags) client() *client.Client {
	return client.New(*flags.serverURL, *flags.apiKey)
}

func agentCreateFromFile[Request, Result any](
	command, fileDescription string,
	args []string,
	create func(*client.Client, Request) (Result, error),
) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	file := fs.String("file", "", fileDescription)
	if err := fs.Parse(args); err != nil {
		return err
	}
	request, err := readAgentJSON[Request](*file)
	if err != nil {
		return err
	}
	result, err := create(connection.client(), request)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func agentsCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(
			"agents: command required (templates|register|releases|create-release|suites|" +
				"publish-suite|evaluate|campaigns|adjudicate|compare|export-campaign|" +
				"request-review|review|deployments|deploy|activate|" +
				"pause|resume|rollback|assists|assist|retry-assist|cancel-assist|feedback|" +
				"approvals|tool-decision|incidents|" +
				"open-incident|resolve-incident|analytics)",
		)
	}
	switch args[0] {
	case "templates":
		return agentListTemplates(args[1:])
	case "register":
		return agentRegisterTemplate(args[1:])
	case "releases":
		return agentListReleases(args[1:])
	case "create-release":
		return agentCreateRelease(args[1:])
	case "suites":
		return agentListSuites(args[1:])
	case "publish-suite":
		return agentPublishSuite(args[1:])
	case "evaluate":
		return agentEvaluate(args[1:])
	case "campaigns":
		return agentListCampaigns(args[1:])
	case "adjudicate":
		return agentAdjudicate(args[1:])
	case "compare":
		return agentCompare(args[1:])
	case "export-campaign":
		return agentExportCampaign(args[1:])
	case "request-review":
		return agentRequestReview(args[1:])
	case "review":
		return agentReview(args[1:])
	case "deployments":
		return agentListDeployments(args[1:])
	case "deploy":
		return agentDeploy(args[1:])
	case "activate", "pause", "resume", "rollback":
		return agentDeploymentAction(args[0], args[1:])
	case "assists":
		return agentListAssists(args[1:])
	case "assist":
		return agentRequestAssist(args[1:])
	case "retry-assist":
		return agentRetryAssist(args[1:])
	case "cancel-assist":
		return agentCancelAssist(args[1:])
	case "feedback":
		return agentFeedback(args[1:])
	case "approvals":
		return agentListApprovals(args[1:])
	case "tool-decision":
		return agentToolDecision(args[1:])
	case "incidents":
		return agentListIncidents(args[1:])
	case "open-incident":
		return agentOpenIncident(args[1:])
	case "resolve-incident":
		return agentResolveIncident(args[1:])
	case "analytics":
		return agentAnalytics(args[1:])
	default:
		return fmt.Errorf("agents: unknown command %q", args[0])
	}
}

func agentListTemplates(args []string) error {
	fs := flag.NewFlagSet("agents templates", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ListAgentTemplates(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func agentRegisterTemplate(args []string) error {
	return agentCreateFromFile(
		"agents register", "agent template JSON file", args,
		func(sdk *client.Client, template agentgovernance.Template) (client.AgentTemplate, error) {
			return sdk.CreateAgentTemplate(context.Background(), template)
		},
	)
}

func agentListReleases(args []string) error {
	fs := flag.NewFlagSet("agents releases", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	templateID := fs.String("template", "", "agent template id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *templateID == "" {
		return errors.New("agents releases: --template is required")
	}
	items, err := connection.client().ListAgentReleases(context.Background(), *templateID)
	if err != nil {
		return err
	}
	return printJSON(items)
}

func agentCreateRelease(args []string) error {
	fs := flag.NewFlagSet("agents create-release", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	templateID := fs.String("template", "", "agent template id")
	file := fs.String("file", "", "immutable release spec JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *templateID == "" {
		return errors.New("agents create-release: --template is required")
	}
	spec, err := readAgentJSON[agentgovernance.ReleaseSpec](*file)
	if err != nil {
		return err
	}
	release, hash, err := connection.client().CreateAgentRelease(
		context.Background(), *templateID, spec,
	)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"release": release, "spec_hash": hash})
}

func agentListSuites(args []string) error {
	fs := flag.NewFlagSet("agents suites", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ListAgentEvalSuites(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func agentPublishSuite(args []string) error {
	return agentCreateFromFile(
		"agents publish-suite", "evaluation suite JSON file", args,
		func(
			sdk *client.Client,
			suite agentgovernance.EvalSuite,
		) (agentgovernance.EvalSuite, error) {
			return sdk.PublishAgentEvalSuite(context.Background(), suite)
		},
	)
}

func agentEvaluate(args []string) error {
	fs := flag.NewFlagSet("agents evaluate", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	templateID := fs.String("template", "", "agent template id")
	release := fs.Int("release", 0, "exact release")
	suiteID := fs.String("suite", "", "evaluation suite id")
	suiteVersion := fs.Int("suite-version", 0, "exact suite version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *templateID == "" || *release < 1 || *suiteID == "" || *suiteVersion < 1 {
		return errors.New(
			"agents evaluate: --template, positive --release, --suite, and positive --suite-version are required",
		)
	}
	result, err := connection.client().RunAgentCampaign(
		context.Background(), *templateID, *release, *suiteID, *suiteVersion,
	)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func agentListCampaigns(args []string) error {
	fs := flag.NewFlagSet("agents campaigns", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	templateID := fs.String("template", "", "agent template id")
	release := fs.Int("release", 0, "exact release")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *templateID == "" || *release < 1 {
		return errors.New("agents campaigns: --template and positive --release are required")
	}
	campaigns, err := connection.client().ListAgentCampaigns(
		context.Background(), *templateID, *release,
	)
	if err != nil {
		return err
	}
	return printJSON(campaigns)
}

func agentAdjudicate(args []string) error {
	fs := flag.NewFlagSet("agents adjudicate", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	campaignID := fs.String("campaign", "", "evaluation campaign id")
	caseID := fs.String("case", "", "evaluation case id")
	trial := fs.Int("trial", 0, "trial number")
	decision := fs.String("decision", "", "pass or fail")
	reason := fs.String("reason", "", "adjudication rationale")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *campaignID == "" || *caseID == "" || *trial < 1 ||
		(*decision != "pass" && *decision != "fail") || strings.TrimSpace(*reason) == "" {
		return errors.New(
			"agents adjudicate: --campaign, --case, positive --trial, pass|fail --decision, and --reason are required",
		)
	}
	return connection.client().AdjudicateAgentCampaignTrial(
		context.Background(), *campaignID, *caseID, *trial, *decision == "pass", *reason,
	)
}

func agentCompare(args []string) error {
	fs := flag.NewFlagSet("agents compare", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	baseline := fs.String("baseline", "", "baseline campaign id")
	challenger := fs.String("challenger", "", "challenger campaign id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *baseline == "" || *challenger == "" || *baseline == *challenger {
		return errors.New(
			"agents compare: distinct --baseline and --challenger campaigns are required",
		)
	}
	comparison, err := connection.client().CompareAgentCampaigns(
		context.Background(), *baseline, *challenger,
	)
	if err != nil {
		return err
	}
	return printJSON(comparison)
}

func agentExportCampaign(args []string) error {
	fs := flag.NewFlagSet("agents export-campaign", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	campaignID := fs.String("campaign", "", "evaluation campaign id")
	format := fs.String("format", "json", "json or csv")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *campaignID == "" || (*format != "json" && *format != "csv") {
		return errors.New(
			"agents export-campaign: --campaign and json|csv --format are required",
		)
	}
	document, err := connection.client().ExportAgentCampaign(
		context.Background(), *campaignID, *format,
	)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(document); err != nil {
		return fmt.Errorf("agents export-campaign: write stdout: %w", err)
	}
	return nil
}

func agentRequestReview(args []string) error {
	fs := flag.NewFlagSet("agents request-review", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	templateID := fs.String("template", "", "agent template id")
	release := fs.Int("release", 0, "exact release")
	campaigns := fs.String("campaigns", "", "comma-separated campaign ids")
	evidence := fs.String("evidence", "", "comma-separated evidence ids")
	reviewers := fs.String("reviewers", "", "comma-separated reviewer actor ids")
	expires := fs.Duration("expires-in", 24*time.Hour, "review lifetime")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *templateID == "" || *release < 1 || len(commaSeparated(*campaigns)) == 0 ||
		*expires <= 0 {
		return errors.New(
			"agents request-review: --template, positive --release, --campaigns, and positive --expires-in are required",
		)
	}
	requestID, err := connection.client().RequestAgentReleaseReview(
		context.Background(), *templateID, *release, commaSeparated(*campaigns),
		commaSeparated(*evidence), commaSeparated(*reviewers), time.Now().UTC().Add(*expires),
	)
	if err != nil {
		return err
	}
	return printJSON(map[string]string{"request_id": requestID})
}

func agentReview(args []string) error {
	fs := flag.NewFlagSet("agents review", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	templateID := fs.String("template", "", "agent template id")
	release := fs.Int("release", 0, "exact release")
	requestID := fs.String("request", "", "review request id")
	decision := fs.String("decision", "", "approve or reject")
	reason := fs.String("reason", "", "review rationale")
	if err := fs.Parse(args); err != nil {
		return err
	}
	reviewDecision := agentgovernance.ReviewDecision(*decision)
	if *templateID == "" || *release < 1 || *requestID == "" ||
		!reviewDecision.Valid() || strings.TrimSpace(*reason) == "" {
		return errors.New(
			"agents review: template/release/request, approve|reject decision, and reason are required",
		)
	}
	return connection.client().ReviewAgentRelease(
		context.Background(), *templateID, *release, *requestID, reviewDecision, *reason,
	)
}

func agentListDeployments(args []string) error {
	fs := flag.NewFlagSet("agents deployments", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ListAgentDeployments(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func agentDeploy(args []string) error {
	return agentCreateFromFile(
		"agents deploy", "deployment request JSON file", args,
		func(
			sdk *client.Client,
			request agentgovernance.DeploymentRequest,
		) (map[string]string, error) {
			deploymentID, err := sdk.RequestAgentDeployment(context.Background(), request)
			return map[string]string{"deployment_id": deploymentID}, err
		},
	)
}

func agentDeploymentAction(action string, args []string) error {
	fs := flag.NewFlagSet("agents "+action, flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	deploymentID := fs.String("deployment", "", "deployment id")
	reason := fs.String("reason", "", "operator rationale")
	toRelease := fs.Int("to-release", 0, "approved rollback release")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *deploymentID == "" {
		return fmt.Errorf("agents %s: --deployment is required", action)
	}
	switch action {
	case "activate":
		return connection.client().ActivateAgentDeployment(context.Background(), *deploymentID)
	case "pause":
		if strings.TrimSpace(*reason) == "" {
			return errors.New("agents pause: --reason is required")
		}
		return connection.client().PauseAgentDeployment(
			context.Background(), *deploymentID, *reason,
		)
	case "resume":
		if strings.TrimSpace(*reason) == "" {
			return errors.New("agents resume: --reason is required")
		}
		return connection.client().ResumeAgentDeployment(
			context.Background(), *deploymentID, *reason,
		)
	case "rollback":
		if *toRelease < 1 || strings.TrimSpace(*reason) == "" {
			return errors.New(
				"agents rollback: positive --to-release and --reason are required",
			)
		}
		return connection.client().RollbackAgentDeployment(
			context.Background(), *deploymentID, *toRelease, *reason,
		)
	default:
		return fmt.Errorf("agents: unsupported deployment action %q", action)
	}
}

func agentListAssists(args []string) error {
	fs := flag.NewFlagSet("agents assists", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	caseID := fs.String("case", "", "case id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caseID == "" {
		return errors.New("agents assists: --case is required")
	}
	items, err := connection.client().ListCaseAgentAssists(context.Background(), *caseID)
	if err != nil {
		return err
	}
	return printJSON(items)
}

func agentRequestAssist(args []string) error {
	fs := flag.NewFlagSet("agents assist", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	caseID := fs.String("case", "", "case id")
	file := fs.String("file", "", "case assist request JSON file")
	idempotencyKey := fs.String("idempotency-key", "", "stable logical request identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caseID == "" || strings.TrimSpace(*idempotencyKey) == "" {
		return errors.New("agents assist: --case and --idempotency-key are required")
	}
	request, err := readAgentJSON[client.AgentAssistRequest](*file)
	if err != nil {
		return err
	}
	assistID, status, err := connection.client().RequestCaseAgentAssist(
		context.Background(), *caseID, request, *idempotencyKey,
	)
	if err != nil {
		return err
	}
	return printJSON(map[string]string{"assist_id": assistID, "status": status})
}

func agentFeedback(args []string) error {
	fs := flag.NewFlagSet("agents feedback", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	assistID := fs.String("assist", "", "assist id")
	file := fs.String("file", "", "reviewer action JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *assistID == "" {
		return errors.New("agents feedback: --assist is required")
	}
	action, err := readAgentJSON[agentgovernance.ReviewerAction](*file)
	if err != nil {
		return err
	}
	return connection.client().RecordAgentAssistAction(
		context.Background(), *assistID, action,
	)
}

func agentRetryAssist(args []string) error {
	fs := flag.NewFlagSet("agents retry-assist", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	assistID := fs.String("assist", "", "assist id")
	reason := fs.String("reason", "", "operator retry rationale")
	acknowledge := fs.Bool(
		"acknowledge-at-least-once", false,
		"accept possible duplicate provider cost/effects after an indeterminate lease loss",
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*assistID) == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("agents retry-assist: --assist and --reason are required")
	}
	return connection.client().RetryAgentAssist(
		context.Background(), *assistID, *reason, *acknowledge,
	)
}

func agentCancelAssist(args []string) error {
	fs := flag.NewFlagSet("agents cancel-assist", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	assistID := fs.String("assist", "", "assist id")
	reason := fs.String("reason", "", "operator cancellation rationale")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*assistID) == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("agents cancel-assist: --assist and --reason are required")
	}
	return connection.client().CancelAgentAssist(
		context.Background(), *assistID, *reason,
	)
}

func agentListApprovals(args []string) error {
	fs := flag.NewFlagSet("agents approvals", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ListAgentToolApprovals(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func agentToolDecision(args []string) error {
	fs := flag.NewFlagSet("agents tool-decision", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	approvalID := fs.String("approval", "", "tool approval id")
	decision := fs.String("decision", "", "approved or rejected")
	reason := fs.String("reason", "", "decision rationale")
	if err := fs.Parse(args); err != nil {
		return err
	}
	status := agentgovernance.ToolApprovalStatus(*decision)
	if *approvalID == "" ||
		(status != agentgovernance.ToolApprovalApproved &&
			status != agentgovernance.ToolApprovalRejected) ||
		strings.TrimSpace(*reason) == "" {
		return errors.New(
			"agents tool-decision: --approval, approved|rejected --decision, and --reason are required",
		)
	}
	result, err := connection.client().DecideAgentToolApproval(
		context.Background(), *approvalID, status, *reason,
	)
	if err != nil {
		return err
	}
	return printJSON(map[string]string{"status": result})
}

func agentListIncidents(args []string) error {
	fs := flag.NewFlagSet("agents incidents", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := connection.client().ListAgentSafetyIncidents(context.Background())
	if err != nil {
		return err
	}
	return printJSON(items)
}

func agentOpenIncident(args []string) error {
	return agentCreateFromFile(
		"agents open-incident", "safety incident JSON file", args,
		func(
			sdk *client.Client,
			incident agentgovernance.SafetyIncidentOpened,
		) (map[string]string, error) {
			incidentID, err := sdk.OpenAgentSafetyIncident(context.Background(), incident)
			return map[string]string{"incident_id": incidentID}, err
		},
	)
}

func agentResolveIncident(args []string) error {
	fs := flag.NewFlagSet("agents resolve-incident", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	incidentID := fs.String("incident", "", "incident id")
	resolution := fs.String("resolution", "", "resolution evidence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *incidentID == "" || strings.TrimSpace(*resolution) == "" {
		return errors.New(
			"agents resolve-incident: --incident and --resolution are required",
		)
	}
	return connection.client().ResolveAgentSafetyIncident(
		context.Background(), *incidentID, *resolution,
	)
}

func agentAnalytics(args []string) error {
	fs := flag.NewFlagSet("agents analytics", flag.ContinueOnError)
	connection := bindAgentConnection(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	report, err := connection.client().AgentGovernanceAnalytics(context.Background())
	if err != nil {
		return err
	}
	return printJSON(report)
}

func readAgentJSON[T any](path string) (T, error) {
	var zero T
	if strings.TrimSpace(path) == "" {
		return zero, errors.New("agents: --file is required")
	}
	// #nosec G304 -- this local CLI command intentionally reads the exact path
	// supplied by its operator; it is not derived from a remote request.
	document, err := os.ReadFile(path)
	if err != nil {
		return zero, fmt.Errorf("agents: read %s: %w", path, err)
	}
	var value T
	if err := json.Unmarshal(document, &value); err != nil {
		return zero, fmt.Errorf("agents: decode %s: %w", path, err)
	}
	return value, nil
}
