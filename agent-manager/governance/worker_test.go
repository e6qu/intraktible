// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	engine "github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func TestDurableAssistWorkerHealthKeepsIndependentFailuresVisible(t *testing.T) {
	service := NewService(NewHandler(eventlog.NewMemory()), store.NewMemory())
	service.reportWorker("assist/zeta", errors.New("completion append failed"))
	service.reportWorker(assistScanHealth, errors.New("queue scan failed"))

	if err := service.Err(); err == nil ||
		!strings.Contains(err.Error(), "assist/zeta: completion append failed") {
		t.Fatalf("worker health = %v, want deterministic assist failure", err)
	}
	service.reportWorker("assist/zeta", nil)
	if err := service.Err(); err == nil ||
		!strings.Contains(err.Error(), "queue-scan: queue scan failed") {
		t.Fatalf("worker health after assist recovery = %v, want scan failure", err)
	}
	service.reportWorker(assistScanHealth, nil)
	if err := service.Err(); err != nil {
		t.Fatalf("worker health after recovery = %v, want nil", err)
	}
}

func TestDurableAssistWorkerUsesSealedSnapshotAndRecordsOneResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	log := eventlog.NewMemory()
	st := store.NewMemory()
	vault := erasure.NewVault(st)
	handler := NewHandler(log).WithContentSealer(vault)
	maker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	checker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "checker"}
	operator := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "durable-assist", Slug: "durable-assist",
		Name: "Durable assist", Task: "Cited review assistance",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	spec := testReleaseSpec()
	spec.Provider = "governance-test"
	release := approveReleaseWithSpec(
		t, ctx, handler, maker, checker, template.TemplateID, suite, spec,
	)
	deployment := DeploymentRequest{
		DeploymentID: "durable-production", TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction, Reason: "worker test",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	request := AssistRequested{
		CaseID: "case-durable", Kind: AssistSummary, TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
		Subject: "customer/durable",
	}
	input := AssistInput{
		CaseID: request.CaseID, CaseType: "underwriting",
		Context: json.RawMessage(`{"private_note":"sealed-only"}`),
		Evidence: []cases.EvidenceLink{{
			EvidenceID: "evidence-1", Kind: "document",
			SubjectType: "entity", SubjectID: "customer-1", Label: "Application",
		}},
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request.SealedInput, err = vault.Seal(ctx, operator, request.Subject, encoded)
	if err != nil {
		t.Fatal(err)
	}
	request.InputSubject = request.Subject
	assistID, _, err := handler.RequestAssist(ctx, operator, request)
	if err != nil {
		t.Fatal(err)
	}
	registry := ai.NewRegistry()
	registry.Register(governanceProvider{})
	serviceA := NewService(
		handler, st, WithAssistRegistry(registry),
		WithToolbox(governanceToolbox{}), WithContentSealer(vault),
	)
	replicaHandler := NewHandler(log).WithContentSealer(vault)
	serviceB := NewService(
		replicaHandler, st, WithAssistRegistry(registry),
		WithToolbox(governanceToolbox{}), WithContentSealer(vault),
	)
	if _, err := serviceA.RecoverAssists(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := serviceB.RecoverAssists(ctx); err != nil {
		t.Fatal(err)
	}
	if err := serviceA.StartWorkers(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := serviceB.StartWorkers(ctx, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		serviceA.DrainWorkers()
		serviceB.DrainWorkers()
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, found, err := handler.AssistStatus(ctx, operator, assistID)
		if err != nil {
			t.Fatal(err)
		}
		if found && status == AssistCompletedStatus {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("assist status = %s after worker deadline", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	events, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	claims, completions := 0, 0
	for _, event := range events {
		switch event.Type {
		case TypeAssistClaimed:
			claims++
		case TypeAssistCompleted:
			completions++
			if strings.Contains(string(event.Payload), "Supported") ||
				strings.Contains(string(event.Payload), "sealed-only") {
				t.Fatalf("durable assist event leaked sealed content: %s", event.Payload)
			}
		case TypeAssistRequested:
			if strings.Contains(string(event.Payload), "sealed-only") {
				t.Fatalf("assist request leaked sealed input: %s", event.Payload)
			}
		}
	}
	if claims != 1 || completions != 1 {
		t.Fatalf("claims=%d completions=%d", claims, completions)
	}
}

func TestApprovedToolContinuationRunsOnDurableWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	log := eventlog.NewMemory()
	st := store.NewMemory()
	vault := erasure.NewVault(st)
	handler := NewHandler(log).WithContentSealer(vault)
	maker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	checker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "checker"}
	operator := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "durable-tool", Slug: "durable-tool",
		Name: "Durable tool", Task: "Human-gated cited assistance",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	spec := testReleaseSpec()
	spec.Provider = "human-tool-test"
	spec.Tools[0].Mode = ToolHumanBeforeCall
	release := approveReleaseWithSpec(
		t, ctx, handler, maker, checker, template.TemplateID, suite, spec,
	)
	deployment := DeploymentRequest{
		DeploymentID: "durable-tool-production", TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction, Reason: "worker test",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	request := AssistRequested{
		CaseID: "case-tool", Kind: AssistSummary, TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
		Subject: "customer/tool",
	}
	input := AssistInput{
		CaseID: request.CaseID, CaseType: "underwriting",
		Evidence: []cases.EvidenceLink{{
			EvidenceID: "evidence-1", Kind: "document",
			SubjectType: "entity", SubjectID: "customer-1", Label: "Application",
		}},
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request.SealedInput, err = vault.Seal(ctx, operator, request.Subject, encoded)
	if err != nil {
		t.Fatal(err)
	}
	request.InputSubject = request.Subject
	assistID, _, err := handler.RequestAssist(ctx, operator, request)
	if err != nil {
		t.Fatal(err)
	}
	registry := ai.NewRegistry()
	registry.Register(&humanToolProvider{
		arguments: json.RawMessage(`{"evidence_id":"evidence-1"}`),
	})
	toolbox := &recordingToolbox{}
	service := NewService(
		handler, st, WithAssistRegistry(registry),
		WithToolbox(toolbox), WithContentSealer(vault),
	)
	if err := service.StartWorkers(ctx, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		service.DrainWorkers()
	})
	waitForAssistStatus(
		t, ctx, handler, operator, assistID, AssistAwaitingApprovalStatus,
	)
	approvalID := ""
	events, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != TypeToolApprovalRequested {
			continue
		}
		var approval ToolApprovalRequested
		if err := json.Unmarshal(event.Payload, &approval); err != nil {
			t.Fatal(err)
		}
		if approval.AssistID == assistID {
			approvalID = approval.ApprovalID
		}
	}
	if approvalID == "" {
		t.Fatal("durable worker produced no tool approval request")
	}
	if _, err := handler.DecideToolApproval(
		ctx, checker, approvalID, ToolApprovalApproved, "necessary and proportionate",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecoverAssists(ctx); err != nil {
		t.Fatal(err)
	}
	waitForAssistStatus(t, ctx, handler, operator, assistID, AssistCompletedStatus)
	if toolbox.calls != 1 {
		t.Fatalf("approved tool executions = %d, want 1", toolbox.calls)
	}
	events, err = log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	claims, completions := 0, 0
	for _, event := range events {
		switch event.Type {
		case TypeAssistClaimed:
			claims++
		case TypeAssistCompleted:
			completions++
		}
	}
	if claims != 2 || completions != 1 {
		t.Fatalf("claims=%d completions=%d", claims, completions)
	}
}

func TestAssistLeaseLossDeadLettersAndRequiresExplicitAtLeastOnceRetry(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	checker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "checker"}
	operator := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "lease-assist", Slug: "lease-assist",
		Name: "Lease assist", Task: "Cited review assistance",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	release := approveRelease(t, ctx, handler, maker, checker, template.TemplateID, suite)
	deployment := DeploymentRequest{
		DeploymentID: "lease-production", TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction, Reason: "lease test",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	assistID, _, err := handler.RequestAssist(ctx, operator, AssistRequested{
		CaseID: "case-lease", Kind: AssistSummary, TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := handler.ClaimAssist(ctx, operator, assistID, "worker-1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	replica := NewHandler(log).WithNow(func() time.Time { return now })
	if _, _, err := replica.ClaimAssist(
		ctx, operator, assistID, "worker-2", 30*time.Second,
	); err == nil {
		t.Fatal("second replica claimed an active assist")
	}
	now = now.Add(31 * time.Second)
	if _, err := replica.DeadLetterExpiredAssist(ctx, operator, assistID); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RetryAssist(
		ctx, operator, assistID, "retry without acknowledgement", false,
	); err == nil || !strings.Contains(err.Error(), "at-least-once") {
		t.Fatalf("unacknowledged retry error = %v", err)
	}
	if _, err := handler.RetryAssist(
		ctx, operator, assistID, "operator accepts possible duplicate provider charge", true,
	); err != nil {
		t.Fatal(err)
	}
	second, _, err := replica.ClaimAssist(
		ctx, operator, assistID, "worker-2", 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.Attempt != first.Attempt+1 {
		t.Fatalf("retry attempt = %d, want %d", second.Attempt, first.Attempt+1)
	}
	if _, err := handler.FailClaimedAssist(
		ctx, operator, assistID, "stale worker result", first.Owner, first.Attempt,
	); err == nil || !strings.Contains(err.Error(), "active lease") {
		t.Fatalf("stale worker terminal error = %v", err)
	}
	if _, err := replica.FailClaimedAssist(
		ctx, operator, assistID, "provider refused", second.Owner, second.Attempt,
	); err != nil {
		t.Fatal(err)
	}
}

func TestAssistCancellationPreventsLateWorkerCompletion(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	operator, assistID, request := readyAssist(t, ctx, handler, log)
	claim, _, err := handler.ClaimAssist(
		ctx, operator, assistID, "worker-1", 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.CancelAssist(
		ctx, operator, assistID, "reviewer no longer needs this suggestion",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.CompleteClaimedAssist(
		ctx, operator, validAssistResult(assistID, request), claim.Owner, claim.Attempt,
	); err == nil || !strings.Contains(err.Error(), "not awaiting") {
		t.Fatalf("late completion error = %v", err)
	}
}

func TestAssistCancellationPropagatesToRunningWorkerContext(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	handler := NewHandler(log)
	operator, assistID, _ := readyAssist(t, ctx, handler, log)
	claim, _, err := handler.ClaimAssist(
		ctx, operator, assistID, "worker-1", assistLease,
	)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	service := NewService(handler, store.NewMemory())
	go service.heartbeatAssist(runCtx, cancel, operator, claim, done)
	if _, err := handler.CancelAssist(
		ctx, operator, assistID, "reviewer no longer needs this suggestion",
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("durable cancellation did not reach the running provider context")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation watcher did not finish")
	}
}

func readyAssist(
	t *testing.T,
	ctx context.Context,
	handler *Handler,
	log *eventlog.MemoryLog,
) (identity.Identity, string, AssistRequested) {
	t.Helper()
	maker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	checker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "checker"}
	operator := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "cancel-assist", Slug: "cancel-assist",
		Name: "Cancel assist", Task: "Cited review assistance",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	release := approveRelease(t, ctx, handler, maker, checker, template.TemplateID, suite)
	deployment := DeploymentRequest{
		DeploymentID: "cancel-production", TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction, Reason: "cancel test",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	request := AssistRequested{
		CaseID: "case-cancel", Kind: AssistSummary, TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
	}
	assistID, _, err := handler.RequestAssist(ctx, operator, request)
	if err != nil {
		t.Fatal(err)
	}
	request, err = handler.AssistSnapshot(ctx, operator, assistID)
	if err != nil {
		t.Fatal(err)
	}
	return operator, assistID, request
}

func validAssistResult(assistID string, request AssistRequested) AssistResult {
	return AssistResult{
		AssistID: assistID, CaseID: request.CaseID, Kind: request.Kind,
		TemplateID: request.TemplateID, Release: request.Release,
		Environment: request.Environment, RunID: "run-late",
		Suggestion: json.RawMessage(`{"summary":"supported"}`),
		Citations: []Citation{{
			EvidenceID: "evidence-1", Claim: "Supported by governed evidence.",
		}},
		Confidence: 0.8, EvidenceSeq: request.EvidenceSeq,
		InvocationID: request.InvocationID, Provider: "local",
		Model: "deterministic-test", Attempts: 1,
	}
}

func waitForAssistStatus(
	t *testing.T,
	ctx context.Context,
	handler *Handler,
	id identity.Identity,
	assistID string,
	want AssistStatus,
) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, found, err := handler.AssistStatus(ctx, id, assistID)
		if err != nil {
			t.Fatal(err)
		}
		if found && status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("assist status = %s, want %s", status, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
