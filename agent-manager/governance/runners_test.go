// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/identity"
)

type governanceProvider struct{}

type governanceToolbox struct{}

type semanticGraderProvider struct {
	request ai.Request
}

func (provider *semanticGraderProvider) Name() string { return "semantic-grader-test" }

func (provider *semanticGraderProvider) Complete(
	_ context.Context,
	request ai.Request,
) (ai.Response, error) {
	provider.request = request
	return ai.Response{
		Structured: json.RawMessage(`{
			"score":0.82,
			"rationale":"The response preserves the governed meaning."
		}`),
		Usage: ai.Usage{PromptTokens: 40, CompletionTokens: 12},
	}, nil
}

func (governanceToolbox) Spec(name string) (ai.Tool, bool) {
	if name != "case_evidence" {
		return ai.Tool{}, false
	}
	return ai.Tool{
		Name: name, Description: "Read governed case evidence",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}, true
}

type humanToolProvider struct {
	arguments json.RawMessage
}

func (provider *humanToolProvider) Name() string { return "human-tool-test" }

func (provider *humanToolProvider) Complete(
	_ context.Context,
	request ai.Request,
) (ai.Response, error) {
	if len(request.History) == 0 {
		return ai.Response{
			ToolCalls: []ai.ToolCall{{
				ID: "tool-call-1", Name: "case_evidence", Arguments: provider.arguments,
			}},
			Usage: ai.Usage{PromptTokens: 20, CompletionTokens: 5},
		}, nil
	}
	return ai.Response{
		Structured: json.RawMessage(`{
			"suggestion":{"summary":"Tool-supported"},
			"citations":[{"evidence_id":"evidence-1","claim":"Supported by evidence"}],
			"confidence":0.9,
			"limitations":[]
		}`),
		Usage: ai.Usage{PromptTokens: 25, CompletionTokens: 10},
	}, nil
}

type recordingToolbox struct {
	calls int
}

func (toolbox *recordingToolbox) Spec(name string) (ai.Tool, bool) {
	return governanceToolbox{}.Spec(name)
}

func (toolbox *recordingToolbox) Call(
	_ context.Context,
	_ identity.Identity,
	name string,
	arguments json.RawMessage,
) (json.RawMessage, error) {
	if name != "case_evidence" || string(arguments) != `{"evidence_id":"evidence-1"}` {
		return nil, errors.New("unexpected tool invocation")
	}
	toolbox.calls++
	return json.RawMessage(`{"risk":"low"}`), nil
}

func (governanceToolbox) Call(
	_ context.Context,
	_ identity.Identity,
	_ string,
	_ json.RawMessage,
) (json.RawMessage, error) {
	return json.RawMessage(`{"status":"ok"}`), nil
}

func (governanceProvider) Name() string { return "governance-test" }

func (governanceProvider) Complete(_ context.Context, request ai.Request) (ai.Response, error) {
	if strings.Contains(request.System, "Every factual claim") {
		return ai.Response{
			Structured: json.RawMessage(`{
				"suggestion":{"summary":"Supported"},
				"citations":[{"evidence_id":"evidence-1","claim":"Supported by evidence"}],
				"confidence":0.8,
				"limitations":["Reviewer verification required"]
			}`),
			Usage: ai.Usage{PromptTokens: 100, CompletionTokens: 50},
		}, nil
	}
	value := "summary"
	if strings.Contains(request.Prompt, "Ignore the policy") {
		value = "cannot"
	}
	return ai.Response{
		Structured: json.RawMessage(`{"value":"` + value + `"}`),
		Usage:      ai.Usage{PromptTokens: 100, CompletionTokens: 20},
	}, nil
}

func TestEvalRunnerExecutesEveryRepeatedTrial(t *testing.T) {
	registry := ai.NewRegistry()
	registry.Register(governanceProvider{})
	runner := NewEvalRunner(registry)
	spec := testReleaseSpec()
	spec.Provider = "governance-test"
	trials, err := runner.Run(
		context.Background(), "evaluator", ReleaseView{Spec: spec}, testSuite(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(trials) != 4 {
		t.Fatalf("trial count = %d, want 4", len(trials))
	}
	for _, trial := range trials {
		if !trial.Passed || trial.Status != "passed" ||
			trial.PromptTokens != 100 || trial.OutputHash == "" {
			t.Fatalf("trial = %+v", trial)
		}
	}
}

func TestEvalRunnerRecordsGovernedSemanticGraderEvidence(t *testing.T) {
	registry := ai.NewRegistry()
	registry.Register(governanceProvider{})
	grader := &semanticGraderProvider{}
	registry.Register(grader)
	runner := NewEvalRunner(registry)
	spec := testReleaseSpec()
	spec.Provider = "governance-test"
	suite := testSuite()
	suite.Trials = 1
	suite.Cases = []EvalCase{{
		CaseID: "meaning", Name: "Equivalent meaning",
		Prompt: "Summarize the evidence", Trust: TrustGoverned,
		Purpose: "case_review", Grader: GraderSemantic,
		Rubric:   "Score whether the response preserves the material governed meaning.",
		MinScore: 0.8, Severity: SeverityRequired,
	}}
	suite.SemanticGrader = &SemanticGraderSpec{
		Provider: "semantic-grader-test", Model: "grader-model",
		Instructions: "Apply the governed rubric consistently.", Version: "rubric-engine-1",
		Budget: Budget{
			MaxPromptTokens: 500, MaxCompletionTokens: 100, MaxCostUSD: 0.05,
			PricingSource: "grader-contract", PricingVersion: "2026-07",
		},
		TimeoutMS: 5_000, MaxAttempts: 2,
	}
	trials, err := runner.Run(
		context.Background(), "evaluator", ReleaseView{
			TemplateID: "template", Release: 1, Spec: spec,
		}, suite,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(trials) != 1 || !trials[0].Passed || trials[0].Grade == nil {
		t.Fatalf("semantic trials = %+v", trials)
	}
	grade := trials[0].Grade
	if grade.Kind != GraderSemantic || grade.Score != 0.82 ||
		grade.Provider != "semantic-grader-test" || grade.Model != "grader-model" ||
		grade.InvocationID == "" || grade.RubricHash == "" ||
		grade.GraderHash == "" || grade.OutputHash == "" {
		t.Fatalf("semantic grade evidence = %+v", grade)
	}
	if !strings.Contains(grader.request.System, "untrusted generated data") ||
		!strings.Contains(grader.request.Prompt, `"trust":"generated"`) ||
		!strings.Contains(grader.request.Prompt, trials[0].OutputHash) {
		t.Fatalf("semantic grader trust boundary = %+v", grader.request)
	}
}

func TestAssistRunnerKeepsEvidenceAndReleaseLineage(t *testing.T) {
	registry := ai.NewRegistry()
	registry.Register(governanceProvider{})
	runner := NewAssistRunner(registry).WithToolbox(governanceToolbox{})
	spec := testReleaseSpec()
	spec.Provider = "governance-test"
	result, err := runner.Run(context.Background(), AssistRequested{
		AssistID: "assist-1", CaseID: "case-1", Kind: AssistSummary,
		TemplateID: "template-1", Release: 4, Environment: "production",
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: 99,
	}, ReleaseView{
		TemplateID: "template-1", Release: 4, Spec: spec,
	}, cases.CaseView{
		CaseID: "case-1", CaseType: "underwriting",
		Evidence: []cases.EvidenceLink{{
			EvidenceID: "evidence-1", Kind: "document",
			SubjectType: "entity", SubjectID: "customer-1", Label: "Application",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AssistID != "assist-1" || result.Release != 4 ||
		result.EvidenceSeq != 99 || len(result.Citations) != 1 ||
		result.Citations[0].EvidenceID != "evidence-1" {
		t.Fatalf("assist result = %+v", result)
	}
}

func TestAssistRunnerRequiresExactHumanApprovalBeforeToolEffect(t *testing.T) {
	provider := &humanToolProvider{
		arguments: json.RawMessage(`{"evidence_id":"evidence-1"}`),
	}
	registry := ai.NewRegistry()
	registry.Register(provider)
	toolbox := &recordingToolbox{}
	runner := NewAssistRunner(registry).WithToolbox(toolbox)
	runner.newID = func() string { return "invocation-1" }
	spec := testReleaseSpec()
	spec.Provider = provider.Name()
	spec.Tools[0].Mode = ToolHumanBeforeCall
	request := AssistRequested{
		AssistID: "assist-1", CaseID: "case-1", Kind: AssistSummary,
		TemplateID: "template-1", Release: 1, Environment: "production",
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: 99, RequestedBy: "reviewer",
	}
	release := ReleaseView{
		Org: "acme", Workspace: "risk", TemplateID: request.TemplateID,
		Release: request.Release, Spec: spec,
	}
	caseView := cases.CaseView{
		CaseID: request.CaseID, CaseType: "underwriting",
		Evidence: []cases.EvidenceLink{{
			EvidenceID: "evidence-1", Kind: "document",
			SubjectType: "entity", SubjectID: "customer-1", Label: "Application",
		}},
	}
	_, err := runner.Run(context.Background(), request, release, caseView)
	var needed *ToolApprovalNeededError
	if !errors.As(err, &needed) {
		t.Fatalf("initial run error = %v, want tool approval", err)
	}
	if toolbox.calls != 0 {
		t.Fatalf("tool executed %d times before approval", toolbox.calls)
	}
	result, err := runner.RunWithToolApproval(
		context.Background(), request, release, caseView, ApprovedToolCall{
			ApprovalID: "approval-1", InvocationID: needed.InvocationID,
			CallID: needed.CallID, Name: needed.Name,
			ArgumentsHash: needed.ArgumentsHash, ApprovedBy: "checker",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if toolbox.calls != 1 || len(result.ToolCalls) != 1 ||
		result.ToolCalls[0].ApprovedBy != "checker" ||
		result.InvocationID != needed.InvocationID {
		t.Fatalf("approved result = %+v, toolbox calls = %d", result, toolbox.calls)
	}
}

func TestAssistRunnerRejectsChangedToolArgumentsAfterApproval(t *testing.T) {
	provider := &humanToolProvider{
		arguments: json.RawMessage(`{"evidence_id":"evidence-1"}`),
	}
	registry := ai.NewRegistry()
	registry.Register(provider)
	toolbox := &recordingToolbox{}
	runner := NewAssistRunner(registry).WithToolbox(toolbox)
	spec := testReleaseSpec()
	spec.Provider = provider.Name()
	spec.Tools[0].Mode = ToolHumanBeforeCall
	request := AssistRequested{
		AssistID: "assist-1", CaseID: "case-1", Kind: AssistSummary,
		TemplateID: "template-1", Release: 1, Environment: "production",
		EvidenceIDs: []string{"evidence-1"}, EvidenceSeq: 99, RequestedBy: "reviewer",
	}
	release := ReleaseView{
		Org: "acme", Workspace: "risk", TemplateID: request.TemplateID,
		Release: request.Release, Spec: spec,
	}
	caseView := cases.CaseView{
		CaseID: request.CaseID, CaseType: "underwriting",
		Evidence: []cases.EvidenceLink{{
			EvidenceID: "evidence-1", Kind: "document",
			SubjectType: "entity", SubjectID: "customer-1", Label: "Application",
		}},
	}
	_, err := runner.Run(context.Background(), request, release, caseView)
	var needed *ToolApprovalNeededError
	if !errors.As(err, &needed) {
		t.Fatalf("initial run error = %v, want tool approval", err)
	}
	provider.arguments = json.RawMessage(`{"evidence_id":"different"}`)
	_, err = runner.RunWithToolApproval(
		context.Background(), request, release, caseView, ApprovedToolCall{
			ApprovalID: "approval-1", InvocationID: needed.InvocationID,
			CallID: needed.CallID, Name: needed.Name,
			ArgumentsHash: needed.ArgumentsHash, ApprovedBy: "checker",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match human approval") {
		t.Fatalf("changed-argument error = %v", err)
	}
	if toolbox.calls != 0 {
		t.Fatalf("changed tool executed %d times", toolbox.calls)
	}
}
