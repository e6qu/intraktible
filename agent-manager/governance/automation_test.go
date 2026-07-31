// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	casedomain "github.com/e6qu/intraktible/case-manager/domain"
	engine "github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

func TestPolicyAssistWaitsThenPinsActiveReleaseAndEvidenceIdempotently(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "o", Workspace: "w", Actor: "maker"}
	checker := identity.Identity{Org: "o", Workspace: "w", Actor: "checker"}
	scheduler := identity.Identity{Org: "o", Workspace: "w", Actor: "sla-sweeper"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "case-copilot", Slug: "case-copilot",
		Name: "Case copilot", Task: "Cited summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	release := approveRelease(
		t, ctx, handler, maker, checker, template.TemplateID, suite,
	)
	deployment := DeploymentRequest{
		DeploymentID: "copilot-production", TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction, Reason: "approved",
	}
	if _, err := handler.RequestDeployment(ctx, scheduler, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, scheduler, deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	policy := casedomain.AssistAutomation{
		Key: "opening_summary", Kind: casedomain.CaseAssistSummary,
		TemplateID:           template.TemplateID,
		Environment:          casedomain.CaseAssistProduction,
		EvidenceRequirements: []string{"decision_record"},
	}
	source := AssistPolicySource{
		Kind: "case_type", Key: "edd", ConfigurationSeq: 42,
		PolicyKey: policy.Key,
	}
	view := cases.CaseView{
		Org: "o", Workspace: "w", CaseID: "case-1", CaseType: "edd",
		Subject: "applicant/1",
	}
	if _, err := handler.RequestPolicyAssist(
		ctx, scheduler, view, policy, source,
	); !errors.Is(err, ErrAssistPolicyWaiting) {
		t.Fatalf("missing evidence error = %v", err)
	}
	view.Evidence = []cases.EvidenceLink{{
		EvidenceID: "decision-1", Requirement: "decision_record",
		Kind: "decision", SubjectType: "decision", SubjectID: "decision-1",
		Label: "Decision record", LinkedSeq: 77,
	}}
	first, err := handler.RequestPolicyAssist(ctx, scheduler, view, policy, source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := handler.RequestPolicyAssist(ctx, scheduler, view, policy, source)
	if err != nil || second != first {
		t.Fatalf("idempotent policy assist = %q/%q err=%v", first, second, err)
	}
	request, err := handler.AssistSnapshot(ctx, scheduler, first)
	if err != nil {
		t.Fatal(err)
	}
	if request.Release != release || request.EvidenceSeq != 77 ||
		request.RequestedBy != scheduler.Actor || request.PolicySource == nil ||
		request.PolicySource.PolicyKey != policy.Key ||
		request.PolicySource.ConfigurationSeq != 42 ||
		!validSHA256(request.PolicySource.EvidenceFingerprint) {
		t.Fatalf("policy request lineage = %+v", request)
	}
	count := 0
	for _, event := range log.Export() {
		if event.Type == TypeAssistRequested {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("assist request events = %d, want one", count)
	}
}
