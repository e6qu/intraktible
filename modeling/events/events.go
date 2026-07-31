// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events defines immutable modeling and data-governance event payloads.
package events

import (
	"time"

	"github.com/e6qu/intraktible/modeling/domain"
)

// StreamModeling is the tenant modeling and data-governance stream.
const StreamModeling = "modeling"

const (
	TypeSchemaVersionDefined        = "modeling.schema.version_defined"
	TypeSchemaApprovalRequested     = "modeling.schema.approval_requested"
	TypeSchemaApprovalApproved      = "modeling.schema.approval_approved"
	TypeSchemaApprovalRejected      = "modeling.schema.approval_rejected"
	TypeSchemaVersionRetired        = "modeling.schema.version_retired"
	TypeDatasetDefined              = "modeling.dataset.defined"
	TypeSnapshotRequested           = "modeling.snapshot.requested"
	TypeJobClaimed                  = "modeling.job.claimed"
	TypeJobHeartbeat                = "modeling.job.heartbeat"
	TypeJobFailed                   = "modeling.job.failed"
	TypeJobProgressed               = "modeling.job.progressed"
	TypeJobPauseRequested           = "modeling.job.pause_requested"
	TypeJobPaused                   = "modeling.job.paused"
	TypeJobResumed                  = "modeling.job.resumed"
	TypeJobRetryRequested           = "modeling.job.retry_requested"
	TypeJobCancelRequested          = "modeling.job.cancel_requested"
	TypeJobCancelled                = "modeling.job.cancelled"
	TypeSnapshotPublished           = "modeling.snapshot.published"
	TypeTrainingRequested           = "modeling.training.requested"
	TypeEvaluationRequested         = "modeling.evaluation.requested"
	TypeEvaluationPublished         = "modeling.evaluation.published"
	TypeBackfillRequested           = "modeling.backfill.requested"
	TypeBackfillPublished           = "modeling.backfill.published"
	TypeArtifactRegistered          = "modeling.artifact.registered"
	TypeArtifactStageChanged        = "modeling.artifact.stage_changed"
	TypeSnapshotExpired             = "modeling.snapshot.expired"
	TypeSourceFreshnessViolated     = "modeling.source.freshness_violated"
	TypeSourceFreshnessRecovered    = "modeling.source.freshness_recovered"
	TypeQualityIncidentAcknowledged = "modeling.quality.incident_acknowledged"
	TypeQualityIncidentResolved     = "modeling.quality.incident_resolved"
)

// SchemaVersionDefined creates one immutable schema version.
type SchemaVersionDefined struct {
	Ref                 domain.SchemaRef  `json:"ref"`
	Version             int               `json:"version"`
	Spec                domain.SchemaSpec `json:"spec"`
	Hash                string            `json:"hash"`
	CompatibilityBreaks []string          `json:"compatibility_breaks,omitempty"`
}

// SchemaApprovalRequested submits one version to an independent checker.
type SchemaApprovalRequested struct {
	RequestID string           `json:"request_id"`
	Ref       domain.SchemaRef `json:"ref"`
	Version   int              `json:"version"`
}

// SchemaApprovalApproved activates one independently checked version.
type SchemaApprovalApproved struct {
	RequestID string           `json:"request_id"`
	Ref       domain.SchemaRef `json:"ref"`
	Version   int              `json:"version"`
	Reason    string           `json:"reason,omitempty"`
}

// SchemaApprovalRejected closes a pending request without activation.
type SchemaApprovalRejected struct {
	RequestID string           `json:"request_id"`
	Ref       domain.SchemaRef `json:"ref"`
	Version   int              `json:"version"`
	Reason    string           `json:"reason,omitempty"`
}

// SchemaVersionRetired makes an approved schema unavailable for new writes.
type SchemaVersionRetired struct {
	Ref       domain.SchemaRef `json:"ref"`
	Version   int              `json:"version"`
	Reason    string           `json:"reason"`
	RetiredAt time.Time        `json:"retired_at"`
}

// DatasetDefined creates one immutable point-in-time dataset definition.
type DatasetDefined struct {
	Name    string             `json:"name"`
	Version int                `json:"version"`
	Spec    domain.DatasetSpec `json:"spec"`
	Hash    string             `json:"hash"`
}

// SnapshotRequested pins a dataset version and both temporal cutoffs.
type SnapshotRequested struct {
	JobID           string             `json:"job_id"`
	SnapshotID      string             `json:"snapshot_id"`
	DatasetName     string             `json:"dataset_name"`
	DatasetVersion  int                `json:"dataset_version"`
	DatasetHash     string             `json:"dataset_hash"`
	Spec            domain.DatasetSpec `json:"spec"`
	ObservationAt   time.Time          `json:"observation_at"`
	KnowledgeAt     time.Time          `json:"knowledge_at"`
	RequestHash     string             `json:"request_hash"`
	FeatureVersions map[string]int     `json:"feature_versions"`
	ExpiresAt       time.Time          `json:"expires_at"`
}

// JobClaimed leases a queued or expired modeling job.
type JobClaimed struct {
	JobID      string    `json:"job_id"`
	Attempt    int       `json:"attempt"`
	Worker     string    `json:"worker"`
	LeaseUntil time.Time `json:"lease_until"`
}

// JobHeartbeat extends the active fenced attempt.
type JobHeartbeat struct {
	JobID      string    `json:"job_id"`
	Attempt    int       `json:"attempt"`
	Worker     string    `json:"worker"`
	LeaseUntil time.Time `json:"lease_until"`
}

// JobFailed records one attempt failure.
type JobFailed struct {
	JobID     string `json:"job_id"`
	Attempt   int    `json:"attempt"`
	Error     string `json:"error"`
	Retryable bool   `json:"retryable"`
}

// JobProgressed records a fenced attempt's durable operational progress.
type JobProgressed struct {
	JobID            string  `json:"job_id"`
	Attempt          int     `json:"attempt"`
	Worker           string  `json:"worker"`
	CompletedUnits   int64   `json:"completed_units"`
	TotalUnits       int64   `json:"total_units"`
	Phase            string  `json:"phase"`
	ComputeUnits     int64   `json:"compute_units"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

// JobTransition requests or finishes a lifecycle transition.
type JobTransition struct {
	JobID  string `json:"job_id"`
	Reason string `json:"reason,omitempty"`
}

// SnapshotPublished atomically exposes a fully-written snapshot manifest and
// completes its active job attempt.
type SnapshotPublished struct {
	JobID    string                  `json:"job_id"`
	Attempt  int                     `json:"attempt"`
	Manifest domain.SnapshotManifest `json:"manifest"`
}

// SnapshotExpired makes retained row content unavailable without deleting its
// immutable lineage or audit manifest.
type SnapshotExpired struct {
	SnapshotID string    `json:"snapshot_id"`
	StorageRef string    `json:"storage_ref"`
	ExpiredAt  time.Time `json:"expired_at"`
	Reason     string    `json:"reason"`
}

// SourceFreshnessViolated opens one deduplicated source-liveness incident.
type SourceFreshnessViolated struct {
	IncidentID     string               `json:"incident_id"`
	Ref            domain.SchemaRef     `json:"ref"`
	SchemaVersion  int                  `json:"schema_version"`
	SchemaHash     string               `json:"schema_hash"`
	Action         domain.QualityAction `json:"action"`
	LastReceivedAt time.Time            `json:"last_received_at"`
	Deadline       time.Time            `json:"deadline"`
	DetectedAt     time.Time            `json:"detected_at"`
}

// SourceFreshnessRecovered closes a scheduler-owned freshness incident.
type SourceFreshnessRecovered struct {
	IncidentID  string    `json:"incident_id"`
	RecoveredAt time.Time `json:"recovered_at"`
}

// TrainingRequested pins a signed-artifact job to one published snapshot.
type TrainingRequested struct {
	JobID       string                  `json:"job_id"`
	ArtifactID  string                  `json:"artifact_id"`
	Request     domain.TrainingRequest  `json:"request"`
	Snapshot    domain.SnapshotManifest `json:"snapshot"`
	RequestHash string                  `json:"request_hash"`
}

// EvaluationRequested creates an independent durable evaluation job.
type EvaluationRequested struct {
	JobID        string                   `json:"job_id"`
	EvaluationID string                   `json:"evaluation_id"`
	Request      domain.EvaluationRequest `json:"request"`
	ArtifactID   string                   `json:"artifact_id"`
	ArtifactHash string                   `json:"artifact_hash"`
	ModelName    string                   `json:"model_name"`
	Snapshot     domain.SnapshotManifest  `json:"snapshot"`
	RequestHash  string                   `json:"request_hash"`
}

// EvaluationPublished atomically completes an independently run evaluation.
type EvaluationPublished struct {
	JobID    string                    `json:"job_id"`
	Attempt  int                       `json:"attempt"`
	Manifest domain.EvaluationManifest `json:"manifest"`
}

// BackfillRequested creates a durable point-in-time materialization job.
type BackfillRequested struct {
	JobID           string                 `json:"job_id"`
	BackfillID      string                 `json:"backfill_id"`
	Request         domain.BackfillRequest `json:"request"`
	FeatureVersions map[string]int         `json:"feature_versions"`
	RequestHash     string                 `json:"request_hash"`
}

// BackfillPublished atomically exposes a verified materialization.
type BackfillPublished struct {
	JobID    string                  `json:"job_id"`
	Attempt  int                     `json:"attempt"`
	Manifest domain.BackfillManifest `json:"manifest"`
}

// ArtifactRegistered records externally built signed metadata without loading
// or embedding artifact bytes.
type ArtifactRegistered struct {
	Registration domain.ArtifactRegistration `json:"registration"`
}

// ArtifactStageChanged records independently governed promotion or archive.
type ArtifactStageChanged struct {
	ArtifactID string               `json:"artifact_id"`
	From       domain.ArtifactStage `json:"from"`
	To         domain.ArtifactStage `json:"to"`
	Reason     string               `json:"reason"`
}

// QualityIncidentAcknowledged records operator ownership of an actionable
// quality incident before manual remediation.
type QualityIncidentAcknowledged struct {
	IncidentID string `json:"incident_id"`
	Note       string `json:"note"`
}

// QualityIncidentResolved closes one acknowledged replay-derived quality
// incident. Scheduler-driven freshness recovery remains automatic.
type QualityIncidentResolved struct {
	IncidentID string `json:"incident_id"`
	Reason     string `json:"reason"`
}
