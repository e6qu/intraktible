// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	engine "github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestGovernanceTextAndGeneratedResultsRejectRawPII(t *testing.T) {
	template := Template{
		TemplateID: "template-1", Slug: "case-assist",
		Name: "Case assist", Task: "Email jane@example.com with the result",
	}
	if err := template.Validate(); err == nil || !strings.Contains(err.Error(), "raw PII") {
		t.Fatalf("template PII error = %v", err)
	}

	spec := testReleaseSpec()
	spec.Instructions = "Use SSN 123-45-6789 as a fixture"
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "raw PII") {
		t.Fatalf("release PII error = %v", err)
	}

	suite := testSuite()
	suite.Cases[0].Prompt = "Summarize jane@example.com"
	if err := suite.Validate(); err == nil || !strings.Contains(err.Error(), "raw PII") {
		t.Fatalf("suite PII error = %v", err)
	}

	result := AssistResult{
		AssistID: "assist-1", CaseID: "case-1", Kind: AssistSummary,
		TemplateID: "template-1", Release: 1, Environment: "production",
		RunID: "run-1", Suggestion: json.RawMessage(`{"email":"jane@example.com"}`),
		Confidence: 0.5, EvidenceSeq: 1, InvocationID: "invocation-1",
		Provider: "provider", Model: "model", Attempts: 1,
	}
	if err := result.Validate(); err == nil || !strings.Contains(err.Error(), "raw PII") {
		t.Fatalf("assist PII error = %v", err)
	}

	action := ReviewerAction{
		AssistID: "assist-1", Action: AssistEdited,
		Final: json.RawMessage(`{"phone":"+1 202-555-0198"}`),
	}
	if err := action.Validate(); err == nil || !strings.Contains(err.Error(), "raw PII") {
		t.Fatalf("feedback PII error = %v", err)
	}
}

func TestSafetyIncidentRejectsRawPIIBeforeAppend(t *testing.T) {
	log := eventlog.NewMemory()
	handler := NewHandler(log)
	id := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}
	_, _, err := handler.OpenSafetyIncident(
		context.Background(), id, SafetyIncidentOpened{
			TemplateID: "template-1", Release: 1, Kind: "provider_error",
			Severity: SeverityCritical, Summary: "Affected jane@example.com",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "raw PII") {
		t.Fatalf("incident PII error = %v", err)
	}
	if log.Head() != 0 {
		t.Fatalf("PII incident appended at seq %d", log.Head())
	}
}

func TestGovernanceAcceptsOpaqueEvidenceAndArtifactReferences(t *testing.T) {
	if err := rejectTextPII(
		"reference", "case/evidence-42", "artifact:sha256:0123456789abcdef",
	); err != nil {
		t.Fatal(err)
	}
}

func TestAssistContentIsCryptoShreddedWithCaseSubject(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	vault := erasure.NewVault(st).WithNow(func() time.Time { return now })
	handler := NewHandler(log).
		WithNow(func() time.Time { return now }).
		WithContentSealer(vault)
	maker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	checker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "checker"}
	operator := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}

	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "privacy-assist", Slug: "privacy-assist",
		Name: "Privacy assist", Task: "Summarize governed evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	release := approveRelease(t, ctx, handler, maker, checker, template.TemplateID, suite)
	deployment := DeploymentRequest{
		DeploymentID: "privacy-production", TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction, Reason: "privacy test",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	request := AssistRequested{
		CaseID: "case-privacy", Kind: AssistSummary, TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
		Subject: "customer/privacy-test",
	}
	input, err := json.Marshal(AssistInput{
		CaseID: request.CaseID, CaseType: "privacy-review",
	})
	if err != nil {
		t.Fatal(err)
	}
	request.SealedInput, err = vault.Seal(ctx, operator, request.Subject, input)
	if err != nil {
		t.Fatal(err)
	}
	request.InputSubject = request.Subject
	assistID, _, err := handler.RequestAssist(ctx, operator, request)
	if err != nil {
		t.Fatal(err)
	}
	request, err = handler.AssistSnapshot(ctx, operator, assistID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.CompleteAssist(ctx, operator, AssistResult{
		AssistID: assistID, CaseID: request.CaseID, Kind: request.Kind,
		TemplateID: request.TemplateID, Release: request.Release,
		Environment: request.Environment, RunID: "run-privacy",
		Suggestion: json.RawMessage(`{"summary":"verified account evidence"}`),
		Citations: []Citation{{
			EvidenceID: "evidence-1", Claim: "The account evidence supports the summary.",
		}},
		Confidence: 0.9, EvidenceSeq: request.EvidenceSeq,
		InvocationID: request.InvocationID, Provider: "local",
		Model: "deterministic-test", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RecordAssistActionAtEvidence(
		ctx, operator,
		ReviewerAction{
			AssistID: assistID, Action: AssistEdited,
			Final: json.RawMessage(
				`{"summary":"reviewed account record","disposition":"manual_review"}`,
			),
			TimeSavedMS: 800,
		},
		request.EvidenceSeq+10,
	); err != nil {
		t.Fatal(err)
	}

	events, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(string(event.Payload), "verified account evidence") ||
			strings.Contains(string(event.Payload), "supports the summary") ||
			strings.Contains(string(event.Payload), "reviewed account record") {
			t.Fatalf("assist event leaked generated content: %s", event.Payload)
		}
	}
	if _, err := projection.New(log, st, Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	sealed, found, err := ReadAssist(ctx, st, operator, assistID)
	if err != nil || !found {
		t.Fatalf("sealed assist found=%t err=%v", found, err)
	}
	if sealed.Result == nil || len(sealed.Result.Suggestion) != 0 ||
		len(sealed.SealedContent) == 0 || sealed.ContentSubject != request.Subject ||
		sealed.Action == nil || len(sealed.Action.Final) != 0 ||
		len(sealed.SealedActionFinal) == 0 ||
		!sealed.Action.EvidenceStale || sealed.Action.EvidenceHeadSeq != request.EvidenceSeq+10 ||
		len(sealed.Differences) != 2 {
		t.Fatalf("sealed projection = %+v", sealed)
	}

	service := NewService(handler, st, WithContentSealer(vault))
	opened, err := service.openAssistContent(ctx, operator, sealed)
	if err != nil || opened.Result == nil ||
		!strings.Contains(string(opened.Result.Suggestion), "verified account evidence") ||
		opened.Action == nil ||
		!strings.Contains(string(opened.Action.Final), "reviewed account record") ||
		len(opened.SealedContent) != 0 || len(opened.SealedActionFinal) != 0 {
		t.Fatalf("opened assist = %+v, err=%v", opened, err)
	}
	if err := vault.Erase(ctx, operator, request.Subject); err != nil {
		t.Fatal(err)
	}
	erased, err := service.openAssistContent(ctx, operator, sealed)
	if err != nil || !erased.ContentErased || len(erased.SealedContent) != 0 ||
		erased.Result == nil || len(erased.Result.Suggestion) != 0 ||
		erased.Action == nil || len(erased.Action.Final) != 0 ||
		!erased.ActionFinalErased {
		t.Fatalf("erased assist = %+v, err=%v", erased, err)
	}
}
