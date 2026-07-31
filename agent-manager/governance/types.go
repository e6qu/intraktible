// SPDX-License-Identifier: AGPL-3.0-or-later

// Package governance defines the pure governed-agent lifecycle and evidence
// contracts. I/O belongs in Handler and Service; these types validate without
// reading clocks, stores, providers, or the event log.
package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	engine "github.com/e6qu/intraktible/decision-engine/domain"
)

const (
	MaxInstructionsBytes = 64 * 1024
	MaxSchemaBytes       = 64 * 1024
	MaxTools             = 64
	MaxPurposes          = 32
	MaxCases             = 500
	MaxTrials            = 50
)

// ReleaseStatus is the governed state of one immutable agent release.
type ReleaseStatus string

const (
	ReleaseDraft           ReleaseStatus = "draft"
	ReleaseEvaluated       ReleaseStatus = "evaluated"
	ReleaseReviewRequested ReleaseStatus = "review_requested"
	ReleaseApproved        ReleaseStatus = "approved"
	ReleaseRejected        ReleaseStatus = "rejected"
	ReleaseRetired         ReleaseStatus = "retired"
)

func (s ReleaseStatus) Valid() bool {
	switch s {
	case ReleaseDraft, ReleaseEvaluated, ReleaseReviewRequested,
		ReleaseApproved, ReleaseRejected, ReleaseRetired:
		return true
	default:
		return false
	}
}

// DeploymentStatus is the operational state of an exact environment binding.
type DeploymentStatus string

const (
	DeploymentScheduled DeploymentStatus = "scheduled"
	DeploymentActive    DeploymentStatus = "active"
	DeploymentPaused    DeploymentStatus = "paused"
	DeploymentRetired   DeploymentStatus = "retired"
)

func (s DeploymentStatus) Valid() bool {
	switch s {
	case DeploymentScheduled, DeploymentActive, DeploymentPaused, DeploymentRetired:
		return true
	default:
		return false
	}
}

// ToolApprovalMode controls whether a release may call a declared tool.
type ToolApprovalMode string

const (
	ToolAutomatic       ToolApprovalMode = "automatic"
	ToolHumanBeforeCall ToolApprovalMode = "human_before_call"
	ToolForbidden       ToolApprovalMode = "forbidden"
)

func (m ToolApprovalMode) Valid() bool {
	return m == ToolAutomatic || m == ToolHumanBeforeCall || m == ToolForbidden
}

type ToolApprovalStatus string

const (
	ToolApprovalPending       ToolApprovalStatus = "pending"
	ToolApprovalApproved      ToolApprovalStatus = "approved"
	ToolApprovalRejected      ToolApprovalStatus = "rejected"
	ToolApprovalExpiredStatus ToolApprovalStatus = "expired"
)

func (s ToolApprovalStatus) Valid() bool {
	switch s {
	case ToolApprovalPending, ToolApprovalApproved, ToolApprovalRejected, ToolApprovalExpiredStatus:
		return true
	default:
		return false
	}
}

// ContentTrust is the immutable trust label carried with model-visible input.
type ContentTrust string

const (
	TrustPlatform  ContentTrust = "platform"
	TrustGoverned  ContentTrust = "governed"
	TrustUser      ContentTrust = "user"
	TrustExternal  ContentTrust = "external"
	TrustTool      ContentTrust = "tool"
	TrustGenerated ContentTrust = "generated"
)

func (t ContentTrust) Valid() bool {
	switch t {
	case TrustPlatform, TrustGoverned, TrustUser, TrustExternal, TrustTool, TrustGenerated:
		return true
	default:
		return false
	}
}

// GraderKind is the deterministic scoring rule for an evaluation case.
type GraderKind string

const (
	GraderContains    GraderKind = "contains"
	GraderEquals      GraderKind = "equals"
	GraderJSONSubset  GraderKind = "json_subset"
	GraderRefusal     GraderKind = "refusal"
	GraderNoToolCalls GraderKind = "no_tool_calls"
	GraderCitations   GraderKind = "citations"
	GraderSemantic    GraderKind = "semantic"
)

func (g GraderKind) Valid() bool {
	switch g {
	case GraderContains, GraderEquals, GraderJSONSubset, GraderRefusal,
		GraderNoToolCalls, GraderCitations, GraderSemantic:
		return true
	default:
		return false
	}
}

// Severity controls whether a failed case blocks release.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityRequired Severity = "required"
	SeverityCritical Severity = "critical"
)

func (s Severity) Valid() bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityRequired, SeverityCritical:
		return true
	default:
		return false
	}
}

// ReviewDecision is an independent maker-checker decision.
type ReviewDecision string

const (
	ReviewApprove ReviewDecision = "approve"
	ReviewReject  ReviewDecision = "reject"
)

func (d ReviewDecision) Valid() bool { return d == ReviewApprove || d == ReviewReject }

// AssistKind is one bounded form of reviewer assistance.
type AssistKind string

const (
	AssistSummary          AssistKind = "summary"
	AssistEvidenceExtract  AssistKind = "evidence_extraction"
	AssistPrioritization   AssistKind = "prioritization"
	AssistNextBestAction   AssistKind = "next_best_action"
	AssistDraftDisposition AssistKind = "draft_disposition"
)

func (k AssistKind) Valid() bool {
	switch k {
	case AssistSummary, AssistEvidenceExtract, AssistPrioritization,
		AssistNextBestAction, AssistDraftDisposition:
		return true
	default:
		return false
	}
}

type AssistPolicySource struct {
	Kind                string `json:"kind"`
	Key                 string `json:"key"`
	ConfigurationSeq    uint64 `json:"configuration_seq"`
	PolicyKey           string `json:"policy_key"`
	EvidenceFingerprint string `json:"evidence_fingerprint"`
}

func (s AssistPolicySource) Validate() error {
	if (s.Kind != "case_type" && s.Kind != "queue") ||
		strings.TrimSpace(s.Key) == "" || s.ConfigurationSeq < 1 ||
		strings.TrimSpace(s.PolicyKey) == "" ||
		!validSHA256(s.EvidenceFingerprint) {
		return errors.New("agent governance: assist policy source lineage is incomplete")
	}
	return nil
}

// AssistAction records what the accountable reviewer did with a suggestion.
type AssistAction string

const (
	AssistAccepted  AssistAction = "accepted"
	AssistEdited    AssistAction = "edited"
	AssistRejected  AssistAction = "rejected"
	AssistEscalated AssistAction = "escalated"
)

func (a AssistAction) Valid() bool {
	switch a {
	case AssistAccepted, AssistEdited, AssistRejected, AssistEscalated:
		return true
	default:
		return false
	}
}

type AssistStatus string

const (
	AssistRequestedStatus        AssistStatus = "requested"
	AssistRunningStatus          AssistStatus = "running"
	AssistAwaitingApprovalStatus AssistStatus = "awaiting_tool_approval"
	AssistCompletedStatus        AssistStatus = "completed"
	AssistFailedStatus           AssistStatus = "failed"
	AssistDeadLetterStatus       AssistStatus = "dead_letter"
	AssistCancelledStatus        AssistStatus = "cancelled"
)

func (s AssistStatus) Valid() bool {
	switch s {
	case AssistRequestedStatus, AssistRunningStatus, AssistAwaitingApprovalStatus,
		AssistCompletedStatus, AssistFailedStatus, AssistDeadLetterStatus,
		AssistCancelledStatus:
		return true
	default:
		return false
	}
}

// Template is the reusable library identity from which immutable releases are
// created. High-impact merely requires a human gate; it does not grant the
// agent authority to make the terminal decision.
type Template struct {
	TemplateID  string   `json:"template_id"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Task        string   `json:"task"`
	Tags        []string `json:"tags,omitempty"`
	HighImpact  bool     `json:"high_impact"`
}

func (t Template) Validate() error {
	if strings.TrimSpace(t.TemplateID) == "" {
		return errors.New("agent governance: template_id is required")
	}
	if !validSlug(t.Slug) {
		return fmt.Errorf("agent governance: invalid template slug %q", t.Slug)
	}
	if strings.TrimSpace(t.Name) == "" || strings.TrimSpace(t.Task) == "" {
		return errors.New("agent governance: template name and task are required")
	}
	if err := rejectTextPII(
		"template text", t.Name, t.Description, t.Task, strings.Join(t.Tags, " "),
	); err != nil {
		return err
	}
	return uniqueNonBlank("template tag", t.Tags, 32)
}

// Budget is enforced independently of model instructions. A zero period budget
// means no period aggregate is configured; per-run limits remain required.
type Budget struct {
	MaxPromptTokens     int     `json:"max_prompt_tokens"`
	MaxCompletionTokens int     `json:"max_completion_tokens"`
	MaxToolCalls        int     `json:"max_tool_calls"`
	MaxCostUSD          float64 `json:"max_cost_usd"`
	InputCostPerMTok    float64 `json:"input_cost_per_mtok"`
	OutputCostPerMTok   float64 `json:"output_cost_per_mtok"`
	PricingSource       string  `json:"pricing_source"`
	PricingVersion      string  `json:"pricing_version"`
	Period              string  `json:"period,omitempty"`
	PeriodCostUSD       float64 `json:"period_cost_usd,omitempty"`
}

func (b Budget) Validate() error {
	if b.MaxPromptTokens < 1 || b.MaxCompletionTokens < 1 {
		return errors.New("agent governance: positive prompt and completion token budgets are required")
	}
	if b.MaxToolCalls < 0 || b.MaxToolCalls > 100 {
		return errors.New("agent governance: max_tool_calls must be between 0 and 100")
	}
	if b.MaxCostUSD <= 0 {
		return errors.New("agent governance: max_cost_usd must be positive")
	}
	if b.InputCostPerMTok < 0 || b.OutputCostPerMTok < 0 ||
		math.IsNaN(b.InputCostPerMTok) || math.IsNaN(b.OutputCostPerMTok) ||
		math.IsInf(b.InputCostPerMTok, 0) || math.IsInf(b.OutputCostPerMTok, 0) {
		return errors.New("agent governance: reviewed model prices must be finite and non-negative")
	}
	if strings.TrimSpace(b.PricingSource) == "" || strings.TrimSpace(b.PricingVersion) == "" {
		return errors.New(
			"agent governance: pricing_source and pricing_version are required for cost enforcement",
		)
	}
	if b.Period == "" {
		if b.PeriodCostUSD != 0 {
			return errors.New("agent governance: period_cost_usd requires a period")
		}
		return nil
	}
	if b.Period != "day" && b.Period != "month" {
		return errors.New("agent governance: budget period must be day or month")
	}
	if b.PeriodCostUSD <= 0 {
		return errors.New("agent governance: period_cost_usd must be positive")
	}
	return nil
}

// ToolPolicy is an independently enforced capability grant. ParameterSchema
// constrains model-supplied arguments before the toolbox is invoked.
type ToolPolicy struct {
	Name            string           `json:"name"`
	Mode            ToolApprovalMode `json:"mode"`
	Purpose         string           `json:"purpose"`
	ParameterSchema json.RawMessage  `json:"parameter_schema,omitempty"`
}

func (p ToolPolicy) Validate() error {
	if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Purpose) == "" {
		return errors.New("agent governance: tool name and purpose are required")
	}
	if !p.Mode.Valid() {
		return fmt.Errorf("agent governance: invalid tool approval mode %q", p.Mode)
	}
	if err := rejectTextPII("tool policy", p.Name, p.Purpose); err != nil {
		return err
	}
	if err := rejectJSONPII("tool parameter schema", p.ParameterSchema); err != nil {
		return err
	}
	return validateJSONObject("tool parameter schema", p.ParameterSchema)
}

// DependencyPin makes the provider/model/data/template inputs reviewed with a
// release explicit. Material dependency changes expire approval.
type DependencyPin struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Hash    string `json:"hash,omitempty"`
}

func (p DependencyPin) Validate() error {
	if strings.TrimSpace(p.Kind) == "" || strings.TrimSpace(p.Name) == "" ||
		strings.TrimSpace(p.Version) == "" {
		return errors.New("agent governance: dependency kind, name, and version are required")
	}
	if len(p.Hash) != sha256.Size*2 {
		return errors.New("agent governance: dependency hash must be SHA-256")
	}
	if _, err := hex.DecodeString(p.Hash); err != nil {
		return errors.New("agent governance: dependency hash must be hexadecimal SHA-256")
	}
	return nil
}

// CircuitBreakerPolicy latches a deployment closed when recent governed
// execution attempts cross a reviewed failure-rate threshold. Recovery is an
// explicit incident resolution followed by deployment resume.
type CircuitBreakerPolicy struct {
	WindowMinutes int     `json:"window_minutes"`
	MinSamples    int     `json:"min_samples"`
	FailureRate   float64 `json:"failure_rate"`
}

func (p CircuitBreakerPolicy) Validate() error {
	if p.WindowMinutes < 1 || p.WindowMinutes > 7*24*60 {
		return errors.New(
			"agent governance: circuit breaker window_minutes must be between 1 and 10080",
		)
	}
	if p.MinSamples < 1 || p.MinSamples > 100_000 {
		return errors.New(
			"agent governance: circuit breaker min_samples must be between 1 and 100000",
		)
	}
	if math.IsNaN(p.FailureRate) || math.IsInf(p.FailureRate, 0) ||
		p.FailureRate <= 0 || p.FailureRate > 1 {
		return errors.New(
			"agent governance: circuit breaker failure_rate must be greater than 0 and at most 1",
		)
	}
	return nil
}

// ReleaseSpec is immutable once its release is created.
type ReleaseSpec struct {
	Instructions          string                `json:"instructions"`
	Provider              string                `json:"provider"`
	Model                 string                `json:"model"`
	InputSchema           json.RawMessage       `json:"input_schema"`
	OutputSchema          json.RawMessage       `json:"output_schema"`
	Tools                 []ToolPolicy          `json:"tools"`
	DataPurposes          []string              `json:"data_purposes"`
	Dependencies          []DependencyPin       `json:"dependencies"`
	Budget                Budget                `json:"budget"`
	TimeoutMS             int64                 `json:"timeout_ms"`
	MaxAttempts           int                   `json:"max_attempts"`
	CircuitBreaker        *CircuitBreakerPolicy `json:"circuit_breaker,omitempty"`
	RequireCitations      bool                  `json:"require_citations"`
	RequireHumanGate      bool                  `json:"require_human_gate"`
	AllowRemoteAgent      bool                  `json:"allow_remote_agent"`
	RemoteProtocolURL     string                `json:"remote_protocol_url,omitempty"`
	RemoteProtocolVersion string                `json:"remote_protocol_version,omitempty"`
	RemoteCredentialEnv   string                `json:"remote_credential_env,omitempty"`
}

func (s ReleaseSpec) Validate() error {
	if strings.TrimSpace(s.Instructions) == "" || len(s.Instructions) > MaxInstructionsBytes {
		return fmt.Errorf("agent governance: instructions must be 1..%d bytes", MaxInstructionsBytes)
	}
	if strings.TrimSpace(s.Provider) == "" || strings.TrimSpace(s.Model) == "" {
		return errors.New("agent governance: provider and model are required")
	}
	if err := rejectTextPII("release instructions", s.Instructions); err != nil {
		return err
	}
	if err := rejectJSONPII(
		"release schema", s.InputSchema, s.OutputSchema,
	); err != nil {
		return err
	}
	if err := validateJSONObject("input schema", s.InputSchema); err != nil {
		return err
	}
	if err := validateJSONObject("output schema", s.OutputSchema); err != nil {
		return err
	}
	if len(s.Tools) > MaxTools {
		return fmt.Errorf("agent governance: too many tools (%d > %d)", len(s.Tools), MaxTools)
	}
	toolNames := map[string]bool{}
	humanApprovalTools := 0
	for _, tool := range s.Tools {
		if err := tool.Validate(); err != nil {
			return err
		}
		if toolNames[tool.Name] {
			return fmt.Errorf("agent governance: duplicate tool %q", tool.Name)
		}
		toolNames[tool.Name] = true
		if tool.Mode == ToolHumanBeforeCall {
			humanApprovalTools++
		}
	}
	if humanApprovalTools > 1 {
		return errors.New(
			"agent governance: a release may declare at most one human-before-call tool",
		)
	}
	if len(s.DataPurposes) == 0 || len(s.DataPurposes) > MaxPurposes {
		return fmt.Errorf("agent governance: 1..%d data purposes are required", MaxPurposes)
	}
	if err := uniqueNonBlank("data purpose", s.DataPurposes, MaxPurposes); err != nil {
		return err
	}
	for _, pin := range s.Dependencies {
		if err := pin.Validate(); err != nil {
			return err
		}
	}
	if err := s.Budget.Validate(); err != nil {
		return err
	}
	if s.TimeoutMS < 1 || s.TimeoutMS > int64((10*time.Minute)/time.Millisecond) {
		return errors.New("agent governance: timeout_ms must be between 1 and 600000")
	}
	if s.MaxAttempts < 1 || s.MaxAttempts > 10 {
		return errors.New("agent governance: max_attempts must be between 1 and 10")
	}
	if s.CircuitBreaker != nil {
		if err := s.CircuitBreaker.Validate(); err != nil {
			return err
		}
	}
	if !s.AllowRemoteAgent {
		if strings.TrimSpace(s.RemoteProtocolURL) != "" ||
			strings.TrimSpace(s.RemoteProtocolVersion) != "" ||
			strings.TrimSpace(s.RemoteCredentialEnv) != "" {
			return errors.New(
				"agent governance: remote protocol fields require allow_remote_agent",
			)
		}
		return nil
	}
	remoteURL, err := url.Parse(s.RemoteProtocolURL)
	if err != nil || (remoteURL.Scheme != "https" && remoteURL.Scheme != "http") ||
		remoteURL.Host == "" || remoteURL.User != nil || remoteURL.Fragment != "" {
		return errors.New("agent governance: remote_protocol_url must be an absolute HTTP(S) URL")
	}
	if s.RemoteProtocolVersion != RemoteProtocolVersion {
		return fmt.Errorf(
			"agent governance: remote_protocol_version must be %q", RemoteProtocolVersion,
		)
	}
	if !validEnvName(s.RemoteCredentialEnv) {
		return errors.New(
			"agent governance: remote_credential_env must be an uppercase environment variable name",
		)
	}
	return nil
}

// EvalCase is immutable suite material. Untrusted content remains separated
// from the governed prompt so it cannot rewrite system policy by concatenation.
type EvalCase struct {
	CaseID           string          `json:"case_id"`
	Name             string          `json:"name"`
	Prompt           string          `json:"prompt"`
	UntrustedContent string          `json:"untrusted_content,omitempty"`
	Trust            ContentTrust    `json:"trust"`
	Purpose          string          `json:"purpose"`
	Grader           GraderKind      `json:"grader"`
	ExpectText       string          `json:"expect_text,omitempty"`
	ExpectJSON       json.RawMessage `json:"expect_json,omitempty"`
	AllowedCitations []string        `json:"allowed_citations,omitempty"`
	Rubric           string          `json:"rubric,omitempty"`
	MinScore         float64         `json:"min_score,omitempty"`
	Severity         Severity        `json:"severity"`
	Segment          string          `json:"segment,omitempty"`
	Tags             []string        `json:"tags,omitempty"`
}

func (c EvalCase) Validate() error {
	if strings.TrimSpace(c.CaseID) == "" || strings.TrimSpace(c.Name) == "" ||
		strings.TrimSpace(c.Prompt) == "" || strings.TrimSpace(c.Purpose) == "" {
		return errors.New("agent governance: eval case id, name, prompt, and purpose are required")
	}
	if !c.Trust.Valid() || !c.Grader.Valid() || !c.Severity.Valid() {
		return errors.New("agent governance: eval case trust, grader, or severity is invalid")
	}
	if err := rejectTextPII(
		"evaluation case text", c.Name, c.Prompt, c.UntrustedContent,
		c.ExpectText, c.Segment, strings.Join(c.Tags, " "),
		c.Rubric,
	); err != nil {
		return err
	}
	if err := rejectJSONPII("evaluation expectation", c.ExpectJSON); err != nil {
		return err
	}
	switch c.Grader {
	case GraderContains, GraderEquals, GraderRefusal:
		if strings.TrimSpace(c.ExpectText) == "" {
			return errors.New("agent governance: text grader requires expect_text")
		}
	case GraderJSONSubset:
		if err := validateJSONObject("eval expectation", c.ExpectJSON); err != nil {
			return err
		}
	case GraderCitations:
		if len(c.AllowedCitations) == 0 {
			return errors.New("agent governance: citation grader requires allowed citations")
		}
	case GraderSemantic:
		if strings.TrimSpace(c.Rubric) == "" || c.MinScore <= 0 || c.MinScore > 1 {
			return errors.New(
				"agent governance: semantic grader requires a rubric and min_score within (0,1]",
			)
		}
	}
	return uniqueNonBlank("eval tag", c.Tags, 32)
}

type SemanticGraderSpec struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
	Version      string `json:"version"`
	Budget       Budget `json:"budget"`
	TimeoutMS    int    `json:"timeout_ms"`
	MaxAttempts  int    `json:"max_attempts"`
}

func (s SemanticGraderSpec) Validate() error {
	if strings.TrimSpace(s.Provider) == "" || strings.TrimSpace(s.Model) == "" ||
		strings.TrimSpace(s.Instructions) == "" || strings.TrimSpace(s.Version) == "" {
		return errors.New(
			"agent governance: semantic grader provider, model, instructions, and version are required",
		)
	}
	if err := rejectTextPII(
		"semantic grader specification",
		s.Provider, s.Model, s.Instructions, s.Version,
	); err != nil {
		return err
	}
	if err := s.Budget.Validate(); err != nil {
		return err
	}
	if s.Budget.MaxToolCalls != 0 {
		return errors.New("agent governance: semantic graders cannot invoke tools")
	}
	if s.TimeoutMS < 1 || s.TimeoutMS > 10*60*1000 || s.MaxAttempts < 1 || s.MaxAttempts > 10 {
		return errors.New(
			"agent governance: semantic grader timeout and attempts are out of bounds",
		)
	}
	return nil
}

// EvalSuite is an immutable, versioned dataset and its acceptance threshold.
type EvalSuite struct {
	SuiteID        string              `json:"suite_id"`
	Version        int                 `json:"version"`
	Name           string              `json:"name"`
	Description    string              `json:"description,omitempty"`
	Adversarial    bool                `json:"adversarial"`
	Required       bool                `json:"required"`
	Trials         int                 `json:"trials"`
	MinPassRate    float64             `json:"min_pass_rate"`
	MaxVariance    float64             `json:"max_variance"`
	Cases          []EvalCase          `json:"cases"`
	SemanticGrader *SemanticGraderSpec `json:"semantic_grader,omitempty"`
	DatasetHash    string              `json:"dataset_hash"`
}

func (s EvalSuite) Validate() error {
	if strings.TrimSpace(s.SuiteID) == "" || s.Version < 1 || strings.TrimSpace(s.Name) == "" {
		return errors.New("agent governance: suite id, positive version, and name are required")
	}
	if s.Trials < 1 || s.Trials > MaxTrials {
		return fmt.Errorf("agent governance: trials must be 1..%d", MaxTrials)
	}
	if err := rejectTextPII("evaluation suite text", s.Name, s.Description); err != nil {
		return err
	}
	if s.MinPassRate < 0 || s.MinPassRate > 1 || s.MaxVariance < 0 || s.MaxVariance > 1 {
		return errors.New("agent governance: pass rate and variance thresholds must be within [0,1]")
	}
	if len(s.Cases) == 0 || len(s.Cases) > MaxCases {
		return fmt.Errorf("agent governance: suite requires 1..%d cases", MaxCases)
	}
	ids := map[string]bool{}
	needsSemantic := false
	for _, item := range s.Cases {
		if err := item.Validate(); err != nil {
			return err
		}
		if ids[item.CaseID] {
			return fmt.Errorf("agent governance: duplicate eval case %q", item.CaseID)
		}
		ids[item.CaseID] = true
		needsSemantic = needsSemantic || item.Grader == GraderSemantic
	}
	if needsSemantic {
		if s.SemanticGrader == nil {
			return errors.New(
				"agent governance: semantic evaluation cases require an exact grader specification",
			)
		}
		if err := s.SemanticGrader.Validate(); err != nil {
			return err
		}
	} else if s.SemanticGrader != nil {
		return errors.New(
			"agent governance: semantic grader specification has no semantic cases",
		)
	}
	if strings.TrimSpace(s.DatasetHash) == "" {
		return errors.New("agent governance: dataset_hash is required")
	}
	return nil
}

type GradeEvidence struct {
	Kind         GraderKind `json:"kind"`
	Score        float64    `json:"score,omitempty"`
	Passed       bool       `json:"passed"`
	Detail       string     `json:"detail,omitempty"`
	GraderHash   string     `json:"grader_hash"`
	RubricHash   string     `json:"rubric_hash,omitempty"`
	InvocationID string     `json:"invocation_id,omitempty"`
	Provider     string     `json:"provider,omitempty"`
	Model        string     `json:"model,omitempty"`
	Attempts     int        `json:"attempts,omitempty"`
	LatencyMS    int64      `json:"latency_ms,omitempty"`
	PromptTokens int        `json:"prompt_tokens,omitempty"`
	OutputTokens int        `json:"output_tokens,omitempty"`
	CostUSD      float64    `json:"cost_usd,omitempty"`
	OutputHash   string     `json:"output_hash,omitempty"`
}

func (g GradeEvidence) Validate() error {
	if !g.Kind.Valid() || !validSHA256(g.GraderHash) ||
		g.Score < 0 || g.Score > 1 || math.IsNaN(g.Score) || math.IsInf(g.Score, 0) ||
		g.LatencyMS < 0 || g.PromptTokens < 0 || g.OutputTokens < 0 || g.CostUSD < 0 ||
		math.IsNaN(g.CostUSD) || math.IsInf(g.CostUSD, 0) {
		return errors.New("agent governance: grader evidence is invalid")
	}
	if g.RubricHash != "" && !validSHA256(g.RubricHash) {
		return errors.New("agent governance: semantic rubric hash must be SHA-256")
	}
	if g.Kind == GraderSemantic {
		if g.InvocationID == "" || g.Provider == "" || g.Model == "" ||
			g.Attempts < 1 || !validSHA256(g.OutputHash) || g.RubricHash == "" {
			return errors.New("agent governance: semantic grader lineage is incomplete")
		}
	}
	return rejectTextPII(
		"evaluation grader evidence", g.Detail, g.Provider, g.Model,
	)
}

// TrialResult is one immutable provider outcome graded by deterministic policy.
type TrialResult struct {
	CaseID       string         `json:"case_id"`
	Trial        int            `json:"trial"`
	Passed       bool           `json:"passed"`
	Status       string         `json:"status"`
	Detail       string         `json:"detail,omitempty"`
	OutputHash   string         `json:"output_hash,omitempty"`
	Citations    []string       `json:"citations,omitempty"`
	ToolCalls    []string       `json:"tool_calls,omitempty"`
	InvocationID string         `json:"invocation_id,omitempty"`
	Provider     string         `json:"provider,omitempty"`
	Model        string         `json:"model,omitempty"`
	Attempts     int            `json:"attempts,omitempty"`
	LatencyMS    int64          `json:"latency_ms"`
	PromptTokens int            `json:"prompt_tokens,omitempty"`
	OutputTokens int            `json:"output_tokens,omitempty"`
	CostUSD      float64        `json:"cost_usd,omitempty"`
	Grade        *GradeEvidence `json:"grade,omitempty"`
}

func (r TrialResult) Validate() error {
	if strings.TrimSpace(r.CaseID) == "" || r.Trial < 1 {
		return errors.New("agent governance: trial case_id and positive trial number are required")
	}
	if strings.TrimSpace(r.InvocationID) == "" || strings.TrimSpace(r.Provider) == "" ||
		strings.TrimSpace(r.Model) == "" || r.Attempts < 1 {
		return errors.New("agent governance: trial invocation lineage is incomplete")
	}
	switch r.Status {
	case "passed", "failed", "provider_error", "grader_error", "budget_exceeded", "policy_violation",
		"human_approval_required":
	default:
		return fmt.Errorf("agent governance: invalid trial status %q", r.Status)
	}
	if r.Passed != (r.Status == "passed") {
		return errors.New("agent governance: trial passed flag and terminal status disagree")
	}
	if (r.Status == "passed" || r.Status == "failed") && r.OutputHash == "" {
		return errors.New("agent governance: graded trial requires an output_hash")
	}
	if (r.Status == "passed" || r.Status == "failed") && r.Grade == nil {
		return errors.New("agent governance: graded trial requires grader evidence")
	}
	if r.LatencyMS < 0 || r.PromptTokens < 0 || r.OutputTokens < 0 || r.CostUSD < 0 ||
		math.IsNaN(r.CostUSD) || math.IsInf(r.CostUSD, 0) {
		return errors.New(
			"agent governance: trial latency, token counts, and cost must be finite and non-negative",
		)
	}
	if r.OutputHash != "" {
		if len(r.OutputHash) != sha256.Size*2 {
			return errors.New("agent governance: trial output_hash must be SHA-256")
		}
		if _, err := hex.DecodeString(r.OutputHash); err != nil {
			return errors.New("agent governance: trial output_hash must be hexadecimal SHA-256")
		}
	}
	if r.Grade != nil {
		if err := r.Grade.Validate(); err != nil {
			return err
		}
		if r.Status != "grader_error" && r.Grade.Passed != r.Passed {
			return errors.New(
				"agent governance: grader evidence and trial result disagree",
			)
		}
	}
	if err := rejectTextPII(
		"evaluation trial evidence",
		append(
			append([]string{r.Detail}, r.Citations...),
			r.ToolCalls...,
		)...,
	); err != nil {
		return err
	}
	return uniqueNonBlank("trial citation", r.Citations, MaxCases)
}

// CampaignResult is the immutable evaluation evidence reviewed for a release.
type CampaignResult struct {
	CampaignID     string        `json:"campaign_id"`
	TemplateID     string        `json:"template_id"`
	Release        int           `json:"release"`
	SuiteID        string        `json:"suite_id"`
	SuiteVersion   int           `json:"suite_version"`
	Total          int           `json:"total"`
	Passed         int           `json:"passed"`
	PassRate       float64       `json:"pass_rate"`
	Variance       float64       `json:"variance"`
	ConfidenceLow  float64       `json:"confidence_low"`
	ConfidenceHigh float64       `json:"confidence_high"`
	Blocking       bool          `json:"blocking"`
	Trials         []TrialResult `json:"trials"`
}

func (r CampaignResult) Validate() error {
	if strings.TrimSpace(r.CampaignID) == "" || strings.TrimSpace(r.TemplateID) == "" ||
		r.Release < 1 || strings.TrimSpace(r.SuiteID) == "" || r.SuiteVersion < 1 {
		return errors.New("agent governance: campaign identity is incomplete")
	}
	if r.Total < 1 || r.Passed < 0 || r.Passed > r.Total || len(r.Trials) != r.Total {
		return errors.New("agent governance: campaign totals do not match trials")
	}
	if r.PassRate < 0 || r.PassRate > 1 || r.Variance < 0 ||
		r.ConfidenceLow < 0 || r.ConfidenceHigh > 1 || r.ConfidenceLow > r.ConfidenceHigh {
		return errors.New("agent governance: campaign statistics are invalid")
	}
	passed := 0
	for _, trial := range r.Trials {
		if err := trial.Validate(); err != nil {
			return err
		}
		if trial.Passed {
			passed++
		}
	}
	if passed != r.Passed {
		return errors.New("agent governance: campaign passed count does not match trials")
	}
	return nil
}

// TrialAdjudication is an accountable human judgment layered over immutable
// provider output. It never rewrites the original trial or its hashes.
type TrialAdjudication struct {
	CampaignID    string    `json:"campaign_id"`
	CaseID        string    `json:"case_id"`
	Trial         int       `json:"trial"`
	Passed        bool      `json:"passed"`
	Reason        string    `json:"reason"`
	AdjudicatedBy string    `json:"adjudicated_by"`
	AdjudicatedAt time.Time `json:"adjudicated_at"`
}

func (a TrialAdjudication) Validate() error {
	if strings.TrimSpace(a.CampaignID) == "" || strings.TrimSpace(a.CaseID) == "" ||
		a.Trial < 1 || strings.TrimSpace(a.Reason) == "" ||
		strings.TrimSpace(a.AdjudicatedBy) == "" || a.AdjudicatedAt.IsZero() {
		return errors.New("agent governance: trial adjudication is incomplete")
	}
	return rejectTextPII("trial adjudication reason", a.Reason)
}

// CampaignAssessment is the release-gate position after accountable
// adjudications are applied to the immutable trial evidence.
type CampaignAssessment struct {
	Total          int     `json:"total"`
	Passed         int     `json:"passed"`
	PassRate       float64 `json:"pass_rate"`
	Variance       float64 `json:"variance"`
	ConfidenceLow  float64 `json:"confidence_low"`
	ConfidenceHigh float64 `json:"confidence_high"`
	Blocking       bool    `json:"blocking"`
}

type CampaignComparisonRow struct {
	CaseID             string  `json:"case_id"`
	Segment            string  `json:"segment,omitempty"`
	Trials             int     `json:"trials"`
	BaselinePassRate   float64 `json:"baseline_pass_rate"`
	ChallengerPassRate float64 `json:"challenger_pass_rate"`
	DeltaPassRate      float64 `json:"delta_pass_rate"`
	Regression         bool    `json:"regression"`
}

type CampaignComparison struct {
	SuiteID              string                  `json:"suite_id"`
	SuiteVersion         int                     `json:"suite_version"`
	BaselineCampaignID   string                  `json:"baseline_campaign_id"`
	BaselineTemplateID   string                  `json:"baseline_template_id"`
	BaselineRelease      int                     `json:"baseline_release"`
	ChallengerCampaignID string                  `json:"challenger_campaign_id"`
	ChallengerTemplateID string                  `json:"challenger_template_id"`
	ChallengerRelease    int                     `json:"challenger_release"`
	Baseline             CampaignAssessment      `json:"baseline"`
	Challenger           CampaignAssessment      `json:"challenger"`
	DeltaPassRate        float64                 `json:"delta_pass_rate"`
	DeltaConfidenceLow   float64                 `json:"delta_confidence_low"`
	DeltaConfidenceHigh  float64                 `json:"delta_confidence_high"`
	BaselineCostUSD      float64                 `json:"baseline_cost_usd"`
	ChallengerCostUSD    float64                 `json:"challenger_cost_usd"`
	BaselineLatencyMS    int64                   `json:"baseline_latency_ms"`
	ChallengerLatencyMS  int64                   `json:"challenger_latency_ms"`
	Regressions          int                     `json:"regressions"`
	Improvements         int                     `json:"improvements"`
	Rows                 []CampaignComparisonRow `json:"rows"`
}

// DeploymentRequest binds an approved release to one environment.
type DeploymentRequest struct {
	DeploymentID string             `json:"deployment_id"`
	TemplateID   string             `json:"template_id"`
	Release      int                `json:"release"`
	Environment  engine.Environment `json:"environment"`
	At           *time.Time         `json:"at,omitempty"`
	ExpiresAt    *time.Time         `json:"expires_at,omitempty"`
	Reason       string             `json:"reason"`
}

func (r DeploymentRequest) Validate() error {
	if strings.TrimSpace(r.DeploymentID) == "" || strings.TrimSpace(r.TemplateID) == "" ||
		r.Release < 1 || !engine.ValidEnvironment(string(r.Environment)) || strings.TrimSpace(r.Reason) == "" {
		return errors.New("agent governance: deployment identity, release, environment, and reason are required")
	}
	if err := rejectTextPII("deployment reason", r.Reason); err != nil {
		return err
	}
	if r.ExpiresAt != nil && r.At != nil && !r.ExpiresAt.After(*r.At) {
		return errors.New("agent governance: deployment expiry must follow activation")
	}
	return nil
}

// Citation points only to governed evidence already visible on the case.
type Citation struct {
	EvidenceID string `json:"evidence_id"`
	Claim      string `json:"claim"`
	QuoteHash  string `json:"quote_hash,omitempty"`
}

func (c Citation) Validate() error {
	if strings.TrimSpace(c.EvidenceID) == "" || strings.TrimSpace(c.Claim) == "" {
		return errors.New("agent governance: citation evidence_id and claim are required")
	}
	if err := rejectTextPII("citation claim", c.Claim); err != nil {
		return err
	}
	return nil
}

// ToolExecution proves a platform-owned tool effect. Only hashes cross the
// event boundary; raw arguments and results stay in the transient worker and
// governed source system.
type ToolExecution struct {
	CallID        string           `json:"call_id"`
	Name          string           `json:"name"`
	Mode          ToolApprovalMode `json:"mode"`
	Purpose       string           `json:"purpose"`
	ArgumentsHash string           `json:"arguments_hash"`
	ResultHash    string           `json:"result_hash"`
	ApprovedBy    string           `json:"approved_by,omitempty"`
}

func (execution ToolExecution) Validate() error {
	if strings.TrimSpace(execution.CallID) == "" ||
		strings.TrimSpace(execution.Name) == "" ||
		!execution.Mode.Valid() ||
		strings.TrimSpace(execution.Purpose) == "" {
		return errors.New("agent governance: tool execution lineage is incomplete")
	}
	for name, value := range map[string]string{
		"arguments_hash": execution.ArgumentsHash,
		"result_hash":    execution.ResultHash,
	} {
		if len(value) != sha256.Size*2 {
			return fmt.Errorf("agent governance: tool %s must be SHA-256", name)
		}
		if _, err := hex.DecodeString(value); err != nil {
			return fmt.Errorf(
				"agent governance: tool %s must be hexadecimal SHA-256", name,
			)
		}
	}
	if execution.Mode == ToolHumanBeforeCall &&
		strings.TrimSpace(execution.ApprovedBy) == "" {
		return errors.New("agent governance: human-approved tool execution lacks approver")
	}
	return nil
}

// AssistResult is a typed suggestion. It never mutates case disposition.
type AssistResult struct {
	AssistID     string             `json:"assist_id"`
	CaseID       string             `json:"case_id"`
	Kind         AssistKind         `json:"kind"`
	TemplateID   string             `json:"template_id"`
	Release      int                `json:"release"`
	Environment  engine.Environment `json:"environment"`
	RunID        string             `json:"run_id"`
	Suggestion   json.RawMessage    `json:"suggestion,omitempty"`
	Citations    []Citation         `json:"citations,omitempty"`
	Unsupported  []string           `json:"unsupported,omitempty"`
	Confidence   float64            `json:"confidence"`
	Limitations  []string           `json:"limitations,omitempty"`
	EvidenceSeq  uint64             `json:"evidence_seq"`
	InvocationID string             `json:"invocation_id"`
	Provider     string             `json:"provider"`
	Model        string             `json:"model"`
	Attempts     int                `json:"attempts"`
	LatencyMS    int64              `json:"latency_ms"`
	PromptTokens int                `json:"prompt_tokens"`
	OutputTokens int                `json:"output_tokens"`
	CostUSD      float64            `json:"cost_usd"`
	ToolCalls    []ToolExecution    `json:"tool_calls,omitempty"`
}

func (r AssistResult) Validate() error {
	if err := r.validateMetadata(); err != nil {
		return err
	}
	if err := validateJSONObject("assist suggestion", r.Suggestion); err != nil {
		return err
	}
	for _, citation := range r.Citations {
		if err := citation.Validate(); err != nil {
			return err
		}
	}
	if err := rejectJSONPII("assist suggestion", r.Suggestion); err != nil {
		return err
	}
	if err := rejectTextPII(
		"assist result text",
		append(append([]string{}, r.Unsupported...), r.Limitations...)...,
	); err != nil {
		return err
	}
	return nil
}

func (r AssistResult) validateMetadata() error {
	if strings.TrimSpace(r.AssistID) == "" || strings.TrimSpace(r.CaseID) == "" ||
		!r.Kind.Valid() || strings.TrimSpace(r.TemplateID) == "" || r.Release < 1 ||
		!engine.ValidEnvironment(string(r.Environment)) || strings.TrimSpace(r.RunID) == "" ||
		strings.TrimSpace(r.InvocationID) == "" || strings.TrimSpace(r.Provider) == "" ||
		strings.TrimSpace(r.Model) == "" || r.Attempts < 1 {
		return errors.New("agent governance: assist lineage is incomplete")
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		return errors.New("agent governance: assist confidence must be within [0,1]")
	}
	if r.LatencyMS < 0 || r.PromptTokens < 0 || r.OutputTokens < 0 || r.CostUSD < 0 ||
		math.IsNaN(r.CostUSD) || math.IsInf(r.CostUSD, 0) {
		return errors.New("agent governance: assist usage and cost must be finite and non-negative")
	}
	for _, execution := range r.ToolCalls {
		if err := execution.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type AssistContent struct {
	Suggestion  json.RawMessage `json:"suggestion"`
	Citations   []Citation      `json:"citations"`
	Unsupported []string        `json:"unsupported,omitempty"`
	Limitations []string        `json:"limitations,omitempty"`
}

func (r AssistResult) content() AssistContent {
	return AssistContent{
		Suggestion: r.Suggestion, Citations: r.Citations,
		Unsupported: r.Unsupported, Limitations: r.Limitations,
	}
}

func (r *AssistResult) clearContent() {
	r.Suggestion, r.Citations, r.Unsupported, r.Limitations = nil, nil, nil, nil
}

func (r *AssistResult) restoreContent(content AssistContent) {
	r.Suggestion, r.Citations = content.Suggestion, content.Citations
	r.Unsupported, r.Limitations = content.Unsupported, content.Limitations
}

// ReviewerAction captures adoption/override evidence without performing the
// terminal case action on the reviewer's behalf.
type ReviewerAction struct {
	AssistID        string          `json:"assist_id"`
	Action          AssistAction    `json:"action"`
	Final           json.RawMessage `json:"final,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	TimeSavedMS     int64           `json:"time_saved_ms,omitempty"`
	EvidenceHeadSeq uint64          `json:"evidence_head_seq"`
	EvidenceStale   bool            `json:"evidence_stale,omitempty"`
}

func (a ReviewerAction) Validate() error {
	if strings.TrimSpace(a.AssistID) == "" || !a.Action.Valid() {
		return errors.New("agent governance: assist_id and reviewer action are required")
	}
	if a.Action == AssistEdited {
		if err := validateJSONObject("edited assist result", a.Final); err != nil {
			return err
		}
	} else if len(a.Final) > 0 {
		return errors.New("agent governance: only an edited assist may include a final result")
	}
	if err := rejectJSONPII("reviewer-edited assist", a.Final); err != nil {
		return err
	}
	if err := rejectTextPII("reviewer feedback", a.Reason); err != nil {
		return err
	}
	if (a.Action == AssistRejected || a.Action == AssistEscalated) && strings.TrimSpace(a.Reason) == "" {
		return errors.New("agent governance: rejection or escalation requires a reason")
	}
	if a.TimeSavedMS < 0 {
		return errors.New("agent governance: time_saved_ms cannot be negative")
	}
	return nil
}

type SuggestionDifferenceKind string

const (
	SuggestionAdded   SuggestionDifferenceKind = "added"
	SuggestionRemoved SuggestionDifferenceKind = "removed"
	SuggestionChanged SuggestionDifferenceKind = "changed"
)

type SuggestionDifference struct {
	Path string                   `json:"path"`
	Kind SuggestionDifferenceKind `json:"kind"`
}

func (difference SuggestionDifference) Validate() error {
	if !strings.HasPrefix(difference.Path, "/") {
		return errors.New("agent governance: suggestion difference path must be a JSON pointer")
	}
	switch difference.Kind {
	case SuggestionAdded, SuggestionRemoved, SuggestionChanged:
		return nil
	default:
		return errors.New("agent governance: suggestion difference kind is invalid")
	}
}

func validateJSONObject(label string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("agent governance: %s is required", label)
	}
	if len(raw) > MaxSchemaBytes {
		return fmt.Errorf("agent governance: %s exceeds %d bytes", label, MaxSchemaBytes)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("agent governance: %s must be a JSON object: %w", label, err)
	}
	return nil
}

func uniqueNonBlank(label string, values []string, limit int) error {
	if len(values) > limit {
		return fmt.Errorf("agent governance: too many %ss (%d > %d)", label, len(values), limit)
	}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("agent governance: %s cannot be blank", label)
		}
		if seen[value] {
			return fmt.Errorf("agent governance: duplicate %s %q", label, value)
		}
		seen[value] = true
	}
	return nil
}

func validSlug(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for i, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' && i > 0 {
			continue
		}
		return false
	}
	return !strings.HasSuffix(value, "-")
}

func validEnvName(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if r == '_' || r >= 'A' && r <= 'Z' || index > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// CanonicalPins returns a stable copy used for release hashes and comparisons.
func CanonicalPins(pins []DependencyPin) []DependencyPin {
	out := append([]DependencyPin(nil), pins...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out
}
