// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/e6qu/intraktible/case-manager/cases"
	engine "github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// QualityMetrics joins governed assist evidence to the accountable human action
// and the case's independent QA/outcome state. Every rate carries its
// denominator; missing outcomes remain visible rather than being silently
// excluded from the quality story.
type QualityMetrics struct {
	Assists              int     `json:"assists"`
	Completed            int     `json:"completed"`
	Failed               int     `json:"failed"`
	AwaitingToolApproval int     `json:"awaiting_tool_approval"`
	AwaitingResult       int     `json:"awaiting_result"`
	Actioned             int     `json:"actioned"`
	Accepted             int     `json:"accepted"`
	Edited               int     `json:"edited"`
	Rejected             int     `json:"rejected"`
	Escalated            int     `json:"escalated"`
	Adopted              int     `json:"adopted"`
	AdoptionRate         float64 `json:"adoption_rate"`
	AdoptionCILow        float64 `json:"adoption_ci_low"`
	AdoptionCIHigh       float64 `json:"adoption_ci_high"`
	QAAssessed           int     `json:"qa_assessed"`
	QAAgreed             int     `json:"qa_agreed"`
	QAAgreementRate      float64 `json:"qa_agreement_rate"`
	QAAgreementCILow     float64 `json:"qa_agreement_ci_low"`
	QAAgreementCIHigh    float64 `json:"qa_agreement_ci_high"`
	ValidatedOutcomes    int     `json:"validated_outcomes"`
	MissingOutcomes      int     `json:"missing_outcomes"`
	PromptTokens         int     `json:"prompt_tokens"`
	OutputTokens         int     `json:"output_tokens"`
	CostUSD              float64 `json:"cost_usd"`
	AverageLatencyMS     float64 `json:"average_latency_ms"`
	P95LatencyMS         int64   `json:"p95_latency_ms"`
	ToolExecutions       int     `json:"tool_executions"`
	HumanToolExecutions  int     `json:"human_tool_executions"`
	TimeSavedMS          int64   `json:"time_saved_ms"`
	AverageTimeSavedMS   float64 `json:"average_time_saved_ms"`
}

type AgentAnalyticsGroup struct {
	TemplateID      string             `json:"template_id"`
	TemplateName    string             `json:"template_name"`
	Task            string             `json:"task"`
	Release         int                `json:"release"`
	Provider        string             `json:"provider"`
	Model           string             `json:"model"`
	Environment     engine.Environment `json:"environment"`
	DeploymentID    string             `json:"deployment_id,omitempty"`
	DeploymentState DeploymentStatus   `json:"deployment_status,omitempty"`
	Campaigns       int                `json:"campaigns"`
	BlockingRuns    int                `json:"blocking_campaigns"`
	OpenIncidents   int                `json:"open_incidents"`
	Metrics         QualityMetrics     `json:"metrics"`
}

type AgentAnalyticsSegment struct {
	CaseType     string         `json:"case_type"`
	Jurisdiction string         `json:"jurisdiction,omitempty"`
	Metrics      QualityMetrics `json:"metrics"`
}

type AgentAnalyticsReport struct {
	Totals   QualityMetrics          `json:"totals"`
	Groups   []AgentAnalyticsGroup   `json:"groups"`
	Segments []AgentAnalyticsSegment `json:"segments"`
}

type qualityAccumulator struct {
	metrics   QualityMetrics
	latencies []int64
}

type analyticsGroupAccumulator struct {
	group AgentAnalyticsGroup
	data  qualityAccumulator
}

// BuildAgentAnalytics derives the operator report from replayable projections.
// It creates no reporting truth of its own: rebuilding cases and governance
// projections reproduces the same joins.
func BuildAgentAnalytics(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) (AgentAnalyticsReport, error) {
	assists, err := ListAssists(ctx, st, id)
	if err != nil {
		return AgentAnalyticsReport{}, fmt.Errorf("agent analytics: list assists: %w", err)
	}
	caseViews, err := cases.List(ctx, st, id, cases.Filter{})
	if err != nil {
		return AgentAnalyticsReport{}, fmt.Errorf("agent analytics: list cases: %w", err)
	}
	templates, err := ListTemplates(ctx, st, id)
	if err != nil {
		return AgentAnalyticsReport{}, fmt.Errorf("agent analytics: list templates: %w", err)
	}
	releases, err := store.QueryDocs[ReleaseView](
		ctx, st, CollectionReleases, store.Key(id.Org, id.Workspace, ""), nil, nil,
	)
	if err != nil {
		return AgentAnalyticsReport{}, fmt.Errorf("agent analytics: list releases: %w", err)
	}
	deployments, err := ListDeployments(ctx, st, id)
	if err != nil {
		return AgentAnalyticsReport{}, fmt.Errorf("agent analytics: list deployments: %w", err)
	}
	campaigns, err := store.QueryDocs[CampaignView](
		ctx, st, CollectionCampaigns, store.Key(id.Org, id.Workspace, ""), nil, nil,
	)
	if err != nil {
		return AgentAnalyticsReport{}, fmt.Errorf("agent analytics: list campaigns: %w", err)
	}
	incidents, err := ListIncidents(ctx, st, id)
	if err != nil {
		return AgentAnalyticsReport{}, fmt.Errorf("agent analytics: list incidents: %w", err)
	}

	caseByID := make(map[string]cases.CaseView, len(caseViews))
	for _, view := range caseViews {
		caseByID[view.CaseID] = view
	}
	templateByID := make(map[string]TemplateView, len(templates))
	for _, template := range templates {
		templateByID[template.TemplateID] = template
	}
	releaseByRef := make(map[string]ReleaseView, len(releases))
	for _, release := range releases {
		releaseByRef[releaseRef(release.TemplateID, release.Release)] = release
	}

	groups := make(map[string]*analyticsGroupAccumulator)
	segments := make(map[string]*qualityAccumulator)
	for _, deployment := range deployments {
		ref := releaseRef(deployment.TemplateID, deployment.Release)
		release := releaseByRef[ref]
		template := templateByID[deployment.TemplateID]
		key := ref + "\x00" + string(deployment.Environment)
		groups[key] = &analyticsGroupAccumulator{group: AgentAnalyticsGroup{
			TemplateID: deployment.TemplateID, TemplateName: template.Name,
			Task: template.Task, Release: deployment.Release,
			Provider: release.Spec.Provider, Model: release.Spec.Model,
			Environment: deployment.Environment, DeploymentID: deployment.DeploymentID,
			DeploymentState: deployment.Status,
		}}
	}
	var totals qualityAccumulator
	for _, assist := range assists {
		ref := releaseRef(assist.TemplateID, assist.Release)
		release := releaseByRef[ref]
		template := templateByID[assist.TemplateID]
		key := ref + "\x00" + string(assist.Environment)
		group := groups[key]
		if group == nil {
			group = &analyticsGroupAccumulator{group: AgentAnalyticsGroup{
				TemplateID: assist.TemplateID, TemplateName: template.Name,
				Task: template.Task, Release: assist.Release,
				Provider: release.Spec.Provider, Model: release.Spec.Model,
				Environment: assist.Environment,
			}}
			groups[key] = group
		}
		caseView := caseByID[assist.CaseID]
		addAssist(&totals, assist, caseView)
		addAssist(&group.data, assist, caseView)
		caseType := caseView.CaseType
		if caseType == "" {
			caseType = "unknown"
		}
		segmentKey := caseType + "\x00" + caseView.Jurisdiction
		segment := segments[segmentKey]
		if segment == nil {
			segment = &qualityAccumulator{}
			segments[segmentKey] = segment
		}
		addAssist(segment, assist, caseView)
	}

	for _, deployment := range deployments {
		key := releaseRef(deployment.TemplateID, deployment.Release) +
			"\x00" + string(deployment.Environment)
		if group := groups[key]; group != nil {
			group.group.DeploymentID = deployment.DeploymentID
			group.group.DeploymentState = deployment.Status
		}
	}
	for _, campaign := range campaigns {
		ref := releaseRef(campaign.TemplateID, campaign.Release)
		for key, group := range groups {
			if len(key) >= len(ref)+1 && key[:len(ref)+1] == ref+"\x00" {
				group.group.Campaigns++
				if campaign.Assessment.Blocking {
					group.group.BlockingRuns++
				}
			}
		}
	}
	for _, incident := range incidents {
		if incident.Status != "open" {
			continue
		}
		ref := releaseRef(incident.TemplateID, incident.Release)
		for key, group := range groups {
			if len(key) >= len(ref)+1 && key[:len(ref)+1] == ref+"\x00" {
				group.group.OpenIncidents++
			}
		}
	}

	report := AgentAnalyticsReport{
		Totals: totals.finish(), Groups: []AgentAnalyticsGroup{}, Segments: []AgentAnalyticsSegment{},
	}
	for _, accumulator := range groups {
		accumulator.group.Metrics = accumulator.data.finish()
		report.Groups = append(report.Groups, accumulator.group)
	}
	sort.Slice(report.Groups, func(i, j int) bool {
		if report.Groups[i].TemplateID == report.Groups[j].TemplateID {
			if report.Groups[i].Release == report.Groups[j].Release {
				return report.Groups[i].Environment < report.Groups[j].Environment
			}
			return report.Groups[i].Release > report.Groups[j].Release
		}
		return report.Groups[i].TemplateID < report.Groups[j].TemplateID
	})
	for key, accumulator := range segments {
		separator := 0
		for separator < len(key) && key[separator] != 0 {
			separator++
		}
		report.Segments = append(report.Segments, AgentAnalyticsSegment{
			CaseType: key[:separator], Jurisdiction: key[separator+1:],
			Metrics: accumulator.finish(),
		})
	}
	sort.Slice(report.Segments, func(i, j int) bool {
		if report.Segments[i].CaseType == report.Segments[j].CaseType {
			return report.Segments[i].Jurisdiction < report.Segments[j].Jurisdiction
		}
		return report.Segments[i].CaseType < report.Segments[j].CaseType
	})
	return report, nil
}

func addAssist(
	accumulator *qualityAccumulator,
	assist AssistView,
	caseView cases.CaseView,
) {
	metrics := &accumulator.metrics
	metrics.Assists++
	switch assist.Status {
	case "completed":
		metrics.Completed++
	case "failed":
		metrics.Failed++
	case "awaiting_tool_approval":
		metrics.AwaitingToolApproval++
	default:
		metrics.AwaitingResult++
	}
	if assist.Result != nil {
		metrics.PromptTokens += assist.Result.PromptTokens
		metrics.OutputTokens += assist.Result.OutputTokens
		metrics.CostUSD += assist.Result.CostUSD
		accumulator.latencies = append(accumulator.latencies, assist.Result.LatencyMS)
		metrics.ToolExecutions += len(assist.Result.ToolCalls)
		for _, execution := range assist.Result.ToolCalls {
			if execution.Mode == ToolHumanBeforeCall {
				metrics.HumanToolExecutions++
			}
		}
	}
	if assist.Action != nil {
		metrics.Actioned++
		metrics.TimeSavedMS += assist.Action.TimeSavedMS
		switch assist.Action.Action {
		case AssistAccepted:
			metrics.Accepted++
			metrics.Adopted++
		case AssistEdited:
			metrics.Edited++
			metrics.Adopted++
		case AssistRejected:
			metrics.Rejected++
		case AssistEscalated:
			metrics.Escalated++
		}
	}
	if assist.Status != AssistCompletedStatus {
		return
	}
	if caseView.QA != nil && caseView.QA.Status == "completed" {
		metrics.QAAssessed++
		if caseView.QA.Agreement {
			metrics.QAAgreed++
		}
	}
	if caseView.QA != nil && caseView.QA.Validated {
		metrics.ValidatedOutcomes++
	} else {
		metrics.MissingOutcomes++
	}
}

func (accumulator qualityAccumulator) finish() QualityMetrics {
	metrics := accumulator.metrics
	metrics.AdoptionRate, metrics.AdoptionCILow, metrics.AdoptionCIHigh =
		rateWithWilson(metrics.Adopted, metrics.Actioned)
	metrics.QAAgreementRate, metrics.QAAgreementCILow, metrics.QAAgreementCIHigh =
		rateWithWilson(metrics.QAAgreed, metrics.QAAssessed)
	if len(accumulator.latencies) > 0 {
		var sum int64
		for _, latency := range accumulator.latencies {
			sum += latency
		}
		metrics.AverageLatencyMS = float64(sum) / float64(len(accumulator.latencies))
		sort.Slice(accumulator.latencies, func(i, j int) bool {
			return accumulator.latencies[i] < accumulator.latencies[j]
		})
		index := int(math.Ceil(0.95*float64(len(accumulator.latencies)))) - 1
		metrics.P95LatencyMS = accumulator.latencies[index]
	}
	if metrics.Actioned > 0 {
		metrics.AverageTimeSavedMS = float64(metrics.TimeSavedMS) / float64(metrics.Actioned)
	}
	return metrics
}

func rateWithWilson(successes, total int) (float64, float64, float64) {
	if total == 0 {
		return 0, 0, 0
	}
	rate := float64(successes) / float64(total)
	const z = 1.959963984540054
	denominator := 1 + z*z/float64(total)
	center := (rate + z*z/(2*float64(total))) / denominator
	margin := z * math.Sqrt(
		rate*(1-rate)/float64(total)+z*z/(4*float64(total*total)),
	) / denominator
	return rate, math.Max(0, center-margin), math.Min(1, center+margin)
}
