// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications_test

import (
	"context"
	"testing"
	"time"

	agentgovernance "github.com/e6qu/intraktible/agent-manager/governance"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestGovernedAgentReviewFailureAndIncidentNotifications(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	appendEvent := func(eventType string, payload any) {
		t.Helper()
		if _, err := eventlog.AppendJSON(
			ctx, log, "acme", "risk", "maker", agentgovernance.Stream,
			eventType, now, payload,
		); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	appendEvent(
		agentgovernance.TypeReleaseReviewRequested,
		agentgovernance.ReleaseReviewRequestedEvent{
			RequestID: "review-1", TemplateID: "copilot", Release: 2,
			CampaignIDs: []string{"campaign-1"}, Reviewers: []string{"checker"},
			RequestedBy: "maker", RequestedAt: now, ExpiresAt: now.Add(time.Hour),
		},
	)
	appendEvent(
		agentgovernance.TypeReleaseReviewed,
		agentgovernance.ReleaseReviewed{
			RequestID: "review-1", TemplateID: "copilot", Release: 2,
			Decision: agentgovernance.ReviewApprove, Reason: "passed",
			ReviewedBy: "checker", ReviewedAt: now,
		},
	)
	appendEvent(
		agentgovernance.TypeAssistRequested,
		agentgovernance.AssistRequested{
			AssistID: "assist-1", CaseID: "case-1",
			TemplateID: "copilot", Release: 2, RequestedBy: "reviewer",
		},
	)
	appendEvent(
		agentgovernance.TypeAssistFailed,
		agentgovernance.AssistFailed{
			AssistID: "assist-1", Reason: "provider refused", FailedAt: now,
		},
	)
	appendEvent(
		agentgovernance.TypeSafetyIncidentOpened,
		agentgovernance.SafetyIncidentOpened{
			IncidentID: "incident-1", TemplateID: "copilot", Release: 2,
			Kind: "prompt_injection", Severity: agentgovernance.SeverityCritical,
			Summary: "attempted policy override", OpenedAt: now,
		},
	)
	st := store.NewMemory()
	if _, err := projection.New(log, st, notifications.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	checker, err := notifications.List(
		ctx, st,
		identity.Identity{Org: "acme", Workspace: "risk", Actor: "checker"},
		notifications.Access{Approvals: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(checker) != 1 || checker[0].Kind != notifications.KindAlert ||
		checker[0].SubjectType != "agent_release" {
		t.Fatalf("checker notifications = %+v", checker)
	}
	reviewer, err := notifications.List(
		ctx, st,
		identity.Identity{Org: "acme", Workspace: "risk", Actor: "reviewer"},
		notifications.Access{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviewer) != 1 || reviewer[0].SubjectType != "agent_assist" {
		t.Fatalf("reviewer notifications = %+v", reviewer)
	}
	operator, err := notifications.List(
		ctx, st,
		identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"},
		notifications.Access{OperatorAlerts: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(operator) != 1 || operator[0].SubjectType != "agent_incident" {
		t.Fatalf("operator notifications = %+v", operator)
	}
}

func TestPolicyRequestedAssistFailureGoesToOperatorQueue(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 12, 30, 0, 0, time.UTC)
	appendEvent := func(eventType string, payload any) {
		t.Helper()
		if _, err := eventlog.AppendJSON(
			ctx, log, "acme", "risk", "sla-sweeper", agentgovernance.Stream,
			eventType, now, payload,
		); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	appendEvent(
		agentgovernance.TypeAssistRequested,
		agentgovernance.AssistRequested{
			AssistID: "assist-policy", CaseID: "case-1",
			TemplateID: "copilot", Release: 2, RequestedBy: "sla-sweeper",
			PolicySource: &agentgovernance.AssistPolicySource{
				Kind: "case_type", Key: "edd", ConfigurationSeq: 41,
				PolicyKey:           "opening_summary",
				EvidenceFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
	)
	appendEvent(
		agentgovernance.TypeAssistFailed,
		agentgovernance.AssistFailed{
			AssistID: "assist-policy", Reason: "provider refused", FailedAt: now,
		},
	)
	st := store.NewMemory()
	if _, err := projection.New(log, st, notifications.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	operator, err := notifications.List(
		ctx, st,
		identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"},
		notifications.Access{OperatorAlerts: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(operator) != 1 || operator[0].SubjectID != "case-1:assist-policy" {
		t.Fatalf("operator notifications = %+v", operator)
	}
	serviceActor, err := notifications.List(
		ctx, st,
		identity.Identity{Org: "acme", Workspace: "risk", Actor: "sla-sweeper"},
		notifications.Access{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(serviceActor) != 0 {
		t.Fatalf("scheduler actor received human action: %+v", serviceActor)
	}
}

func TestAgentAssistDeadLetterAlertResolvesOnExplicitRetry(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	appendEvent := func(eventType string, payload any) eventlog.Envelope {
		t.Helper()
		event, err := eventlog.AppendJSON(
			ctx, log, "acme", "risk", "worker", agentgovernance.Stream,
			eventType, now, payload,
		)
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
		return event
	}
	appendEvent(
		agentgovernance.TypeAssistRequested,
		agentgovernance.AssistRequested{
			AssistID: "assist-dead", CaseID: "case-1",
			TemplateID: "copilot", Release: 2, RequestedBy: "reviewer",
		},
	)
	appendEvent(
		agentgovernance.TypeAssistDeadLettered,
		agentgovernance.AssistDeadLettered{
			AssistID: "assist-dead", Attempt: 1,
			Reason: "worker lease expired", At: now,
		},
	)
	st := store.NewMemory()
	if _, err := projection.New(log, st, notifications.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	reviewerID := identity.Identity{
		Org: "acme", Workspace: "risk", Actor: "reviewer",
	}
	inbox, err := notifications.List(ctx, st, reviewerID, notifications.Access{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].SubjectID != "case-1:assist-dead" {
		t.Fatalf("dead-letter inbox = %+v", inbox)
	}
	retry := appendEvent(
		agentgovernance.TypeAssistRetryRequested,
		agentgovernance.AssistRetryRequested{
			AssistID: "assist-dead", Reason: "operator accepts at-least-once",
			RequestedBy: "reviewer", AcknowledgeAtLeastOnce: true, At: now,
		},
	)
	if err := (notifications.Projector{}).Apply(ctx, retry, st); err != nil {
		t.Fatal(err)
	}
	inbox, err = notifications.List(ctx, st, reviewerID, notifications.Access{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 0 {
		t.Fatalf("retry left actionable dead-letter alerts: %+v", inbox)
	}
}

func TestAgentCampaignAdjudicationResolvesBlockingAlert(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	appendEvent := func(eventType string, payload any) {
		t.Helper()
		if _, err := eventlog.AppendJSON(
			ctx, log, "acme", "risk", "validator", agentgovernance.Stream,
			eventType, now, payload,
		); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	appendEvent(
		agentgovernance.TypeCampaignRecorded,
		agentgovernance.CampaignRecorded{Result: agentgovernance.CampaignResult{
			CampaignID: "campaign-1", TemplateID: "copilot", Release: 2,
			SuiteID: "safety", SuiteVersion: 1, Blocking: true,
		}},
	)
	appendEvent(
		agentgovernance.TypeCampaignTrialAdjudicated,
		agentgovernance.CampaignTrialAdjudicated{
			Adjudication: agentgovernance.TrialAdjudication{
				CampaignID: "campaign-1", CaseID: "attack", Trial: 1,
				Passed: true, Reason: "Equivalent refusal",
				AdjudicatedBy: "validator", AdjudicatedAt: now,
			},
			TemplateID: "copilot", Release: 2,
			PreviousAssessment: agentgovernance.CampaignAssessment{Blocking: true},
			Assessment:         agentgovernance.CampaignAssessment{Blocking: false},
		},
	)
	st := store.NewMemory()
	if _, err := projection.New(log, st, notifications.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	operator, err := notifications.List(
		ctx, st,
		identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"},
		notifications.Access{OperatorAlerts: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(operator) != 0 {
		t.Fatalf("resolved campaign alert remained visible: %+v", operator)
	}
}
