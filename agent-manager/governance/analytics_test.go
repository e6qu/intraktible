// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	casedomain "github.com/e6qu/intraktible/case-manager/domain"
	engine "github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func TestAgentAnalyticsJoinsHumanQAOutcomeCostAndLineage(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	put := func(collection, key string, value any) {
		t.Helper()
		if err := store.PutDoc(ctx, st, collection, key, value); err != nil {
			t.Fatal(err)
		}
	}
	put(CollectionTemplates, store.Key(id.Org, id.Workspace, "template-1"), TemplateView{
		Org: id.Org, Workspace: id.Workspace,
		Template: Template{
			TemplateID: "template-1", Slug: "case-copilot",
			Name: "Case copilot", Task: "Cited summary",
		},
	})
	spec := testReleaseSpec()
	put(
		CollectionReleases,
		releaseKey(id.Org, id.Workspace, "template-1", 2),
		ReleaseView{
			Org: id.Org, Workspace: id.Workspace, TemplateID: "template-1",
			Release: 2, Status: ReleaseApproved, Spec: spec,
		},
	)
	put(
		CollectionDeployments,
		store.Key(id.Org, id.Workspace, "deployment-1"),
		DeploymentView{
			Org: id.Org, Workspace: id.Workspace, DeploymentID: "deployment-1",
			TemplateID: "template-1", Release: 2, Environment: engine.EnvProduction,
			Status: DeploymentActive,
		},
	)
	action := ReviewerAction{
		AssistID: "assist-1", Action: AssistEdited, TimeSavedMS: 1_200,
		Final: json.RawMessage(`{"summary":"reviewed"}`),
	}
	put(CollectionAssists, store.Key(id.Org, id.Workspace, "assist-1"), AssistView{
		Org: id.Org, Workspace: id.Workspace, AssistID: "assist-1", CaseID: "case-1",
		TemplateID: "template-1", Release: 2, Environment: engine.EnvProduction,
		Status: "completed", Action: &action, RequestedAt: now,
		Result: &AssistResult{
			PromptTokens: 100, OutputTokens: 40, CostUSD: 0.0125, LatencyMS: 250,
			ToolCalls: []ToolExecution{{Mode: ToolHumanBeforeCall}},
		},
	})
	put(CollectionAssists, store.Key(id.Org, id.Workspace, "assist-2"), AssistView{
		Org: id.Org, Workspace: id.Workspace, AssistID: "assist-2", CaseID: "case-2",
		TemplateID: "template-1", Release: 2, Environment: engine.EnvProduction,
		Status: "failed", RequestedAt: now.Add(time.Minute),
	})
	put(cases.Collection, store.Key(id.Org, id.Workspace, "case-1"), cases.CaseView{
		Org: id.Org, Workspace: id.Workspace, CaseID: "case-1",
		CaseType: "underwriting", Jurisdiction: "US", Disposition: "approve",
		Status: casedomain.StatusCompleted,
		QA: &cases.QAReview{
			Status: "completed", Agreement: true, Validated: true,
		},
	})
	put(cases.Collection, store.Key(id.Org, id.Workspace, "case-2"), cases.CaseView{
		Org: id.Org, Workspace: id.Workspace, CaseID: "case-2",
		CaseType: "underwriting", Jurisdiction: "US",
		Status: casedomain.StatusInProgress,
	})
	put(CollectionCampaigns, store.Key(id.Org, id.Workspace, "campaign-1"), CampaignView{
		Org: id.Org, Workspace: id.Workspace,
		CampaignResult: CampaignResult{
			CampaignID: "campaign-1", TemplateID: "template-1", Release: 2,
		},
	})
	put(CollectionIncidents, store.Key(id.Org, id.Workspace, "incident-1"), IncidentView{
		Org: id.Org, Workspace: id.Workspace, IncidentID: "incident-1",
		TemplateID: "template-1", Release: 2, Status: "open",
	})

	report, err := BuildAgentAnalytics(ctx, st, id)
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Assists != 2 || report.Totals.Completed != 1 ||
		report.Totals.Failed != 1 || report.Totals.Edited != 1 ||
		report.Totals.AdoptionRate != 1 || report.Totals.QAAgreementRate != 1 ||
		report.Totals.ValidatedOutcomes != 1 || report.Totals.MissingOutcomes != 0 ||
		report.Totals.CostUSD != 0.0125 || report.Totals.P95LatencyMS != 250 ||
		report.Totals.HumanToolExecutions != 1 {
		t.Fatalf("analytics totals = %+v", report.Totals)
	}
	if len(report.Groups) != 1 || report.Groups[0].Campaigns != 1 ||
		report.Groups[0].OpenIncidents != 1 ||
		report.Groups[0].DeploymentState != DeploymentActive {
		t.Fatalf("analytics groups = %+v", report.Groups)
	}
	if len(report.Segments) != 1 || report.Segments[0].CaseType != "underwriting" ||
		report.Segments[0].Jurisdiction != "US" {
		t.Fatalf("analytics segments = %+v", report.Segments)
	}
	if report.Totals.AdoptionCILow <= 0 || report.Totals.AdoptionCIHigh != 1 {
		t.Fatalf(
			"adoption confidence interval = [%f,%f]",
			report.Totals.AdoptionCILow, report.Totals.AdoptionCIHigh,
		)
	}
}

func TestAgentAnalyticsShowsDeployedReleaseWithZeroDenominators(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}
	if err := store.PutDoc(
		ctx, st, CollectionDeployments, store.Key(id.Org, id.Workspace, "deployment-1"),
		DeploymentView{
			Org: id.Org, Workspace: id.Workspace, DeploymentID: "deployment-1",
			TemplateID: "template-1", Release: 1, Environment: engine.EnvSandbox,
			Status: DeploymentActive,
		},
	); err != nil {
		t.Fatal(err)
	}
	report, err := BuildAgentAnalytics(ctx, st, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Groups) != 1 || report.Groups[0].Metrics.Assists != 0 ||
		report.Groups[0].Metrics.AdoptionRate != 0 {
		t.Fatalf("zero-denominator report = %+v", report)
	}
}

func TestAgentAnalyticsEmptyWorkspaceUsesJSONArrays(t *testing.T) {
	report, err := BuildAgentAnalytics(
		context.Background(),
		store.NewMemory(),
		identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Groups   []json.RawMessage `json:"groups"`
		Segments []json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Groups == nil || wire.Segments == nil {
		t.Fatalf("empty analytics arrays encoded as null: %s", body)
	}
}
