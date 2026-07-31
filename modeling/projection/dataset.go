// SPDX-License-Identifier: AGPL-3.0-or-later

package projection

import (
	"context"
	"fmt"
	"sort"
	"time"

	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/modeling/domain"
	"github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// DatasetVersionView is one immutable dataset definition.
type DatasetVersionView struct {
	Version   int                `json:"version"`
	Spec      domain.DatasetSpec `json:"spec"`
	Hash      string             `json:"hash"`
	DefinedBy string             `json:"defined_by"`
	DefinedAt time.Time          `json:"defined_at"`
}

// DatasetView is the complete version history for a named dataset.
type DatasetView struct {
	Org       string               `json:"org"`
	Workspace string               `json:"workspace"`
	Name      string               `json:"name"`
	Versions  []DatasetVersionView `json:"versions"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// JobView is one durable modeling job.
type JobView struct {
	Org               string                   `json:"org"`
	Workspace         string                   `json:"workspace"`
	JobID             string                   `json:"job_id"`
	Kind              string                   `json:"kind"`
	State             string                   `json:"state"`
	Attempt           int                      `json:"attempt"`
	Worker            string                   `json:"worker,omitempty"`
	LeaseUntil        time.Time                `json:"lease_until,omitempty"`
	SnapshotID        string                   `json:"snapshot_id,omitempty"`
	DatasetName       string                   `json:"dataset_name,omitempty"`
	DatasetVersion    int                      `json:"dataset_version,omitempty"`
	DatasetHash       string                   `json:"dataset_hash,omitempty"`
	DatasetSpec       domain.DatasetSpec       `json:"dataset_spec,omitempty"`
	ObservationAt     time.Time                `json:"observation_at,omitempty"`
	KnowledgeAt       time.Time                `json:"knowledge_at,omitempty"`
	RequestHash       string                   `json:"request_hash"`
	FeatureVersions   map[string]int           `json:"feature_versions,omitempty"`
	ArtifactID        string                   `json:"artifact_id,omitempty"`
	ArtifactHash      string                   `json:"artifact_hash,omitempty"`
	ModelName         string                   `json:"model_name,omitempty"`
	TrainingRequest   domain.TrainingRequest   `json:"training_request,omitempty"`
	EvaluationID      string                   `json:"evaluation_id,omitempty"`
	EvaluationRequest domain.EvaluationRequest `json:"evaluation_request,omitempty"`
	SnapshotManifest  domain.SnapshotManifest  `json:"snapshot_manifest,omitempty"`
	BackfillID        string                   `json:"backfill_id,omitempty"`
	BackfillRequest   domain.BackfillRequest   `json:"backfill_request,omitempty"`
	Error             string                   `json:"error,omitempty"`
	Retryable         bool                     `json:"retryable,omitempty"`
	CompletedUnits    int64                    `json:"completed_units"`
	TotalUnits        int64                    `json:"total_units"`
	ProgressPercent   float64                  `json:"progress_percent"`
	Phase             string                   `json:"phase,omitempty"`
	ComputeUnits      int64                    `json:"compute_units"`
	EstimatedCostUSD  float64                  `json:"estimated_cost_usd"`
	Logs              []JobLog                 `json:"logs"`
	CreatedBy         string                   `json:"created_by"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	ExpiresAt         *time.Time               `json:"expires_at,omitempty"`
}

// JobLog is one replay-derived operational checkpoint. It contains
// control-plane metadata only; source rows and model bytes never enter it.
type JobLog struct {
	Time           time.Time `json:"time"`
	Attempt        int       `json:"attempt"`
	Worker         string    `json:"worker,omitempty"`
	Level          string    `json:"level"`
	Phase          string    `json:"phase"`
	Message        string    `json:"message"`
	CompletedUnits int64     `json:"completed_units,omitempty"`
	TotalUnits     int64     `json:"total_units,omitempty"`
	ComputeUnits   int64     `json:"compute_units,omitempty"`
}

// SnapshotView is one published immutable snapshot.
type SnapshotView struct {
	Org          string                  `json:"org"`
	Workspace    string                  `json:"workspace"`
	JobID        string                  `json:"job_id"`
	Manifest     domain.SnapshotManifest `json:"manifest"`
	PublishedBy  string                  `json:"published_by"`
	State        string                  `json:"state"`
	ExpiredAt    *time.Time              `json:"expired_at,omitempty"`
	ExpireReason string                  `json:"expire_reason,omitempty"`
}

// ArtifactView is one verified signed artifact registered with a model.
type ArtifactView struct {
	Org            string                             `json:"org"`
	Workspace      string                             `json:"workspace"`
	ArtifactID     string                             `json:"artifact_id"`
	ArtifactHash   string                             `json:"artifact_hash"`
	JobID          string                             `json:"job_id"`
	ModelName      string                             `json:"model_name"`
	Origin         domain.ArtifactOrigin              `json:"origin"`
	Stage          domain.ArtifactStage               `json:"stage"`
	OwnerTeam      string                             `json:"owner_team"`
	Format         string                             `json:"format"`
	Runtime        string                             `json:"runtime"`
	SizeBytes      int64                              `json:"size_bytes"`
	Dependencies   []domain.ArtifactDependency        `json:"dependencies"`
	Vulnerability  *domain.VulnerabilityEvidence      `json:"vulnerability,omitempty"`
	Explanation    domain.ExplanationContract         `json:"explanation"`
	Purpose        string                             `json:"purpose"`
	RetentionUntil time.Time                          `json:"retention_until"`
	External       *domain.ArtifactRegistration       `json:"external_registration,omitempty"`
	StageHistory   []ArtifactStageHistory             `json:"stage_history"`
	Lineage        decisionevents.ModelLineage        `json:"lineage"`
	Publication    decisionevents.TrainingPublication `json:"publication"`
	RegisteredBy   string                             `json:"registered_by"`
	RegisteredAt   time.Time                          `json:"registered_at"`
}

// ArtifactStageHistory is one replayable promotion or archive transition.
type ArtifactStageHistory struct {
	From      domain.ArtifactStage `json:"from,omitempty"`
	To        domain.ArtifactStage `json:"to"`
	ChangedBy string               `json:"changed_by"`
	ChangedAt time.Time            `json:"changed_at"`
	Reason    string               `json:"reason"`
}

// EvaluationView is one independently executed immutable report.
type EvaluationView struct {
	Org         string                    `json:"org"`
	Workspace   string                    `json:"workspace"`
	JobID       string                    `json:"job_id"`
	Manifest    domain.EvaluationManifest `json:"manifest"`
	EvaluatedBy string                    `json:"evaluated_by"`
}

// MaterializationView is one immutable feature backfill.
type MaterializationView struct {
	Org         string                  `json:"org"`
	Workspace   string                  `json:"workspace"`
	JobID       string                  `json:"job_id"`
	Manifest    domain.BackfillManifest `json:"manifest"`
	PublishedBy string                  `json:"published_by"`
}

func applyDatasetEvent(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	switch envelope.Type {
	case events.TypeDatasetDefined:
		var payload events.DatasetDefined
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		key := store.Key(envelope.Org, envelope.Workspace, payload.Name)
		view, _, err := store.GetDoc[DatasetView](ctx, st, CollectionDatasets, key)
		if err != nil {
			return err
		}
		view.Org, view.Workspace, view.Name = envelope.Org, envelope.Workspace, payload.Name
		view.Versions = append(view.Versions, DatasetVersionView{
			Version: payload.Version, Spec: payload.Spec, Hash: payload.Hash,
			DefinedBy: envelope.Actor, DefinedAt: envelope.Time,
		})
		sort.Slice(view.Versions, func(i, j int) bool {
			return view.Versions[i].Version < view.Versions[j].Version
		})
		view.UpdatedAt = envelope.Time
		return store.PutDoc(ctx, st, CollectionDatasets, key, view)
	case events.TypeSnapshotRequested:
		var payload events.SnapshotRequested
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionJobs,
			store.Key(envelope.Org, envelope.Workspace, payload.JobID), JobView{
				Org: envelope.Org, Workspace: envelope.Workspace, JobID: payload.JobID,
				Kind: "snapshot", State: "queued", SnapshotID: payload.SnapshotID,
				DatasetName: payload.DatasetName, DatasetVersion: payload.DatasetVersion,
				DatasetHash: payload.DatasetHash, DatasetSpec: payload.Spec,
				ObservationAt: payload.ObservationAt, KnowledgeAt: payload.KnowledgeAt,
				RequestHash: payload.RequestHash, FeatureVersions: payload.FeatureVersions,
				CreatedBy: envelope.Actor,
				CreatedAt: envelope.Time, UpdatedAt: envelope.Time, ExpiresAt: timePointer(payload.ExpiresAt),
				Logs: []JobLog{},
			})
	case events.TypeTrainingRequested:
		var payload events.TrainingRequested
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionJobs,
			store.Key(envelope.Org, envelope.Workspace, payload.JobID), JobView{
				Org: envelope.Org, Workspace: envelope.Workspace, JobID: payload.JobID,
				Kind: "training", State: "queued", ArtifactID: payload.ArtifactID,
				DatasetName:    payload.Snapshot.DatasetName,
				DatasetVersion: payload.Snapshot.DatasetVersion,
				DatasetHash:    payload.Snapshot.DatasetHash,
				RequestHash:    payload.RequestHash, TrainingRequest: payload.Request,
				SnapshotManifest: payload.Snapshot,
				CreatedBy:        envelope.Actor, CreatedAt: envelope.Time,
				UpdatedAt: envelope.Time, ExpiresAt: timePointer(payload.Snapshot.ExpiresAt),
				Logs: []JobLog{},
			})
	case events.TypeEvaluationRequested:
		var payload events.EvaluationRequested
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionJobs,
			store.Key(envelope.Org, envelope.Workspace, payload.JobID), JobView{
				Org: envelope.Org, Workspace: envelope.Workspace, JobID: payload.JobID,
				Kind: "evaluation", State: "queued", EvaluationID: payload.EvaluationID,
				EvaluationRequest: payload.Request, ArtifactID: payload.ArtifactID,
				ArtifactHash: payload.ArtifactHash, ModelName: payload.ModelName,
				SnapshotID:       payload.Snapshot.SnapshotID,
				DatasetName:      payload.Snapshot.DatasetName,
				DatasetVersion:   payload.Snapshot.DatasetVersion,
				DatasetHash:      payload.Snapshot.DatasetHash,
				SnapshotManifest: payload.Snapshot,
				RequestHash:      payload.RequestHash, CreatedBy: envelope.Actor,
				CreatedAt: envelope.Time, UpdatedAt: envelope.Time,
				ExpiresAt: timePointer(payload.Snapshot.ExpiresAt),
				Logs:      []JobLog{},
			})
	case events.TypeBackfillRequested:
		var payload events.BackfillRequested
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionJobs,
			store.Key(envelope.Org, envelope.Workspace, payload.JobID), JobView{
				Org: envelope.Org, Workspace: envelope.Workspace, JobID: payload.JobID,
				Kind: "backfill", State: "queued", BackfillID: payload.BackfillID,
				BackfillRequest: payload.Request, FeatureVersions: payload.FeatureVersions,
				RequestHash: payload.RequestHash, CreatedBy: envelope.Actor,
				CreatedAt: envelope.Time, UpdatedAt: envelope.Time,
				Logs: []JobLog{},
			})
	case events.TypeJobClaimed:
		var payload events.JobClaimed
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return mutateJob(ctx, st, envelope, payload.JobID, func(view *JobView) error {
			if view.State == "completed" || view.State == "cancelled" || payload.Attempt <= view.Attempt {
				return nil
			}
			view.State, view.Attempt = "running", payload.Attempt
			view.Worker, view.LeaseUntil = payload.Worker, payload.LeaseUntil
			view.Error, view.Retryable = "", false
			view.CompletedUnits, view.TotalUnits, view.ProgressPercent = 0, 0, 0
			view.Phase, view.ComputeUnits, view.EstimatedCostUSD = "starting", 0, 0
			appendJobLog(view, envelope, "info", "starting", "job attempt claimed")
			return nil
		})
	case events.TypeJobHeartbeat:
		var payload events.JobHeartbeat
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return mutateJob(ctx, st, envelope, payload.JobID, func(view *JobView) error {
			if view.State == "running" && view.Attempt == payload.Attempt && view.Worker == payload.Worker {
				view.LeaseUntil = payload.LeaseUntil
			}
			return nil
		})
	case events.TypeJobFailed:
		var payload events.JobFailed
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return mutateJob(ctx, st, envelope, payload.JobID, func(view *JobView) error {
			if view.State != "running" || view.Attempt != payload.Attempt {
				return nil
			}
			view.State, view.Error, view.Retryable = "failed", payload.Error, payload.Retryable
			view.Worker, view.LeaseUntil = "", time.Time{}
			view.Phase = "failed"
			appendJobLog(view, envelope, "error", "failed", payload.Error)
			return nil
		})
	case events.TypeJobProgressed:
		var payload events.JobProgressed
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return mutateJob(ctx, st, envelope, payload.JobID, func(view *JobView) error {
			if view.State != "running" || view.Attempt != payload.Attempt ||
				view.Worker != payload.Worker {
				return nil
			}
			if payload.CompletedUnits < view.CompletedUnits ||
				(view.TotalUnits > 0 && payload.TotalUnits != view.TotalUnits) ||
				payload.ComputeUnits < view.ComputeUnits ||
				payload.EstimatedCostUSD < view.EstimatedCostUSD {
				return fmt.Errorf(
					"modeling projection: job %q progress regressed at seq %d",
					view.JobID, envelope.Seq,
				)
			}
			view.CompletedUnits, view.TotalUnits = payload.CompletedUnits, payload.TotalUnits
			view.ProgressPercent = 100 * float64(payload.CompletedUnits) / float64(payload.TotalUnits)
			view.Phase = payload.Phase
			view.ComputeUnits, view.EstimatedCostUSD =
				payload.ComputeUnits, payload.EstimatedCostUSD
			appendJobLog(view, envelope, "info", payload.Phase, "progress checkpoint")
			return nil
		})
	case events.TypeJobPauseRequested:
		var payload events.JobTransition
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return mutateJob(ctx, st, envelope, payload.JobID, func(view *JobView) error {
			if view.State == "queued" || view.State == "running" || view.State == "failed" {
				view.State = "pausing"
				appendJobLog(view, envelope, "info", "pausing", payload.Reason)
			}
			return nil
		})
	case events.TypeJobPaused:
		var payload events.JobTransition
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return settleJob(ctx, st, envelope, payload, "pausing", "paused")
	case events.TypeJobResumed, events.TypeJobRetryRequested:
		var payload events.JobTransition
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return mutateJob(ctx, st, envelope, payload.JobID, func(view *JobView) error {
			view.State, view.Worker, view.LeaseUntil = "queued", "", time.Time{}
			view.Error, view.Retryable, view.Phase = "", false, "queued"
			appendJobLog(view, envelope, "info", "queued", payload.Reason)
			return nil
		})
	case events.TypeJobCancelRequested:
		var payload events.JobTransition
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return mutateJob(ctx, st, envelope, payload.JobID, func(view *JobView) error {
			if view.State == "completed" || view.State == "cancelled" {
				return nil
			}
			view.State = "cancelling"
			appendJobLog(view, envelope, "info", "cancelling", payload.Reason)
			return nil
		})
	case events.TypeJobCancelled:
		var payload events.JobTransition
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return settleJob(ctx, st, envelope, payload, "cancelling", "cancelled")
	case events.TypeSnapshotPublished:
		var payload events.SnapshotPublished
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return publishView(ctx, st, envelope, CollectionSnapshots,
			payload.JobID, payload.Attempt, payload.Manifest.SnapshotID,
			SnapshotView{
				Org: envelope.Org, Workspace: envelope.Workspace, JobID: payload.JobID,
				Manifest: payload.Manifest, PublishedBy: envelope.Actor, State: "available",
			})
	case events.TypeSnapshotExpired:
		var payload events.SnapshotExpired
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		key := store.Key(envelope.Org, envelope.Workspace, payload.SnapshotID)
		view, found, err := store.GetDoc[SnapshotView](ctx, st, CollectionSnapshots, key)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("modeling projection: expired snapshot %q is missing", payload.SnapshotID)
		}
		if view.Manifest.StorageRef != payload.StorageRef {
			return fmt.Errorf("modeling projection: expired snapshot %q storage reference changed", payload.SnapshotID)
		}
		expiredAt := payload.ExpiredAt
		view.State, view.ExpiredAt, view.ExpireReason = "expired", &expiredAt, payload.Reason
		if err := store.PutDoc(ctx, st, CollectionSnapshots, key, view); err != nil {
			return err
		}
		return st.Delete(
			ctx, CollectionSnapshotBlobs,
			store.Key(envelope.Org, envelope.Workspace, payload.StorageRef),
		)
	case decisionevents.TypeModelDefined:
		var payload decisionevents.ModelDefined
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		if payload.Lineage == nil || payload.Training == nil {
			return nil
		}
		if err := mutateJob(
			ctx, st, envelope, payload.Lineage.TrainingJobID, func(view *JobView) error {
				if view.State != "running" || view.Attempt != payload.Training.Attempt {
					return nil
				}
				view.State, view.Worker, view.LeaseUntil = "completed", "", time.Time{}
				completeJob(view)
				return nil
			},
		); err != nil {
			return err
		}
		artifactID := payload.Lineage.ArtifactID
		ownerTeam, purpose, retentionUntil, err := trainedArtifactGovernance(
			ctx, st, envelope, *payload.Lineage,
		)
		if err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionArtifacts,
			store.Key(envelope.Org, envelope.Workspace, artifactID), ArtifactView{
				Org: envelope.Org, Workspace: envelope.Workspace,
				ArtifactID: artifactID, ArtifactHash: payload.Lineage.ArtifactHash,
				JobID: payload.Lineage.TrainingJobID, ModelName: payload.Name,
				Origin: domain.ArtifactPlatformTrained, Stage: domain.ArtifactRegistered,
				OwnerTeam: ownerTeam, Format: "intraktible-model-json/v1",
				Runtime: payload.Lineage.Runtime, SizeBytes: int64(len(payload.Spec)),
				Dependencies: []domain.ArtifactDependency{},
				Explanation:  explanationForRuntime(payload.Lineage.Runtime),
				Purpose:      purpose, RetentionUntil: retentionUntil,
				StageHistory: []ArtifactStageHistory{{
					To: domain.ArtifactRegistered, ChangedBy: envelope.Actor,
					ChangedAt: envelope.Time, Reason: "platform training publication verified",
				}},
				Lineage: *payload.Lineage, Publication: *payload.Training,
				RegisteredBy: envelope.Actor, RegisteredAt: envelope.Time,
			})
	case events.TypeArtifactRegistered:
		var payload events.ArtifactRegistered
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		registration := payload.Registration
		return store.PutDoc(
			ctx, st, CollectionArtifacts,
			store.Key(envelope.Org, envelope.Workspace, registration.ArtifactID),
			ArtifactView{
				Org: envelope.Org, Workspace: envelope.Workspace,
				ArtifactID: registration.ArtifactID, ArtifactHash: registration.ArtifactHash,
				ModelName: registration.ModelName,
				Origin:    domain.ArtifactExternal, Stage: domain.ArtifactRegistered,
				OwnerTeam: registration.OwnerTeam, Format: registration.Format,
				Runtime: registration.Runtime, SizeBytes: registration.SizeBytes,
				Dependencies:  append([]domain.ArtifactDependency(nil), registration.Dependencies...),
				Vulnerability: &registration.Vulnerability,
				Explanation:   registration.Explanation, Purpose: registration.Purpose,
				RetentionUntil: registration.RetentionUntil, External: &registration,
				StageHistory: []ArtifactStageHistory{{
					To: domain.ArtifactRegistered, ChangedBy: envelope.Actor,
					ChangedAt: envelope.Time, Reason: "external signature and provenance verified",
				}},
				RegisteredBy: envelope.Actor, RegisteredAt: envelope.Time,
			},
		)
	case events.TypeArtifactStageChanged:
		var payload events.ArtifactStageChanged
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return mutateArtifact(
			ctx, st, envelope, payload.ArtifactID, func(view *ArtifactView) error {
				if view.Stage != payload.From {
					return fmt.Errorf(
						"modeling projection: artifact %q stage is %s, transition says %s",
						payload.ArtifactID, view.Stage, payload.From,
					)
				}
				view.Stage = payload.To
				view.StageHistory = append(view.StageHistory, ArtifactStageHistory{
					From: payload.From, To: payload.To, ChangedBy: envelope.Actor,
					ChangedAt: envelope.Time, Reason: payload.Reason,
				})
				return nil
			},
		)
	case events.TypeBackfillPublished:
		var payload events.BackfillPublished
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return publishView(ctx, st, envelope, CollectionMaterializations,
			payload.JobID, payload.Attempt, payload.Manifest.BackfillID,
			MaterializationView{
				Org: envelope.Org, Workspace: envelope.Workspace,
				JobID: payload.JobID, Manifest: payload.Manifest, PublishedBy: envelope.Actor,
			})
	case events.TypeEvaluationPublished:
		var payload events.EvaluationPublished
		if err := decode(envelope, &payload); err != nil {
			return err
		}
		return publishView(ctx, st, envelope, CollectionEvaluations,
			payload.JobID, payload.Attempt, payload.Manifest.EvaluationID,
			EvaluationView{
				Org: envelope.Org, Workspace: envelope.Workspace, JobID: payload.JobID,
				Manifest: payload.Manifest, EvaluatedBy: envelope.Actor,
			})
	default:
		return nil
	}
}

// settleJob applies a guard-then-settle job transition (pause, cancel).
func settleJob(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	payload events.JobTransition,
	from string,
	to string,
) error {
	return mutateJob(ctx, st, envelope, payload.JobID, func(view *JobView) error {
		if view.State != from {
			return fmt.Errorf("modeling projection: job %q %s from %s", view.JobID, to, view.State)
		}
		view.State, view.Worker, view.LeaseUntil = to, "", time.Time{}
		view.Phase = to
		appendJobLog(view, envelope, "info", to, payload.Reason)
		return nil
	})
}

// publishView completes the owning job attempt and stores the publication
// read model under the manifest id.
func publishView[V any](
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	collection string,
	jobID string,
	attempt int,
	manifestID string,
	view V,
) error {
	if err := completeRunningAttempt(ctx, st, envelope, jobID, attempt); err != nil {
		return err
	}
	return store.PutDoc(
		ctx, st, collection,
		store.Key(envelope.Org, envelope.Workspace, manifestID), view,
	)
}

// completeRunningAttempt marks the job completed when a publication matches
// its active attempt; stale attempts are ignored.
func completeRunningAttempt(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	jobID string,
	attempt int,
) error {
	return mutateJob(ctx, st, envelope, jobID, func(view *JobView) error {
		if view.State != "running" || view.Attempt != attempt {
			return nil
		}
		view.State, view.Worker, view.LeaseUntil = "completed", "", time.Time{}
		completeJob(view)
		return nil
	})
}

func completeJob(view *JobView) {
	if view.TotalUnits == 0 {
		view.TotalUnits = 1
	}
	view.CompletedUnits = view.TotalUnits
	view.ProgressPercent = 100
	view.Phase = "completed"
}

func appendJobLog(
	view *JobView,
	envelope eventlog.Envelope,
	level string,
	phase string,
	message string,
) {
	view.Logs = append(view.Logs, JobLog{
		Time: envelope.Time, Attempt: view.Attempt, Worker: view.Worker,
		Level: level, Phase: phase, Message: message,
		CompletedUnits: view.CompletedUnits, TotalUnits: view.TotalUnits,
		ComputeUnits: view.ComputeUnits,
	})
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	out := value
	return &out
}

// ListEvaluations returns newest independent evaluations first.
func ListEvaluations(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]EvaluationView, error) {
	return store.QueryDocs(
		ctx, st, CollectionEvaluations, store.Key(id.Org, id.Workspace, ""), nil,
		func(left, right EvaluationView) bool {
			return left.Manifest.EvaluatedAt.After(right.Manifest.EvaluatedAt)
		},
	)
}

// ReadEvaluation returns one independent evaluation.
func ReadEvaluation(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	evaluationID string,
) (EvaluationView, bool, error) {
	return store.GetDoc[EvaluationView](
		ctx, st, CollectionEvaluations,
		store.Key(id.Org, id.Workspace, evaluationID),
	)
}

// ListMaterializations returns newest feature backfills first.
func ListMaterializations(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]MaterializationView, error) {
	return store.QueryDocs(ctx, st, CollectionMaterializations, store.Key(id.Org, id.Workspace, ""), nil,
		func(left, right MaterializationView) bool {
			return left.Manifest.PublishedAt.After(right.Manifest.PublishedAt)
		})
}

// ReadMaterialization returns one feature backfill.
func ReadMaterialization(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	backfillID string,
) (MaterializationView, bool, error) {
	return store.GetDoc[MaterializationView](ctx, st, CollectionMaterializations,
		store.Key(id.Org, id.Workspace, backfillID))
}

func mutateJob(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	jobID string,
	mutate func(*JobView) error,
) error {
	key := store.Key(envelope.Org, envelope.Workspace, jobID)
	view, found, err := store.GetDoc[JobView](ctx, st, CollectionJobs, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("modeling projection: job %q is missing at seq %d", jobID, envelope.Seq)
	}
	if err := mutate(&view); err != nil {
		return err
	}
	view.UpdatedAt = envelope.Time
	return store.PutDoc(ctx, st, CollectionJobs, key, view)
}

func mutateArtifact(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	artifactID string,
	mutate func(*ArtifactView) error,
) error {
	key := store.Key(envelope.Org, envelope.Workspace, artifactID)
	view, found, err := store.GetDoc[ArtifactView](ctx, st, CollectionArtifacts, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"modeling projection: artifact %q is missing at seq %d",
			artifactID, envelope.Seq,
		)
	}
	if err := mutate(&view); err != nil {
		return err
	}
	return store.PutDoc(ctx, st, CollectionArtifacts, key, view)
}

func trainedArtifactGovernance(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	lineage decisionevents.ModelLineage,
) (string, string, time.Time, error) {
	dataset, found, err := store.GetDoc[DatasetView](
		ctx, st, CollectionDatasets,
		store.Key(envelope.Org, envelope.Workspace, lineage.DatasetName),
	)
	if err != nil {
		return "", "", time.Time{}, err
	}
	if !found {
		return "", "", time.Time{}, fmt.Errorf(
			"modeling projection: trained artifact dataset %q is missing",
			lineage.DatasetName,
		)
	}
	var spec *domain.DatasetSpec
	for index := range dataset.Versions {
		if dataset.Versions[index].Version == lineage.DatasetVersion {
			spec = &dataset.Versions[index].Spec
			break
		}
	}
	if spec == nil {
		return "", "", time.Time{}, fmt.Errorf(
			"modeling projection: trained artifact dataset %q version %d is missing",
			lineage.DatasetName, lineage.DatasetVersion,
		)
	}
	snapshot, found, err := store.GetDoc[SnapshotView](
		ctx, st, CollectionSnapshots,
		store.Key(envelope.Org, envelope.Workspace, lineage.SnapshotID),
	)
	if err != nil {
		return "", "", time.Time{}, err
	}
	if !found || snapshot.Manifest.RowsHash != lineage.SnapshotHash {
		return "", "", time.Time{}, fmt.Errorf(
			"modeling projection: trained artifact snapshot %q lineage is missing or changed",
			lineage.SnapshotID,
		)
	}
	return spec.OwnerTeam, spec.Purpose, snapshot.Manifest.ExpiresAt, nil
}

func explanationForRuntime(runtime string) domain.ExplanationContract {
	if runtime == string(domain.RuntimeLogisticV1) {
		return domain.ExplanationContract{
			LocalSupported: true, GlobalSupported: true,
			Method:      "signed coefficient contribution",
			Limitations: "Coefficients describe association in the fitted feature space, not causality; correlated and transformed features can make contributions unstable.",
		}
	}
	return domain.ExplanationContract{
		Limitations: "This runtime does not publish a platform-verified faithful explanation method.",
	}
}

// ListDatasets returns named dataset histories.
func ListDatasets(ctx context.Context, st store.Store, id identity.Identity) ([]DatasetView, error) {
	return store.QueryDocs(ctx, st, CollectionDatasets, store.Key(id.Org, id.Workspace, ""), nil,
		func(left, right DatasetView) bool { return left.Name < right.Name })
}

// ReadDataset returns one dataset history.
func ReadDataset(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	name string,
) (DatasetView, bool, error) {
	return store.GetDoc[DatasetView](ctx, st, CollectionDatasets, store.Key(id.Org, id.Workspace, name))
}

// ListJobs returns newest jobs first.
func ListJobs(ctx context.Context, st store.Store, id identity.Identity) ([]JobView, error) {
	return store.QueryDocs(ctx, st, CollectionJobs, store.Key(id.Org, id.Workspace, ""), nil,
		func(left, right JobView) bool { return left.CreatedAt.After(right.CreatedAt) })
}

// ReadJob returns one modeling job.
func ReadJob(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	jobID string,
) (JobView, bool, error) {
	return store.GetDoc[JobView](ctx, st, CollectionJobs, store.Key(id.Org, id.Workspace, jobID))
}

// ListSnapshots returns newest snapshots first.
func ListSnapshots(ctx context.Context, st store.Store, id identity.Identity) ([]SnapshotView, error) {
	return store.QueryDocs(ctx, st, CollectionSnapshots, store.Key(id.Org, id.Workspace, ""), nil,
		func(left, right SnapshotView) bool {
			return left.Manifest.PublishedAt.After(right.Manifest.PublishedAt)
		})
}

// ReadSnapshot returns one snapshot.
func ReadSnapshot(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	snapshotID string,
) (SnapshotView, bool, error) {
	return store.GetDoc[SnapshotView](ctx, st, CollectionSnapshots,
		store.Key(id.Org, id.Workspace, snapshotID))
}

// ListArtifacts returns newest registered artifacts first.
func ListArtifacts(ctx context.Context, st store.Store, id identity.Identity) ([]ArtifactView, error) {
	return store.QueryDocs(ctx, st, CollectionArtifacts, store.Key(id.Org, id.Workspace, ""), nil,
		func(left, right ArtifactView) bool { return left.RegisteredAt.After(right.RegisteredAt) })
}

// ReadArtifact returns one signed artifact manifest.
func ReadArtifact(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	artifactID string,
) (ArtifactView, bool, error) {
	return store.GetDoc[ArtifactView](ctx, st, CollectionArtifacts,
		store.Key(id.Org, id.Workspace, artifactID))
}
