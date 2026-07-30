// SPDX-License-Identifier: AGPL-3.0-or-later

// Package client is a typed Go SDK for the intraktible public data-plane API —
// the contract published at /openapi.json. It wraps the decide hot path,
// decision history, and flow management over net/http with no third-party
// dependencies, so a Go service can call a decision flow in a few lines.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls an intraktible instance. Construct it with New; it is safe for
// concurrent use.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient sets the underlying HTTP client (for timeouts, transports, or
// test servers). The default is http.DefaultClient.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// New builds a Client for the given base URL (e.g. "https://decide.acme.com")
// authenticating with apiKey via the X-Api-Key header.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: http.DefaultClient}
	for _, o := range opts {
		o(c)
	}
	return c
}

// APIError is a non-2xx response, carrying the server's status and message.
type APIError struct {
	Status  int
	Message string
	// Body retains the bounded server error document so typed callers can inspect
	// structured conflict material (for example, the authoritative draft on 409).
	Body json.RawMessage
}

func (e *APIError) Error() string {
	return fmt.Sprintf("intraktible: http %d: %s", e.Status, e.Message)
}

// DecideRequest is the input to a decision: the data to decide on, plus an
// optional Context Layer entity whose features are injected.
type DecideRequest struct {
	Data              map[string]any    `json:"data"`
	EntityType        string            `json:"entity_type,omitempty"`
	EntityID          string            `json:"entity_id,omitempty"`
	BusinessReference string            `json:"business_reference,omitempty"`
	CorrelationID     string            `json:"correlation_id,omitempty"`
	Metadata          map[string]any    `json:"metadata,omitempty"`
	Control           *ExecutionControl `json:"control,omitempty"`
	// IdempotencyKey is sent in the Idempotency-Key header and never serialized.
	IdempotencyKey string `json:"-"`
}

// ExecutionControl bounds one decision invocation.
type ExecutionControl struct {
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

// DecideResult is a recorded decision. A flow whose logic errors completes with
// Status "failed" and a populated Error (it is not a transport error).
type DecideResult struct {
	DecisionID       string         `json:"decision_id"`
	Status           string         `json:"status"`
	Data             map[string]any `json:"data,omitempty"`
	Disposition      string         `json:"disposition,omitempty"`
	Error            string         `json:"error,omitempty"`
	ExperimentID     string         `json:"experiment_id,omitempty"`
	ExperimentCohort int            `json:"experiment_cohort,omitempty"`
	ExperimentArm    string         `json:"experiment_arm,omitempty"`
}

// BatchResult is the outcome of a batch decide: a summary plus per-row results.
type BatchResult struct {
	Summary map[string]any   `json:"summary"`
	Results []map[string]any `json:"results"`
}

// Decision is a row of recorded decision history.
type Decision struct {
	DecisionID        string         `json:"decision_id"`
	Slug              string         `json:"slug"`
	Version           int            `json:"version"`
	Environment       string         `json:"environment"`
	Status            string         `json:"status"`
	Disposition       string         `json:"disposition,omitempty"`
	EntityType        string         `json:"entity_type,omitempty"`
	EntityID          string         `json:"entity_id,omitempty"`
	BusinessReference string         `json:"business_reference,omitempty"`
	CorrelationID     string         `json:"correlation_id,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	ExperimentID      string         `json:"experiment_id,omitempty"`
	ExperimentCohort  int            `json:"experiment_cohort,omitempty"`
	ExperimentArm     string         `json:"experiment_arm,omitempty"`
	ExperimentArmName string         `json:"experiment_arm_name,omitempty"`
}

// Flow is a flow summary.
type Flow struct {
	FlowID string `json:"flow_id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Latest int    `json:"latest"`
}

// FlowDoc is a flow-as-code document (the shape /openapi.json's FlowImport, and
// what the export endpoint produces).
type FlowDoc struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name,omitempty"`
	Graph       json.RawMessage `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ImportResult reports a flow-as-code import.
type ImportResult struct {
	FlowID    string `json:"flow_id"`
	Slug      string `json:"slug"`
	Version   int    `json:"version"`
	Created   bool   `json:"created"`
	Published bool   `json:"published"`
}

// Identity is the authenticated caller.
type Identity struct {
	Org       string `json:"org"`
	Workspace string `json:"workspace"`
	Actor     string `json:"actor"`
	Scope     string `json:"scope"`
	Role      string `json:"role"`
}

// PromoteResult reports a promotion: Promoted when it deployed directly, or
// Pending with a RequestID when it opened a maker-checker request (production).
type PromoteResult struct {
	Promoted  bool   `json:"promoted"`
	Pending   bool   `json:"pending"`
	RequestID string `json:"request_id,omitempty"`
	Version   int    `json:"version"`
}

// BundleResult is the outcome of a bundle import: a per-flow result plus rollups.
type BundleResult struct {
	Results []struct {
		Slug      string `json:"slug"`
		FlowID    string `json:"flow_id,omitempty"`
		Version   int    `json:"version,omitempty"`
		Created   bool   `json:"created"`
		Published bool   `json:"published"`
		Error     string `json:"error,omitempty"`
	} `json:"results"`
	Published int `json:"published"`
	Failed    int `json:"failed"`
	Unchanged int `json:"unchanged"`
}

// ExperimentAction is a lifecycle transition accepted by TransitionExperiment.
type ExperimentAction string

const (
	// ExperimentStart begins a non-production experiment or requests production review.
	ExperimentStart ExperimentAction = "start"
	// ExperimentPause stops new treatment assignment without losing the cohort.
	ExperimentPause ExperimentAction = "pause"
	// ExperimentResume resumes treatment assignment for a paused cohort.
	ExperimentResume ExperimentAction = "resume"
	// ExperimentComplete closes observation for the current cohort.
	ExperimentComplete ExperimentAction = "complete"
	// ExperimentCancel permanently cancels the experiment.
	ExperimentCancel ExperimentAction = "cancel"
)

// Valid reports whether action is accepted by the experiment lifecycle endpoint.
func (action ExperimentAction) Valid() bool {
	switch action {
	case ExperimentStart, ExperimentPause, ExperimentResume, ExperimentComplete, ExperimentCancel:
		return true
	default:
		return false
	}
}

// LaunchDecision is an independent checker decision on a production launch.
type LaunchDecision string

const (
	// LaunchApprove starts the reviewed production experiment.
	LaunchApprove LaunchDecision = "approve"
	// LaunchReject returns the experiment to draft.
	LaunchReject LaunchDecision = "reject"
)

// Valid reports whether decision is an accepted launch-review action.
func (decision LaunchDecision) Valid() bool {
	return decision == LaunchApprove || decision == LaunchReject
}

// PopulationAction is a lifecycle transition accepted by TransitionPopulationJob.
type PopulationAction string

const (
	// PopulationPause stops new item claims while preserving progress.
	PopulationPause PopulationAction = "pause"
	// PopulationResume resumes claims for a paused job.
	PopulationResume PopulationAction = "resume"
	// PopulationCancel requests cancellation and fences further claims.
	PopulationCancel PopulationAction = "cancel"
)

// Valid reports whether action is accepted by the population lifecycle endpoint.
func (action PopulationAction) Valid() bool {
	return action == PopulationPause || action == PopulationResume || action == PopulationCancel
}

// ExperimentArm pins one allocation arm to an immutable flow version.
type ExperimentArm struct {
	Key           string `json:"key"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Version       int    `json:"version"`
	AllocationBPS int    `json:"allocation_bps"`
}

// ExperimentMetric defines a primary KPI or guardrail backed by business outcomes.
type ExperimentMetric struct {
	Key           string  `json:"key"`
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	Direction     string  `json:"direction"`
	MaxRegression float64 `json:"max_regression,omitempty"`
}

// ExperimentSpec is the complete immutable configuration for one experiment cohort.
type ExperimentSpec struct {
	Name                  string             `json:"name"`
	Hypothesis            string             `json:"hypothesis"`
	Owner                 string             `json:"owner"`
	FlowID                string             `json:"flow_id"`
	Environment           string             `json:"environment"`
	SubjectKeyExpression  string             `json:"subject_key_expression"`
	EligibilityExpression string             `json:"eligibility_expression,omitempty"`
	Salt                  string             `json:"salt"`
	Arms                  []ExperimentArm    `json:"arms"`
	PrimaryMetric         ExperimentMetric   `json:"primary_metric"`
	Guardrails            []ExperimentMetric `json:"guardrails,omitempty"`
	MinimumSamplePerArm   int                `json:"minimum_sample_per_arm"`
	MinimumEffect         float64            `json:"minimum_effect"`
	Confidence            float64            `json:"confidence"`
	ObservationWindowDays int                `json:"observation_window_days"`
	StartAt               *time.Time         `json:"start_at,omitempty"`
	StopAt                *time.Time         `json:"stop_at,omitempty"`
}

// Experiment is the current governed experiment projection.
type Experiment struct {
	ExperimentID string          `json:"experiment_id"`
	Cohort       int             `json:"cohort"`
	State        string          `json:"state"`
	Spec         ExperimentSpec  `json:"spec"`
	Launch       json.RawMessage `json:"launch,omitempty"`
}

// ExperimentInterval is a confidence interval.
type ExperimentInterval struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

// ExperimentArmMetric summarizes observations for one arm.
type ExperimentArmMetric struct {
	ArmKey   string             `json:"arm_key"`
	Count    int                `json:"count"`
	Mean     float64            `json:"mean"`
	StdDev   float64            `json:"std_dev"`
	Interval ExperimentInterval `json:"interval"`
}

// ExperimentComparison describes a treatment effect relative to the champion.
type ExperimentComparison struct {
	ArmKey     string             `json:"arm_key"`
	Effect     float64            `json:"effect"`
	EffectSize float64            `json:"effect_size"`
	Interval   ExperimentInterval `json:"interval"`
	Favorable  bool               `json:"favorable"`
}

// ExperimentMetricAnalysis is the exact-cohort analysis for one metric.
type ExperimentMetricAnalysis struct {
	Metric       ExperimentMetric       `json:"metric"`
	Arms         []ExperimentArmMetric  `json:"arms"`
	Comparisons  []ExperimentComparison `json:"comparisons"`
	Excluded     int                    `json:"excluded"`
	LabelVersion string                 `json:"label_version,omitempty"`
}

// ExperimentAnalysis is the reproducible, conservative winner report.
type ExperimentAnalysis struct {
	ExperimentID   string                     `json:"experiment_id"`
	Cohort         int                        `json:"cohort"`
	State          string                     `json:"state"`
	Status         string                     `json:"status"`
	Reason         string                     `json:"reason"`
	WinnerArmKey   string                     `json:"winner_arm_key,omitempty"`
	ExposureCounts map[string]int             `json:"exposure_counts"`
	SRMPValue      float64                    `json:"srm_p_value"`
	Primary        ExperimentMetricAnalysis   `json:"primary"`
	Guardrails     []ExperimentMetricAnalysis `json:"guardrails"`
	Assumptions    []string                   `json:"assumptions"`
}

// OutcomeSource identifies the upstream business record and its lineage.
type OutcomeSource struct {
	System   string `json:"system"`
	RecordID string `json:"record_id"`
	Lineage  string `json:"lineage,omitempty"`
}

// OutcomeRecord is a new immutable observed fact for one completed decision.
type OutcomeRecord struct {
	DecisionID            string        `json:"decision_id"`
	Key                   string        `json:"key"`
	Kind                  string        `json:"kind"`
	Value                 float64       `json:"value"`
	EventTime             time.Time     `json:"event_time"`
	ObservationWindowDays int           `json:"observation_window_days,omitempty"`
	Source                OutcomeSource `json:"source"`
	LabelVersion          string        `json:"label_version"`
}

// OutcomeCorrection appends a reasoned revision without overwriting history.
type OutcomeCorrection struct {
	Value                 float64       `json:"value"`
	EventTime             time.Time     `json:"event_time"`
	ObservationWindowDays int           `json:"observation_window_days,omitempty"`
	Source                OutcomeSource `json:"source"`
	LabelVersion          string        `json:"label_version"`
	Reason                string        `json:"reason"`
}

// BusinessOutcome is the current fact plus its complete correction and decision lineage.
type BusinessOutcome struct {
	OutcomeID   string            `json:"outcome_id"`
	DecisionID  string            `json:"decision_id"`
	Key         string            `json:"key"`
	Kind        string            `json:"kind"`
	FlowID      string            `json:"flow_id"`
	FlowVersion int               `json:"flow_version"`
	Environment string            `json:"environment"`
	Treatment   json.RawMessage   `json:"treatment,omitempty"`
	Predictions []json.RawMessage `json:"predictions,omitempty"`
	Current     json.RawMessage   `json:"current"`
	History     []json.RawMessage `json:"history"`
}

// PopulationItem is one immutable input row in a durable population job.
type PopulationItem struct {
	Data              map[string]any `json:"data"`
	EntityType        string         `json:"entity_type,omitempty"`
	EntityID          string         `json:"entity_id,omitempty"`
	BusinessReference string         `json:"business_reference,omitempty"`
	CorrelationID     string         `json:"correlation_id,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// PopulationJobCreate configures a version-pinned decision or record-free backtest.
type PopulationJobCreate struct {
	Kind          string           `json:"kind"`
	Slug          string           `json:"slug"`
	Environment   string           `json:"environment"`
	Items         []PopulationItem `json:"items"`
	MaxAttempts   int              `json:"max_attempts,omitempty"`
	Concurrency   int              `json:"concurrency,omitempty"`
	RetentionDays int              `json:"retention_days,omitempty"`
}

// PopulationJob is the current durable manifest, progress, and per-item result view.
type PopulationJob struct {
	JobID        string            `json:"job_id"`
	State        string            `json:"state"`
	Manifest     json.RawMessage   `json:"manifest"`
	ManifestHash string            `json:"manifest_hash"`
	Total        int               `json:"total"`
	Pending      int               `json:"pending"`
	Running      int               `json:"running"`
	Succeeded    int               `json:"succeeded"`
	Failed       int               `json:"failed"`
	Items        []json.RawMessage `json:"items"`
}

// Decide runs the live version of slug in env against req and returns the
// recorded decision.
func (c *Client) Decide(ctx context.Context, slug, env string, req DecideRequest) (DecideResult, error) {
	headers := map[string]string{}
	if req.IdempotencyKey != "" {
		headers["Idempotency-Key"] = req.IdempotencyKey
	}
	return doWithHeaders[DecideResult](
		ctx, c, http.MethodPost, decidePath(slug, env, "decide"), req, headers,
	)
}

// DecideBatch runs each row of dataset through the recorded decide path.
func (c *Client) DecideBatch(ctx context.Context, slug, env string, dataset []map[string]any) (BatchResult, error) {
	return do[BatchResult](ctx, c, http.MethodPost, decidePath(slug, env, "decide/batch"),
		map[string]any{"dataset": dataset})
}

// ListDecisions returns recorded decisions, newest first.
func (c *Client) ListDecisions(ctx context.Context) ([]Decision, error) {
	out, err := do[struct {
		Decisions []Decision `json:"decisions"`
	}](ctx, c, http.MethodGet, "/v1/decisions", nil)
	return out.Decisions, err
}

// GetDecision reads one decision by id.
func (c *Client) GetDecision(ctx context.Context, decisionID string) (Decision, error) {
	return do[Decision](ctx, c, http.MethodGet, "/v1/decisions/"+url.PathEscape(decisionID), nil)
}

// ListFlows returns the caller's flows.
func (c *Client) ListFlows(ctx context.Context) ([]Flow, error) {
	out, err := do[struct {
		Flows []Flow `json:"flows"`
	}](ctx, c, http.MethodGet, "/v1/flows", nil)
	return out.Flows, err
}

// CreateFlow creates an empty flow and returns its id.
func (c *Client) CreateFlow(ctx context.Context, slug, name string) (string, error) {
	out, err := do[struct {
		FlowID string `json:"flow_id"`
	}](ctx, c, http.MethodPost, "/v1/flows", map[string]string{"slug": slug, "name": name})
	return out.FlowID, err
}

// GetFlow reads one flow by id.
func (c *Client) GetFlow(ctx context.Context, flowID string) (Flow, error) {
	return do[Flow](ctx, c, http.MethodGet, "/v1/flows/"+url.PathEscape(flowID), nil)
}

// ImportBundle calls the retired direct-publication bundle route and returns
// HTTP 410.
// Deprecated: use ImportCanonicalBundle with a stable idempotency key.
func (c *Client) ImportBundle(ctx context.Context, docs []FlowDoc) (BundleResult, error) {
	return do[BundleResult](ctx, c, http.MethodPost, "/v1/flows/import-bundle",
		map[string]any{"flows": docs})
}

// Deploy makes version live in env for the flow (a direct deploy; use Promote to
// move a live version up the environment chain).
func (c *Client) Deploy(ctx context.Context, flowID, env string, version int) error {
	_, err := do[map[string]any](ctx, c, http.MethodPost, "/v1/flows/"+url.PathEscape(flowID)+"/deployments",
		map[string]any{"environment": env, "version": version})
	return err
}

// Promote ships the live version of `from` up to `to`. A non-production target
// deploys directly; production opens a maker-checker request (Pending). force
// overrides the promotion gate where the stage allows it.
func (c *Client) Promote(ctx context.Context, flowID, from, to string, force bool) (PromoteResult, error) {
	return do[PromoteResult](ctx, c, http.MethodPost, "/v1/flows/"+url.PathEscape(flowID)+"/promote",
		map[string]any{"from": from, "to": to, "force": force})
}

// ListExperiments returns the tenant's governed experiment cohorts.
func (c *Client) ListExperiments(ctx context.Context) ([]Experiment, error) {
	out, err := do[struct {
		Experiments []Experiment `json:"experiments"`
	}](ctx, c, http.MethodGet, "/v1/experiments", nil)
	return out.Experiments, err
}

// GetExperiment reads one experiment by id.
func (c *Client) GetExperiment(ctx context.Context, experimentID string) (Experiment, error) {
	return do[Experiment](ctx, c, http.MethodGet, "/v1/experiments/"+url.PathEscape(experimentID), nil)
}

// CreateExperiment creates a governed experiment draft and returns its id.
func (c *Client) CreateExperiment(ctx context.Context, spec ExperimentSpec) (string, error) {
	out, err := do[struct {
		ExperimentID string `json:"experiment_id"`
	}](ctx, c, http.MethodPost, "/v1/experiments", spec)
	return out.ExperimentID, err
}

// UpdateExperiment replaces a draft specification and advances its immutable cohort.
func (c *Client) UpdateExperiment(ctx context.Context, experimentID string, spec ExperimentSpec) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPut, "/v1/experiments/"+url.PathEscape(experimentID), spec,
	)
	return err
}

// TransitionExperiment applies a typed start, pause, resume, complete, or cancel action.
func (c *Client) TransitionExperiment(
	ctx context.Context,
	experimentID string,
	action ExperimentAction,
	reason string,
) error {
	if !action.Valid() {
		return fmt.Errorf("intraktible: invalid experiment action %q", action)
	}
	body := any(nil)
	if action != "start" {
		body = map[string]string{"reason": reason}
	}
	_, err := do[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/experiments/"+url.PathEscape(experimentID)+"/"+url.PathEscape(string(action)), body,
	)
	return err
}

// DecideExperimentLaunch approves or rejects a pending production launch as checker.
func (c *Client) DecideExperimentLaunch(
	ctx context.Context,
	experimentID, requestID string,
	decision LaunchDecision,
	reason string,
) error {
	if !decision.Valid() {
		return fmt.Errorf("intraktible: invalid experiment launch decision %q", decision)
	}
	_, err := do[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/experiments/"+url.PathEscape(experimentID)+"/launch-requests/"+
			url.PathEscape(requestID)+"/"+url.PathEscape(string(decision)),
		map[string]string{"reason": reason},
	)
	return err
}

// ExperimentAnalysis returns the current exact-cohort statistical report.
func (c *Client) ExperimentAnalysis(ctx context.Context, experimentID string) (ExperimentAnalysis, error) {
	return do[ExperimentAnalysis](
		ctx, c, http.MethodGet,
		"/v1/experiments/"+url.PathEscape(experimentID)+"/analysis", nil,
	)
}

// PromoteExperimentWinner opens the governed deployment for a completed valid winner.
func (c *Client) PromoteExperimentWinner(ctx context.Context, experimentID string) (PromoteResult, error) {
	return do[PromoteResult](
		ctx, c, http.MethodPost,
		"/v1/experiments/"+url.PathEscape(experimentID)+"/promote", nil,
	)
}

// ListOutcomes returns current decision-linked facts, optionally filtered by decision and key.
func (c *Client) ListOutcomes(ctx context.Context, decisionID, key string) ([]BusinessOutcome, error) {
	query := url.Values{}
	if decisionID != "" {
		query.Set("decision_id", decisionID)
	}
	if key != "" {
		query.Set("key", key)
	}
	path := "/v1/outcomes"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	out, err := do[struct {
		Outcomes []BusinessOutcome `json:"outcomes"`
	}](ctx, c, http.MethodGet, path, nil)
	return out.Outcomes, err
}

// RecordOutcome records one externally observed business fact idempotently.
func (c *Client) RecordOutcome(
	ctx context.Context,
	outcome OutcomeRecord,
	idempotencyKey string,
) (string, error) {
	out, err := doWithHeaders[struct {
		OutcomeID string `json:"outcome_id"`
	}](ctx, c, http.MethodPost, "/v1/outcomes", outcome,
		map[string]string{"Idempotency-Key": idempotencyKey})
	return out.OutcomeID, err
}

// CorrectOutcome appends an auditable revision to an existing outcome.
func (c *Client) CorrectOutcome(
	ctx context.Context,
	outcomeID string,
	correction OutcomeCorrection,
	idempotencyKey string,
) error {
	_, err := doWithHeaders[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/outcomes/"+url.PathEscape(outcomeID)+"/corrections", correction,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	return err
}

// ListPopulationJobs returns durable population-job progress summaries.
func (c *Client) ListPopulationJobs(ctx context.Context) ([]PopulationJob, error) {
	out, err := do[struct {
		Jobs []PopulationJob `json:"jobs"`
	}](ctx, c, http.MethodGet, "/v1/population-jobs", nil)
	return out.Jobs, err
}

// GetPopulationJob reads a job's immutable manifest, progress, and item results.
func (c *Client) GetPopulationJob(ctx context.Context, jobID string) (PopulationJob, error) {
	return do[PopulationJob](ctx, c, http.MethodGet, "/v1/population-jobs/"+url.PathEscape(jobID), nil)
}

// CreatePopulationJob resolves and persists a version-pinned population manifest.
func (c *Client) CreatePopulationJob(
	ctx context.Context,
	job PopulationJobCreate,
	idempotencyKey string,
) (string, error) {
	out, err := doWithHeaders[struct {
		JobID string `json:"job_id"`
	}](ctx, c, http.MethodPost, "/v1/population-jobs", job,
		map[string]string{"Idempotency-Key": idempotencyKey})
	return out.JobID, err
}

// TransitionPopulationJob applies a typed pause, resume, or cancel action.
func (c *Client) TransitionPopulationJob(
	ctx context.Context,
	jobID string,
	action PopulationAction,
	reason string,
) error {
	if !action.Valid() {
		return fmt.Errorf("intraktible: invalid population action %q", action)
	}
	_, err := do[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/population-jobs/"+url.PathEscape(jobID)+"/"+url.PathEscape(string(action)),
		map[string]string{"reason": reason},
	)
	return err
}

// RetryPopulationJob requeues selected failed items, or every retryable failure when indices is empty.
func (c *Client) RetryPopulationJob(ctx context.Context, jobID string, indices []int) error {
	_, err := do[map[string]any](
		ctx, c, http.MethodPost,
		"/v1/population-jobs/"+url.PathEscape(jobID)+"/retry",
		map[string]any{"indices": indices},
	)
	return err
}

// PopulationResults downloads the immutable terminal NDJSON result manifest.
func (c *Client) PopulationResults(ctx context.Context, jobID string) ([]byte, error) {
	return doBytes(ctx, c, "/v1/population-jobs/"+url.PathEscape(jobID)+"/results")
}

// Me returns the authenticated caller's identity, scope, and role.
func (c *Client) Me(ctx context.Context) (Identity, error) {
	return do[Identity](ctx, c, http.MethodGet, "/v1/me", nil)
}

func doBytes(ctx context.Context, c *Client, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(data))
		var payload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Error != "" {
			message = payload.Error
		}
		return nil, &APIError{Status: resp.StatusCode, Message: message, Body: data}
	}
	return data, nil
}

func decidePath(slug, env, tail string) string {
	return "/v1/flows/" + url.PathEscape(slug) + "/" + url.PathEscape(env) + "/" + tail
}

// do issues one request: it marshals body (when non-nil), sets auth, and decodes
// a 2xx JSON body into T. A non-2xx response becomes an *APIError carrying the
// server's {error} message when present.
func do[T any](ctx context.Context, c *Client, method, path string, body any) (T, error) {
	return doWithHeaders[T](ctx, c, method, path, body, nil)
}

func doWithHeaders[T any](
	ctx context.Context,
	c *Client,
	method, path string,
	body any,
	headers map[string]string,
) (T, error) {
	var zero T
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return zero, fmt.Errorf("intraktible: marshal request: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return zero, err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return zero, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return zero, &APIError{Status: resp.StatusCode, Message: msg, Body: data}
	}
	var out T
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return zero, fmt.Errorf("intraktible: decode response: %w", err)
		}
	}
	return out, nil
}
