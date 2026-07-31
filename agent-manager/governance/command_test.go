// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	engine "github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestGovernedReleaseLifecycleReplaysExactly(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	checker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "checker"}
	operator := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}

	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "case-copilot", Slug: "case-copilot", Name: "Case copilot",
		Task: "Cited case assistance", HighImpact: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := testSuite()
	suite.Version, suite.DatasetHash = 0, ""
	suite, _, err = handler.PublishSuite(ctx, maker, suite)
	if err != nil {
		t.Fatal(err)
	}

	release1 := approveRelease(t, ctx, handler, maker, checker, template.TemplateID, suite)
	deployment1 := DeploymentRequest{
		DeploymentID: "production-1", TemplateID: template.TemplateID, Release: release1,
		Environment: engine.EnvProduction, Reason: "initial production release",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment1); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment1.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.PauseDeployment(ctx, operator, deployment1.DeploymentID, "replace release"); err != nil {
		t.Fatal(err)
	}

	release2 := approveRelease(t, ctx, handler, maker, checker, template.TemplateID, suite)
	activateAt := now.Add(time.Hour)
	deployment2 := DeploymentRequest{
		DeploymentID: "production-2", TemplateID: template.TemplateID, Release: release2,
		Environment: engine.EnvProduction, At: &activateAt, Reason: "scheduled upgrade",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment2); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment2.DeploymentID); err == nil ||
		!strings.Contains(err.Error(), "has not arrived") {
		t.Fatalf("early activation error = %v", err)
	}
	now = activateAt
	if _, err := handler.ActivateDeployment(ctx, operator, deployment2.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RollbackDeployment(
		ctx, operator, deployment2.DeploymentID, release1, "regression",
	); err != nil {
		t.Fatal(err)
	}
	assistRequest := AssistRequested{
		CaseID: "case-1", Kind: AssistSummary, TemplateID: template.TemplateID,
		Release: release1, Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
		IdempotencyHash: strings.Repeat("d", 64),
	}
	malformedIdempotency := assistRequest
	malformedIdempotency.IdempotencyHash = "not-a-sha256"
	if _, _, err := handler.RequestAssist(
		ctx, operator, malformedIdempotency,
	); err == nil || !strings.Contains(err.Error(), "idempotency hash") {
		t.Fatalf("malformed assist idempotency error = %v", err)
	}
	assistID, assistEvent, err := handler.RequestAssist(ctx, operator, assistRequest)
	if err != nil {
		t.Fatal(err)
	}
	repeatedID, repeatedEvent, err := handler.RequestAssist(ctx, operator, assistRequest)
	if err != nil {
		t.Fatal(err)
	}
	if repeatedID != assistID || repeatedEvent.Seq != assistEvent.Seq {
		t.Fatalf(
			"idempotent assist = %s/%d, want %s/%d",
			repeatedID, repeatedEvent.Seq, assistID, assistEvent.Seq,
		)
	}
	recordedRequest, err := handler.AssistSnapshot(ctx, operator, assistID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.CompleteAssist(ctx, operator, AssistResult{
		AssistID: assistID, CaseID: "case-1", Kind: AssistSummary,
		TemplateID: template.TemplateID, Release: release1,
		Environment: engine.EnvProduction, RunID: "run-1",
		Suggestion: json.RawMessage(`{"summary":"verified"}`),
		Citations: []Citation{{
			EvidenceID: "evidence-1", Claim: "The evidence supports this summary.",
		}},
		Confidence: 0.9, EvidenceSeq: assistRequest.EvidenceSeq,
		InvocationID: recordedRequest.InvocationID, Provider: "local", Model: "deterministic-test",
		Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RecordAssistAction(ctx, operator, ReviewerAction{
		AssistID: assistID, Action: AssistAccepted, TimeSavedMS: 1_000,
	}); err != nil {
		t.Fatal(err)
	}

	projected := store.NewMemory()
	if _, err := projection.New(log, projected, Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	view, found, err := store.GetDoc[DeploymentView](
		ctx, projected, CollectionDeployments, store.Key("acme", "risk", deployment2.DeploymentID),
	)
	if err != nil || !found {
		t.Fatalf("deployment projection found=%t err=%v", found, err)
	}
	if view.Status != DeploymentActive || view.Release != release1 ||
		view.PreviousRelease != release2 {
		t.Fatalf("rollback projection = %+v", view)
	}
	releases, err := ListReleases(ctx, projected, maker, template.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 || releases[0].Status != ReleaseApproved ||
		releases[1].Status != ReleaseApproved {
		t.Fatalf("release projections = %+v", releases)
	}
	assists, err := ListCaseAssists(ctx, projected, operator, "case-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(assists) != 1 || assists[0].Status != "completed" ||
		assists[0].Action == nil || assists[0].Action.Action != AssistAccepted {
		t.Fatalf("assist projection = %+v", assists)
	}

	fresh := store.NewMemory()
	if _, err := projection.New(log, fresh, Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(projected.Collections())
	second, _ := json.Marshal(fresh.Collections())
	if !bytes.Equal(first, second) {
		t.Fatalf("replay collections differ: %s != %s", first, second)
	}
}

func TestSchedulerActivatesExpiresAndContainsCriticalIncidents(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "o", Workspace: "w", Actor: "maker"}
	checker := identity.Identity{Org: "o", Workspace: "w", Actor: "checker"}
	operator := identity.Identity{Org: "o", Workspace: "w", Actor: "operator"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "t", Slug: "template", Name: "Template", Task: "Assist",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	release := approveRelease(t, ctx, handler, maker, checker, template.TemplateID, suite)
	activateAt, expiresAt := now.Add(time.Hour), now.Add(2*time.Hour)
	if _, err := handler.RequestDeployment(ctx, operator, DeploymentRequest{
		DeploymentID: "scheduled", TemplateID: template.TemplateID, Release: release,
		Environment: engine.EnvProduction, At: &activateAt, ExpiresAt: &expiresAt,
		Reason: "time-boxed release",
	}); err != nil {
		t.Fatal(err)
	}
	projected := store.NewMemory()
	rebuild := func() {
		t.Helper()
		if _, err := projection.New(log, projected, Projector{}).RebuildTo(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	rebuild()
	scheduler := NewScheduler(projected, handler).WithNow(func() time.Time { return now })
	summary, err := scheduler.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary != (TickSummary{}) {
		t.Fatalf("early scheduler summary = %+v", summary)
	}
	now = activateAt
	summary, err = scheduler.Tick(ctx)
	if err != nil || summary.Activated != 1 {
		t.Fatalf("activation summary=%+v err=%v", summary, err)
	}
	rebuild()
	if _, _, err := handler.OpenSafetyIncident(ctx, operator, SafetyIncidentOpened{
		TemplateID: template.TemplateID, Release: release, DeploymentID: "scheduled",
		Kind: "prompt_injection", Severity: SeverityCritical, Summary: "boundary violation",
	}); err != nil {
		t.Fatal(err)
	}
	rebuild()
	summary, err = scheduler.Tick(ctx)
	if err != nil || summary.SafetyContained != 1 {
		t.Fatalf("containment summary=%+v err=%v", summary, err)
	}
	rebuild()
	deployment, found, err := ReadDeployment(ctx, projected, operator, "scheduled")
	if err != nil || !found || deployment.Status != DeploymentPaused {
		t.Fatalf("contained deployment found=%t view=%+v err=%v", found, deployment, err)
	}
}

func TestSchedulerContainsCriticalIncidentOnlyWithinItsTenant(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	type tenantDeployment struct {
		id         identity.Identity
		deployment string
	}
	configure := func(org string) tenantDeployment {
		t.Helper()
		maker := identity.Identity{Org: org, Workspace: "risk", Actor: "maker"}
		checker := identity.Identity{Org: org, Workspace: "risk", Actor: "checker"}
		operator := identity.Identity{Org: org, Workspace: "risk", Actor: "operator"}
		template, _, err := handler.RegisterTemplate(ctx, maker, Template{
			TemplateID: "shared-template", Slug: "shared-template",
			Name: "Shared template", Task: "Cited assistance",
		})
		if err != nil {
			t.Fatal(err)
		}
		suite := passingSuite(t, ctx, handler, maker)
		release := approveRelease(
			t, ctx, handler, maker, checker, template.TemplateID, suite,
		)
		deploymentID := "shared-production"
		if _, err := handler.RequestDeployment(ctx, operator, DeploymentRequest{
			DeploymentID: deploymentID, TemplateID: template.TemplateID,
			Release: release, Environment: engine.EnvProduction, Reason: "approved rollout",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := handler.ActivateDeployment(ctx, operator, deploymentID); err != nil {
			t.Fatal(err)
		}
		return tenantDeployment{id: operator, deployment: deploymentID}
	}
	affected := configure("affected")
	unrelated := configure("unrelated")
	if _, _, err := handler.OpenSafetyIncident(
		ctx, affected.id, SafetyIncidentOpened{
			TemplateID: "shared-template", Release: 1,
			DeploymentID: affected.deployment, Kind: "prompt_injection",
			Severity: SeverityCritical, Summary: "tenant-local safety boundary",
		},
	); err != nil {
		t.Fatal(err)
	}
	projected := store.NewMemory()
	rebuild := func() {
		t.Helper()
		if _, err := projection.New(log, projected, Projector{}).RebuildTo(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	rebuild()
	summary, err := NewScheduler(projected, handler).
		WithNow(func() time.Time { return now }).
		Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.SafetyContained != 1 {
		t.Fatalf("tenant-local containment summary = %+v", summary)
	}
	rebuild()
	affectedView, found, err := ReadDeployment(
		ctx, projected, affected.id, affected.deployment,
	)
	if err != nil || !found || affectedView.Status != DeploymentPaused {
		t.Fatalf("affected deployment = %+v found=%t err=%v", affectedView, found, err)
	}
	unrelatedView, found, err := ReadDeployment(
		ctx, projected, unrelated.id, unrelated.deployment,
	)
	if err != nil || !found || unrelatedView.Status != DeploymentActive {
		t.Fatalf("unrelated deployment = %+v found=%t err=%v", unrelatedView, found, err)
	}
}

func TestCircuitBreakerBlocksAdmissionContainsAndRequiresExplicitRecovery(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "o", Workspace: "w", Actor: "maker"}
	checker := identity.Identity{Org: "o", Workspace: "w", Actor: "checker"}
	operator := identity.Identity{Org: "o", Workspace: "w", Actor: "operator"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "contained", Slug: "contained", Name: "Contained", Task: "Assist",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	spec := testReleaseSpec()
	spec.CircuitBreaker = &CircuitBreakerPolicy{
		WindowMinutes: 10, MinSamples: 2, FailureRate: 0.5,
	}
	release := approveReleaseWithSpec(
		t, ctx, handler, maker, checker, template.TemplateID, suite, spec,
	)
	deployment := DeploymentRequest{
		DeploymentID: "contained-production", TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction, Reason: "approved rollout",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	fail := func(caseID string) string {
		t.Helper()
		assistID, _, err := handler.RequestAssist(ctx, operator, AssistRequested{
			CaseID: caseID, Kind: AssistSummary, TemplateID: template.TemplateID,
			Release: release, Environment: engine.EnvProduction,
			EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := handler.FailAssist(
			ctx, operator, assistID, "provider execution failed",
		); err != nil {
			t.Fatal(err)
		}
		return assistID
	}
	firstFailed := fail("case-1")
	opened, _, err := handler.EvaluateDeploymentCircuit(
		ctx, operator, deployment.DeploymentID,
	)
	if err != nil || opened {
		t.Fatalf("breaker opened before min_samples: opened=%t err=%v", opened, err)
	}
	fail("case-2")

	projected := store.NewMemory()
	rebuild := func() {
		t.Helper()
		if _, err := projection.New(log, projected, Projector{}).RebuildTo(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	rebuild()
	scheduler := NewScheduler(projected, handler).WithNow(func() time.Time { return now })
	summary, err := scheduler.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CircuitsOpened != 1 || summary.SafetyContained != 1 {
		t.Fatalf("circuit containment summary = %+v", summary)
	}
	if _, _, err := handler.RequestAssist(ctx, operator, AssistRequested{
		CaseID: "case-blocked", Kind: AssistSummary, TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
	}); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("paused admission error = %v", err)
	}
	if _, err := handler.RetryAssist(
		ctx, operator, firstFailed, "retry after containment", true,
	); err == nil || !strings.Contains(err.Error(), "exact active deployment") {
		t.Fatalf("paused retry admission error = %v", err)
	}
	if _, err := handler.ResumeDeployment(
		ctx, operator, deployment.DeploymentID, "provider recovered",
	); err == nil || !strings.Contains(err.Error(), "resolve critical") {
		t.Fatalf("resume with open incident error = %v", err)
	}
	rebuild()
	incidents, err := ListIncidents(ctx, projected, operator)
	if err != nil || len(incidents) != 1 ||
		incidents[0].Kind != circuitBreakerIncidentKind ||
		incidents[0].Severity != SeverityCritical {
		t.Fatalf("circuit incidents = %+v err=%v", incidents, err)
	}
	if _, err := handler.ResolveSafetyIncident(
		ctx, operator, incidents[0].IncidentID,
		"provider health verified; reset the observed failure window",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ResumeDeployment(
		ctx, operator, deployment.DeploymentID, "provider health verified",
	); err != nil {
		t.Fatal(err)
	}
	fail("case-recovered-1")
	fail("case-recovered-2")
	if _, _, err := handler.RequestAssist(ctx, operator, AssistRequested{
		CaseID: "case-circuit-admission", Kind: AssistSummary,
		TemplateID: template.TemplateID, Release: release,
		Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
	}); err == nil || !strings.Contains(err.Error(), "circuit breaker opened") {
		t.Fatalf("authoritative circuit admission error = %v", err)
	}
	rebuild()
	summary, err = scheduler.Tick(ctx)
	if err != nil || summary.CircuitsOpened != 0 || summary.SafetyContained != 1 {
		t.Fatalf("duplicate circuit summary=%+v err=%v", summary, err)
	}
}

func TestCircuitBreakerLatchesOnceAcrossReplicaRace(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "o", Workspace: "w", Actor: "maker"}
	checker := identity.Identity{Org: "o", Workspace: "w", Actor: "checker"}
	operator := identity.Identity{Org: "o", Workspace: "w", Actor: "operator"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "replica-contained", Slug: "replica-contained",
		Name: "Replica contained", Task: "Assist",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	spec := testReleaseSpec()
	spec.CircuitBreaker = &CircuitBreakerPolicy{
		WindowMinutes: 10, MinSamples: 1, FailureRate: 1,
	}
	release := approveReleaseWithSpec(
		t, ctx, handler, maker, checker, template.TemplateID, suite, spec,
	)
	deployment := DeploymentRequest{
		DeploymentID: "replica-production", TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction, Reason: "approved rollout",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	assistID, _, err := handler.RequestAssist(ctx, operator, AssistRequested{
		CaseID: "replica-case", Kind: AssistSummary, TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := handler.FailAssist(ctx, operator, assistID, "provider execution failed")
	if err != nil {
		t.Fatal(err)
	}

	racingLog := newSynchronizedTenantReadLog(log, 2)
	replicas := []*Handler{
		NewHandler(racingLog).WithNow(func() time.Time { return now }),
		NewHandler(racingLog).WithNow(func() time.Time { return now }),
	}
	type result struct {
		opened bool
		event  eventlog.Envelope
		err    error
	}
	results := make([]result, len(replicas))
	var calls sync.WaitGroup
	calls.Add(len(replicas))
	for index, replica := range replicas {
		go func() {
			defer calls.Done()
			results[index].opened, results[index].event, results[index].err =
				replica.EvaluateDeploymentCircuit(ctx, operator, deployment.DeploymentID)
		}()
	}
	racingLog.waitForSnapshots()
	racingLog.release()
	calls.Wait()

	opened := 0
	for index, result := range results {
		if result.err != nil {
			t.Fatalf("replica %d circuit evaluation: %v", index, result.err)
		}
		if result.opened {
			opened++
			if result.event.Type != TypeSafetyIncidentOpened {
				t.Fatalf("replica %d opened event = %+v", index, result.event)
			}
		} else if result.event.Seq != 0 {
			t.Fatalf("replica %d conflict returned event %+v", index, result.event)
		}
	}
	if opened != 1 {
		t.Fatalf("replica circuit openers = %d, want 1", opened)
	}

	events, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	incidentCount := 0
	wantClaim := handler.claim(
		operator, "circuit.open", deployment.DeploymentID,
		strconv.FormatUint(terminal.Seq, 10),
	)
	for _, event := range events {
		if event.Type != TypeSafetyIncidentOpened {
			continue
		}
		incidentCount++
		if event.Unique != wantClaim {
			t.Fatalf("circuit incident claim = %q, want %q", event.Unique, wantClaim)
		}
	}
	if incidentCount != 1 {
		t.Fatalf("circuit incident events = %d, want 1", incidentCount)
	}
}

func TestIndependentTrialAdjudicationChangesGateWithoutRewritingTrial(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	validator := identity.Identity{Org: "acme", Workspace: "risk", Actor: "validator"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "adjudicated-agent", Slug: "adjudicated-agent",
		Name: "Adjudicated agent", Task: "Governed assistance",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	release, _, _, err := handler.CreateRelease(
		ctx, maker, template.TemplateID, testReleaseSpec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	trials := passingTrials(suite)
	trials[0].Passed, trials[0].Status = false, "failed"
	trials[0].Grade.Passed = false
	campaign, _, err := handler.RecordCampaign(
		ctx, maker, template.TemplateID, release, suite.SuiteID, suite.Version, trials,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !campaign.Blocking {
		t.Fatal("required trial failure did not block original campaign")
	}
	if _, _, err := handler.RequestReview(
		ctx, maker, template.TemplateID, release, []string{campaign.CampaignID},
		[]string{"eval:" + campaign.CampaignID}, []string{validator.Actor},
		now.Add(time.Hour),
	); err == nil || !strings.Contains(err.Error(), "blocks release review") {
		t.Fatalf("pre-adjudication review error = %v", err)
	}
	if _, err := handler.AdjudicateCampaignTrial(
		ctx, maker, campaign.CampaignID, trials[0].CaseID, trials[0].Trial,
		true, "self approval is forbidden",
	); err == nil || !strings.Contains(err.Error(), "cannot adjudicate") {
		t.Fatalf("self-adjudication error = %v", err)
	}
	if _, err := handler.AdjudicateCampaignTrial(
		ctx, validator, campaign.CampaignID, trials[0].CaseID, trials[0].Trial,
		true, "The answer is semantically equivalent to the governed expectation.",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.AdjudicateCampaignTrial(
		ctx, validator, campaign.CampaignID, trials[0].CaseID, trials[0].Trial,
		true, "duplicate",
	); err == nil || !strings.Contains(err.Error(), "already adjudicated") {
		t.Fatalf("duplicate adjudication error = %v", err)
	}
	if _, _, err := handler.RequestReview(
		ctx, maker, template.TemplateID, release, []string{campaign.CampaignID},
		[]string{"eval:" + campaign.CampaignID}, []string{validator.Actor},
		now.Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	projected := store.NewMemory()
	if _, err := projection.New(log, projected, Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	view, found, err := ReadCampaign(ctx, projected, maker, campaign.CampaignID)
	if err != nil || !found || len(view.Adjudications) != 1 ||
		view.Assessment.Blocking || view.Assessment.Passed != campaign.Total ||
		view.CampaignResult.Passed != campaign.Passed ||
		view.Trials[0].Passed != campaign.Trials[0].Passed {
		t.Fatalf("adjudicated projection = %+v, found=%t, err=%v", view, found, err)
	}
}

func TestSchedulerExpiresReviewAndPermitsFreshEvidenceRequest(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "o", Workspace: "w", Actor: "maker"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "expiring", Slug: "expiring", Name: "Expiring", Task: "Assist",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	release, _, _, err := handler.CreateRelease(ctx, maker, template.TemplateID, testReleaseSpec())
	if err != nil {
		t.Fatal(err)
	}
	campaign, _, err := handler.RecordCampaign(
		ctx, maker, template.TemplateID, release, suite.SuiteID, suite.Version,
		passingTrials(suite),
	)
	if err != nil {
		t.Fatal(err)
	}
	firstRequest, _, err := handler.RequestReview(
		ctx, maker, template.TemplateID, release,
		[]string{campaign.CampaignID}, nil, nil, now.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projection.New(log, st, Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	summary, err := NewScheduler(st, handler).
		WithNow(func() time.Time { return now }).
		Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ReviewsExpired != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	replayed := store.NewMemory()
	if _, err := projection.New(log, replayed, Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	view, found, err := ReadRelease(ctx, replayed, maker, template.TemplateID, release)
	if err != nil || !found {
		t.Fatalf("release found=%t err=%v", found, err)
	}
	if view.Status != ReleaseEvaluated || view.Review == nil ||
		view.Review.RequestID != firstRequest || view.Review.ExpiredAt.IsZero() {
		t.Fatalf("expired review projection = %+v", view)
	}
	secondRequest, _, err := handler.RequestReview(
		ctx, maker, template.TemplateID, release,
		[]string{campaign.CampaignID}, nil, nil, now.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("fresh review request after expiry: %v", err)
	}
	if secondRequest == firstRequest {
		t.Fatal("fresh review reused expired request identity")
	}
}

func TestReviewEnforcesFourEyesAssignmentAndExpiry(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "o", Workspace: "w", Actor: "maker"}
	checker := identity.Identity{Org: "o", Workspace: "w", Actor: "checker"}
	other := identity.Identity{Org: "o", Workspace: "w", Actor: "other"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "t", Slug: "template", Name: "Template", Task: "Assist",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	release, _, _, err := handler.CreateRelease(ctx, maker, template.TemplateID, testReleaseSpec())
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := handler.RecordCampaign(
		ctx, maker, template.TemplateID, release, suite.SuiteID, suite.Version,
		passingTrials(suite),
	)
	if err != nil {
		t.Fatal(err)
	}
	expires := now.Add(time.Hour)
	requestID, _, err := handler.RequestReview(
		ctx, maker, template.TemplateID, release,
		[]string{result.CampaignID}, nil, []string{checker.Actor}, expires,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ReviewRelease(
		ctx, maker, template.TemplateID, release, requestID, ReviewApprove, "self",
	); err == nil || !strings.Contains(err.Error(), "four-eyes") {
		t.Fatalf("maker approval error = %v", err)
	}
	if _, err := handler.ReviewRelease(
		ctx, other, template.TemplateID, release, requestID, ReviewApprove, "unassigned",
	); err == nil || !strings.Contains(err.Error(), "not assigned") {
		t.Fatalf("unassigned approval error = %v", err)
	}
	now = expires
	if _, err := handler.ReviewRelease(
		ctx, checker, template.TemplateID, release, requestID, ReviewApprove, "late",
	); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired approval error = %v", err)
	}
}

func TestBlockedCampaignCannotEnterReview(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "o", Workspace: "w", Actor: "maker"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "t", Slug: "template", Name: "Template", Task: "Assist",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	release, _, _, err := handler.CreateRelease(ctx, maker, template.TemplateID, testReleaseSpec())
	if err != nil {
		t.Fatal(err)
	}
	trials := passingTrials(suite)
	trials[0].Passed = false
	trials[0].Status = "failed"
	trials[0].Grade.Passed = false
	result, _, err := handler.RecordCampaign(
		ctx, maker, template.TemplateID, release, suite.SuiteID, suite.Version, trials,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Blocking {
		t.Fatal("critical failure did not block campaign")
	}
	_, _, err = handler.RequestReview(
		ctx, maker, template.TemplateID, release,
		[]string{result.CampaignID}, nil, nil, now.Add(time.Hour),
	)
	if err == nil || !strings.Contains(err.Error(), "blocks release review") {
		t.Fatalf("blocked review error = %v", err)
	}
}

func TestAssistPeriodBudgetRetainsIndeterminateFailureAndBoundsRetry(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "o", Workspace: "w", Actor: "maker"}
	checker := identity.Identity{Org: "o", Workspace: "w", Actor: "checker"}
	operator := identity.Identity{Org: "o", Workspace: "w", Actor: "operator"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "budgeted", Slug: "budgeted", Name: "Budgeted", Task: "Assist",
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	spec := testReleaseSpec()
	spec.Budget.Period = "day"
	spec.Budget.PeriodCostUSD = 0.15
	release, _, _, err := handler.CreateRelease(ctx, maker, template.TemplateID, spec)
	if err != nil {
		t.Fatal(err)
	}
	campaign, _, err := handler.RecordCampaign(
		ctx, maker, template.TemplateID, release, suite.SuiteID, suite.Version,
		passingTrials(suite),
	)
	if err != nil {
		t.Fatal(err)
	}
	requestID, _, err := handler.RequestReview(
		ctx, maker, template.TemplateID, release,
		[]string{campaign.CampaignID}, nil, []string{checker.Actor}, now.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ReviewRelease(
		ctx, checker, template.TemplateID, release, requestID, ReviewApprove, "safe",
	); err != nil {
		t.Fatal(err)
	}
	deployment := DeploymentRequest{
		DeploymentID: "budgeted-production", TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction, Reason: "approved",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	request := AssistRequested{
		CaseID: "case-1", Kind: AssistSummary, TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: log.Head(),
		IdempotencyHash: strings.Repeat("1", 64),
	}
	first, _, err := handler.RequestAssist(ctx, operator, request)
	if err != nil {
		t.Fatal(err)
	}
	request.IdempotencyHash = strings.Repeat("2", 64)
	if _, _, err := handler.RequestAssist(ctx, operator, request); err == nil ||
		!strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("second reservation error = %v", err)
	}
	if _, err := handler.FailAssist(ctx, operator, first, "provider refused"); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RetryAssist(
		ctx, operator, first, "retry provider refusal", true,
	); err == nil || !strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("retry budget error = %v", err)
	}
	if _, _, err := handler.RequestAssist(ctx, operator, request); err == nil ||
		!strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("failed-attempt reservation error = %v", err)
	}
	now = now.Add(24 * time.Hour)
	if _, _, err := handler.RequestAssist(ctx, operator, request); err != nil {
		t.Fatalf("new budget period did not admit request: %v", err)
	}
}

func TestToolApprovalIsDurableAndRequiredForHumanGatedCompletion(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	handler := NewHandler(log).WithNow(func() time.Time { return now })
	maker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "maker"}
	checker := identity.Identity{Org: "acme", Workspace: "risk", Actor: "checker"}
	operator := identity.Identity{Org: "acme", Workspace: "risk", Actor: "operator"}
	template, _, err := handler.RegisterTemplate(ctx, maker, Template{
		TemplateID: "human-tool-agent", Slug: "human-tool-agent",
		Name: "Human tool agent", Task: "Governed assistance", HighImpact: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	suite := passingSuite(t, ctx, handler, maker)
	spec := testReleaseSpec()
	spec.Tools[0].Mode = ToolHumanBeforeCall
	release := approveReleaseWithSpec(
		t, ctx, handler, maker, checker, template.TemplateID, suite, spec,
	)
	deployment := DeploymentRequest{
		DeploymentID: "human-tool-production", TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction, Reason: "approved release",
	}
	if _, err := handler.RequestDeployment(ctx, operator, deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ActivateDeployment(ctx, operator, deployment.DeploymentID); err != nil {
		t.Fatal(err)
	}
	evidenceSeq := log.Head()
	assistID, _, err := handler.RequestAssist(ctx, operator, AssistRequested{
		CaseID: "case-1", Kind: AssistSummary, TemplateID: template.TemplateID,
		Release: release, Environment: engine.EnvProduction,
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: evidenceSeq,
	})
	if err != nil {
		t.Fatal(err)
	}
	recordedRequest, err := handler.AssistSnapshot(ctx, operator, assistID)
	if err != nil {
		t.Fatal(err)
	}
	argumentsHash, err := hashJSON(map[string]any{"evidence_id": "evidence-1"})
	if err != nil {
		t.Fatal(err)
	}
	approvalID, _, err := handler.RequestToolApproval(
		ctx, operator, assistID, ToolApprovalNeededError{
			InvocationID: recordedRequest.InvocationID, CallID: "call-1",
			Name: "case_evidence", Purpose: "case_review",
			ArgumentsHash: argumentsHash,
		}, now.Add(15*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.DecideToolApproval(
		ctx, checker, approvalID, ToolApprovalApproved, "necessary and proportionate",
	); err != nil {
		t.Fatal(err)
	}
	resultHash := sha256.Sum256([]byte(`{"risk":"low"}`))
	if _, err := handler.CompleteAssist(ctx, operator, AssistResult{
		AssistID: assistID, CaseID: "case-1", Kind: AssistSummary,
		TemplateID: template.TemplateID, Release: release,
		Environment: engine.EnvProduction, RunID: "run-1",
		Suggestion: json.RawMessage(`{"summary":"verified"}`),
		Citations: []Citation{{
			EvidenceID: "evidence-1", Claim: "Supported by case evidence",
		}},
		Confidence: 0.9, EvidenceSeq: evidenceSeq,
		InvocationID: recordedRequest.InvocationID, Provider: spec.Provider, Model: spec.Model,
		Attempts: 2, ToolCalls: []ToolExecution{{
			CallID: "call-1", Name: "case_evidence", Mode: ToolHumanBeforeCall,
			Purpose: "case_review", ArgumentsHash: argumentsHash,
			ResultHash: hex.EncodeToString(resultHash[:]), ApprovedBy: checker.Actor,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	projected := store.NewMemory()
	if _, err := projection.New(log, projected, Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	approval, found, err := ReadToolApproval(ctx, projected, checker, approvalID)
	if err != nil || !found || approval.Status != ToolApprovalApproved ||
		approval.DecidedBy != checker.Actor {
		t.Fatalf("tool approval = %+v, found=%t, err=%v", approval, found, err)
	}
	assist, found, err := ReadAssist(ctx, projected, operator, assistID)
	if err != nil || !found || assist.Status != "completed" ||
		assist.Result == nil || len(assist.Result.ToolCalls) != 1 {
		t.Fatalf("assist = %+v, found=%t, err=%v", assist, found, err)
	}
}

type synchronizedTenantReadLog struct {
	eventlog.Log
	snapshots sync.WaitGroup
	releaseCh chan struct{}
}

func newSynchronizedTenantReadLog(
	log eventlog.Log,
	readers int,
) *synchronizedTenantReadLog {
	synchronized := &synchronizedTenantReadLog{
		Log: log, releaseCh: make(chan struct{}),
	}
	synchronized.snapshots.Add(readers)
	return synchronized
}

func (l *synchronizedTenantReadLog) ReadTenantStream(
	ctx context.Context,
	org, workspace, stream string,
	fromSeq uint64,
) ([]eventlog.Envelope, error) {
	events, err := l.Log.ReadTenantStream(ctx, org, workspace, stream, fromSeq)
	l.snapshots.Done()
	select {
	case <-l.releaseCh:
		return events, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *synchronizedTenantReadLog) waitForSnapshots() {
	l.snapshots.Wait()
}

func (l *synchronizedTenantReadLog) release() {
	close(l.releaseCh)
}

func approveRelease(
	t *testing.T,
	ctx context.Context,
	handler *Handler,
	maker, checker identity.Identity,
	templateID string,
	suite EvalSuite,
) int {
	t.Helper()
	return approveReleaseWithSpec(
		t, ctx, handler, maker, checker, templateID, suite, testReleaseSpec(),
	)
}

func approveReleaseWithSpec(
	t *testing.T,
	ctx context.Context,
	handler *Handler,
	maker, checker identity.Identity,
	templateID string,
	suite EvalSuite,
	spec ReleaseSpec,
) int {
	t.Helper()
	release, _, _, err := handler.CreateRelease(ctx, maker, templateID, spec)
	if err != nil {
		t.Fatal(err)
	}
	trials := passingTrials(suite)
	for index := range trials {
		trials[index].Provider = spec.Provider
		trials[index].Model = spec.Model
	}
	result, _, err := handler.RecordCampaign(
		ctx, maker, templateID, release, suite.SuiteID, suite.Version, trials,
	)
	if err != nil {
		t.Fatal(err)
	}
	requestID, _, err := handler.RequestReview(
		ctx, maker, templateID, release, []string{result.CampaignID},
		[]string{"eval:" + result.CampaignID}, []string{checker.Actor},
		handler.now().Add(24*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ReviewRelease(
		ctx, checker, templateID, release, requestID, ReviewApprove, "evidence passed",
	); err != nil {
		t.Fatal(err)
	}
	return release
}

func passingSuite(
	t *testing.T,
	ctx context.Context,
	handler *Handler,
	id identity.Identity,
) EvalSuite {
	t.Helper()
	suite := testSuite()
	suite.Version, suite.DatasetHash = 0, ""
	suite, _, err := handler.PublishSuite(ctx, id, suite)
	if err != nil {
		t.Fatal(err)
	}
	return suite
}

func passingTrials(suite EvalSuite) []TrialResult {
	out := make([]TrialResult, 0, len(suite.Cases)*suite.Trials)
	for _, item := range suite.Cases {
		for trial := 1; trial <= suite.Trials; trial++ {
			graderHash, err := graderDefinitionHash(item)
			if err != nil {
				panic(err)
			}
			out = append(out, TrialResult{
				CaseID: item.CaseID, Trial: trial, Passed: true, Status: "passed",
				OutputHash: strings.Repeat("b", 64), LatencyMS: 10,
				InvocationID: fmt.Sprintf("%s-%d", item.CaseID, trial),
				Provider:     "local", Model: "deterministic-test", Attempts: 1,
				Grade: &GradeEvidence{
					Kind: item.Grader, Passed: true, GraderHash: graderHash,
				},
			})
		}
	}
	return out
}

func testReleaseSpec() ReleaseSpec {
	return ReleaseSpec{
		Instructions: "Use governed evidence and cite every claim.",
		Provider:     "local", Model: "deterministic-test",
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
		Tools: []ToolPolicy{{
			Name: "case_evidence", Mode: ToolAutomatic, Purpose: "case_review",
			ParameterSchema: json.RawMessage(`{"type":"object"}`),
		}},
		DataPurposes: []string{"case_review"},
		Dependencies: []DependencyPin{{
			Kind: "prompt", Name: "case-policy", Version: "1", Hash: strings.Repeat("c", 64),
		}},
		Budget: Budget{
			MaxPromptTokens: 1000, MaxCompletionTokens: 500,
			MaxToolCalls: 2, MaxCostUSD: 0.10,
			PricingSource: "test-fixture", PricingVersion: "1",
		},
		TimeoutMS: 10_000, MaxAttempts: 2, RequireCitations: true, RequireHumanGate: true,
	}
}
