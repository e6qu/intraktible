// SPDX-License-Identifier: AGPL-3.0-or-later

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	contextfeatures "github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
)

// GovernedFeature joins a feature definition to source, materialization, cost,
// and downstream lineage evidence.
type GovernedFeature struct {
	Definition          contextfeatures.FeatureView       `json:"definition"`
	SourceRef           domain.SchemaRef                  `json:"source_ref"`
	SourceSchemaVersion int                               `json:"source_schema_version,omitempty"`
	SourceSchemaHash    string                            `json:"source_schema_hash,omitempty"`
	FreshnessSeconds    int64                             `json:"freshness_seconds,omitempty"`
	SourceHealth        *modelprojection.SourceHealthView `json:"source_health,omitempty"`
	Materialization     string                            `json:"materialization_status"`
	LastSuccess         *domain.BackfillManifest          `json:"last_success,omitempty"`
	LastError           string                            `json:"last_error,omitempty"`
	LastJobID           string                            `json:"last_job_id,omitempty"`
	LastJobUpdatedAt    *time.Time                        `json:"last_job_updated_at,omitempty"`
	Cardinality         int                               `json:"cardinality"`
	StorageBytes        int64                             `json:"storage_bytes"`
	ComputeUnits        int64                             `json:"compute_units"`
	EstimatedCostUSD    float64                           `json:"estimated_cost_usd"`
	DownstreamConsumers []string                          `json:"downstream_consumers"`
}

// Modeling wire types are aliases of the platform's public read models.
type (
	SchemaView          = modelprojection.SchemaView
	QualityObservation  = modelprojection.QualityObservation
	QualityIncident     = modelprojection.QualityIncident
	SourceHealth        = modelprojection.SourceHealthView
	DatasetView         = modelprojection.DatasetView
	ModelingJob         = modelprojection.JobView
	SnapshotView        = modelprojection.SnapshotView
	ArtifactView        = modelprojection.ArtifactView
	EvaluationView      = modelprojection.EvaluationView
	MaterializationView = modelprojection.MaterializationView
)

// CommandResult is the common accepted-event response.
type CommandResult struct {
	EventID string `json:"event_id"`
	Seq     uint64 `json:"seq"`
}

// JobAccepted is returned by asynchronous model-data operations.
type JobAccepted struct {
	CommandResult
	JobID        string `json:"job_id"`
	SnapshotID   string `json:"snapshot_id,omitempty"`
	ArtifactID   string `json:"artifact_id,omitempty"`
	EvaluationID string `json:"evaluation_id,omitempty"`
	BackfillID   string `json:"backfill_id,omitempty"`
}

// SnapshotRows is the admin-only verified row response.
type SnapshotRows struct {
	Manifest domain.SnapshotManifest `json:"manifest"`
	Rows     []domain.DatasetRow     `json:"rows"`
}

func (c *Client) DefineSchema(
	ctx context.Context,
	spec domain.SchemaSpec,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost, "/v1/modeling/schemas", spec)
}

func (c *Client) ListSchemas(ctx context.Context) ([]SchemaView, error) {
	out, err := do[struct {
		Schemas []SchemaView `json:"schemas"`
	}](ctx, c, http.MethodGet, "/v1/modeling/schemas", nil)
	return out.Schemas, err
}

func schemaBasePath(ref domain.SchemaRef) string {
	return "/v1/modeling/schemas/" + url.PathEscape(string(ref.Kind)) + "/" +
		url.PathEscape(ref.EntityType)
}

func schemaPath(ref domain.SchemaRef, suffix string) string {
	path := schemaBasePath(ref) + suffix
	if ref.EventName != "" {
		path += "?event_name=" + url.QueryEscape(ref.EventName)
	}
	return path
}

func (c *Client) GetSchema(
	ctx context.Context,
	ref domain.SchemaRef,
) (SchemaView, error) {
	return do[SchemaView](ctx, c, http.MethodGet, schemaPath(ref, ""), nil)
}

func (c *Client) RequestSchemaApproval(
	ctx context.Context,
	ref domain.SchemaRef,
	version int,
) (string, error) {
	out, err := do[struct {
		RequestID string `json:"request_id"`
	}](ctx, c, http.MethodPost, schemaPath(
		ref, "/versions/"+strconv.Itoa(version)+"/approval-request",
	), map[string]any{})
	return out.RequestID, err
}

func (c *Client) DecideSchemaApproval(
	ctx context.Context,
	requestID string,
	ref domain.SchemaRef,
	approve bool,
	reason string,
) (CommandResult, error) {
	return do[CommandResult](
		ctx, c, http.MethodPost,
		"/v1/modeling/schema-approval/"+url.PathEscape(requestID)+"/decision",
		map[string]any{"ref": ref, "approve": approve, "reason": reason},
	)
}

func (c *Client) RetireSchema(
	ctx context.Context,
	ref domain.SchemaRef,
	version int,
	reason string,
) (CommandResult, error) {
	return do[CommandResult](
		ctx, c, http.MethodPost,
		schemaPath(ref, "/versions/"+strconv.Itoa(version)+"/retire"),
		map[string]string{"reason": reason},
	)
}

func (c *Client) ListQualityObservations(
	ctx context.Context,
) ([]QualityObservation, error) {
	out, err := do[struct {
		Observations []QualityObservation `json:"observations"`
	}](ctx, c, http.MethodGet, "/v1/modeling/quality/observations", nil)
	return out.Observations, err
}

func (c *Client) ListQualityIncidents(ctx context.Context) ([]QualityIncident, error) {
	out, err := do[struct {
		Incidents []QualityIncident `json:"incidents"`
	}](ctx, c, http.MethodGet, "/v1/modeling/quality/incidents", nil)
	return out.Incidents, err
}

func (c *Client) AcknowledgeQualityIncident(
	ctx context.Context,
	incidentID string,
	note string,
) (CommandResult, error) {
	return do[CommandResult](
		ctx, c, http.MethodPost,
		"/v1/modeling/quality/incidents/"+url.PathEscape(incidentID)+"/acknowledge",
		map[string]string{"note": note},
	)
}

func (c *Client) ResolveQualityIncident(
	ctx context.Context,
	incidentID string,
	reason string,
) (CommandResult, error) {
	return do[CommandResult](
		ctx, c, http.MethodPost,
		"/v1/modeling/quality/incidents/"+url.PathEscape(incidentID)+"/resolve",
		map[string]string{"reason": reason},
	)
}

func (c *Client) ListSourceHealth(ctx context.Context) ([]SourceHealth, error) {
	out, err := do[struct {
		Sources []SourceHealth `json:"sources"`
	}](ctx, c, http.MethodGet, "/v1/modeling/source-health", nil)
	return out.Sources, err
}

func (c *Client) ListGovernedFeatures(ctx context.Context) ([]GovernedFeature, error) {
	out, err := do[struct {
		Features []GovernedFeature `json:"features"`
	}](ctx, c, http.MethodGet, "/v1/modeling/features", nil)
	return out.Features, err
}

func (c *Client) DefineDataset(
	ctx context.Context,
	spec domain.DatasetSpec,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost, "/v1/modeling/datasets", spec)
}

func (c *Client) ListDatasets(ctx context.Context) ([]DatasetView, error) {
	out, err := do[struct {
		Datasets []DatasetView `json:"datasets"`
	}](ctx, c, http.MethodGet, "/v1/modeling/datasets", nil)
	return out.Datasets, err
}

func (c *Client) GetDataset(ctx context.Context, name string) (DatasetView, error) {
	return do[DatasetView](
		ctx, c, http.MethodGet, "/v1/modeling/datasets/"+url.PathEscape(name), nil,
	)
}

func (c *Client) RequestSnapshot(
	ctx context.Context,
	name string,
	version int,
	request domain.SnapshotRequest,
) (JobAccepted, error) {
	return do[JobAccepted](
		ctx, c, http.MethodPost,
		"/v1/modeling/datasets/"+url.PathEscape(name)+"/versions/"+
			strconv.Itoa(version)+"/snapshots",
		map[string]any{
			"observation_at":  request.ObservationAt,
			"knowledge_at":    request.KnowledgeAt,
			"idempotency_key": request.IdempotencyKey,
		},
	)
}

func (c *Client) ListSnapshots(ctx context.Context) ([]SnapshotView, error) {
	out, err := do[struct {
		Snapshots []SnapshotView `json:"snapshots"`
	}](ctx, c, http.MethodGet, "/v1/modeling/snapshots", nil)
	return out.Snapshots, err
}

func (c *Client) GetSnapshot(ctx context.Context, snapshotID string) (SnapshotView, error) {
	return do[SnapshotView](
		ctx, c, http.MethodGet,
		"/v1/modeling/snapshots/"+url.PathEscape(snapshotID), nil,
	)
}

func (c *Client) SnapshotRows(
	ctx context.Context,
	snapshotID string,
) (SnapshotRows, error) {
	return do[SnapshotRows](
		ctx, c, http.MethodGet,
		"/v1/modeling/snapshots/"+url.PathEscape(snapshotID)+"/rows", nil,
	)
}

func (c *Client) ExportSnapshot(
	ctx context.Context,
	snapshotID string,
	format string,
) ([]byte, error) {
	query := url.Values{}
	query.Set("format", format)
	return doBytes(
		ctx, c,
		"/v1/modeling/snapshots/"+url.PathEscape(snapshotID)+"/export?"+query.Encode(),
	)
}

func (c *Client) ListModelingJobs(ctx context.Context) ([]ModelingJob, error) {
	out, err := do[struct {
		Jobs []ModelingJob `json:"jobs"`
	}](ctx, c, http.MethodGet, "/v1/modeling/jobs", nil)
	return out.Jobs, err
}

func (c *Client) GetModelingJob(ctx context.Context, jobID string) (ModelingJob, error) {
	return do[ModelingJob](
		ctx, c, http.MethodGet, "/v1/modeling/jobs/"+url.PathEscape(jobID), nil,
	)
}

func (c *Client) CancelModelingJob(
	ctx context.Context,
	jobID string,
	reason string,
) (CommandResult, error) {
	return do[CommandResult](
		ctx, c, http.MethodPost,
		"/v1/modeling/jobs/"+url.PathEscape(jobID)+"/cancel",
		map[string]string{"reason": reason},
	)
}

func (c *Client) PauseModelingJob(
	ctx context.Context,
	jobID string,
	reason string,
) (CommandResult, error) {
	return c.transitionModelingJob(ctx, jobID, "pause", reason)
}

func (c *Client) ResumeModelingJob(
	ctx context.Context,
	jobID string,
	reason string,
) (CommandResult, error) {
	return c.transitionModelingJob(ctx, jobID, "resume", reason)
}

func (c *Client) RetryModelingJob(
	ctx context.Context,
	jobID string,
	reason string,
) (CommandResult, error) {
	return c.transitionModelingJob(ctx, jobID, "retry", reason)
}

func (c *Client) transitionModelingJob(
	ctx context.Context,
	jobID string,
	transition string,
	reason string,
) (CommandResult, error) {
	return do[CommandResult](
		ctx, c, http.MethodPost,
		"/v1/modeling/jobs/"+url.PathEscape(jobID)+"/"+transition,
		map[string]string{"reason": reason},
	)
}

func (c *Client) RequestTraining(
	ctx context.Context,
	request domain.TrainingRequest,
) (JobAccepted, error) {
	return do[JobAccepted](
		ctx, c, http.MethodPost, "/v1/modeling/training-jobs", request,
	)
}

func (c *Client) RequestEvaluation(
	ctx context.Context,
	request domain.EvaluationRequest,
) (JobAccepted, error) {
	return do[JobAccepted](
		ctx, c, http.MethodPost, "/v1/modeling/evaluation-jobs", request,
	)
}

func (c *Client) ListArtifacts(ctx context.Context) ([]ArtifactView, error) {
	out, err := do[struct {
		Artifacts []ArtifactView `json:"artifacts"`
	}](ctx, c, http.MethodGet, "/v1/modeling/artifacts", nil)
	return out.Artifacts, err
}

func (c *Client) RegisterExternalArtifact(
	ctx context.Context,
	registration domain.ArtifactRegistration,
) (CommandResult, error) {
	return do[CommandResult](
		ctx, c, http.MethodPost, "/v1/modeling/artifacts", registration,
	)
}

func (c *Client) ChangeArtifactStage(
	ctx context.Context,
	artifactID string,
	stage domain.ArtifactStage,
	reason string,
) (CommandResult, error) {
	return do[CommandResult](
		ctx, c, http.MethodPost,
		"/v1/modeling/artifacts/"+url.PathEscape(artifactID)+"/stage",
		map[string]any{"stage": stage, "reason": reason},
	)
}

func (c *Client) GetArtifact(ctx context.Context, artifactID string) (ArtifactView, error) {
	return do[ArtifactView](
		ctx, c, http.MethodGet,
		"/v1/modeling/artifacts/"+url.PathEscape(artifactID), nil,
	)
}

func (c *Client) VerifyArtifact(ctx context.Context, artifactID string) error {
	_, err := do[map[string]bool](
		ctx, c, http.MethodGet,
		"/v1/modeling/artifacts/"+url.PathEscape(artifactID)+"/verify", nil,
	)
	return err
}

func (c *Client) ListEvaluations(ctx context.Context) ([]EvaluationView, error) {
	out, err := do[struct {
		Evaluations []EvaluationView `json:"evaluations"`
	}](ctx, c, http.MethodGet, "/v1/modeling/evaluations", nil)
	return out.Evaluations, err
}

func (c *Client) GetEvaluation(
	ctx context.Context,
	evaluationID string,
) (EvaluationView, error) {
	return do[EvaluationView](
		ctx, c, http.MethodGet,
		"/v1/modeling/evaluations/"+url.PathEscape(evaluationID), nil,
	)
}

func (c *Client) RequestBackfill(
	ctx context.Context,
	request domain.BackfillRequest,
) (JobAccepted, error) {
	return do[JobAccepted](
		ctx, c, http.MethodPost, "/v1/modeling/backfills", request,
	)
}

func (c *Client) ListMaterializations(
	ctx context.Context,
) ([]MaterializationView, error) {
	out, err := do[struct {
		Materializations []MaterializationView `json:"materializations"`
	}](ctx, c, http.MethodGet, "/v1/modeling/materializations", nil)
	return out.Materializations, err
}

func (c *Client) GetMaterialization(
	ctx context.Context,
	backfillID string,
) (MaterializationView, error) {
	return do[MaterializationView](
		ctx, c, http.MethodGet,
		"/v1/modeling/materializations/"+url.PathEscape(backfillID), nil,
	)
}

func (c *Client) ModelLineage(
	ctx context.Context,
	modelName string,
) (json.RawMessage, error) {
	return do[json.RawMessage](
		ctx, c, http.MethodGet,
		"/v1/modeling/lineage/models/"+url.PathEscape(modelName), nil,
	)
}

func (c *Client) CompareModels(
	ctx context.Context,
	champion string,
	challenger string,
) (json.RawMessage, error) {
	query := url.Values{"champion": {champion}, "challenger": {challenger}}
	return do[json.RawMessage](
		ctx, c, http.MethodGet, "/v1/modeling/comparisons?"+query.Encode(), nil,
	)
}
