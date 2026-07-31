// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/modeling/domain"
	"github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

const defaultJobAttempts = 3

func hashJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// DefineDataset creates the next immutable version.
func (h *Handler) DefineDataset(
	ctx context.Context,
	id identity.Identity,
	spec domain.DatasetSpec,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := spec.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	hash, err := hashJSON(spec)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("modeling: hash dataset: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; attempt < maxClaimRetries; attempt++ {
		versions, err := h.foldDatasets(ctx, id, spec.Name)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		version := 1
		for candidate := range versions {
			if candidate >= version {
				version = candidate + 1
			}
		}
		envelope, err := h.appendUnique(ctx, id, events.TypeDatasetDefined, events.DatasetDefined{
			Name: spec.Name, Version: version, Spec: spec, Hash: hash,
		}, "modeling.dataset.version\x00"+spec.Name+"\x00"+fmt.Sprint(version))
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		return envelope, err
	}
	return eventlog.Envelope{}, fmt.Errorf("modeling: concurrent dataset definitions for %q did not settle", spec.Name)
}

func (h *Handler) foldDatasets(
	ctx context.Context,
	id identity.Identity,
	name string,
) (map[int]events.DatasetDefined, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamModeling, 0)
	if err != nil {
		return nil, err
	}
	versions := make(map[int]events.DatasetDefined)
	for _, envelope := range envelopes {
		if envelope.Type != events.TypeDatasetDefined {
			continue
		}
		var payload events.DatasetDefined
		if err := decode(envelope, &payload); err != nil {
			return nil, err
		}
		if payload.Name == name {
			versions[payload.Version] = payload
		}
	}
	return versions, nil
}

// RequestSnapshot creates a durable snapshot job pinned to an immutable
// definition and bitemporal cutoff.
func (h *Handler) RequestSnapshot(
	ctx context.Context,
	id identity.Identity,
	request domain.SnapshotRequest,
	featureVersions map[string]int,
) (string, string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	if err := request.Validate(); err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	versions, err := h.foldDatasets(ctx, id, request.DatasetName)
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	definition, ok := versions[request.Version]
	if !ok {
		return "", "", eventlog.Envelope{}, fmt.Errorf(
			"modeling: unknown dataset %q version %d", request.DatasetName, request.Version,
		)
	}
	if len(featureVersions) != len(definition.Spec.Features) {
		return "", "", eventlog.Envelope{}, errors.New(
			"modeling: every dataset feature must have a pinned definition version",
		)
	}
	for _, feature := range definition.Spec.Features {
		if featureVersions[feature] <= 0 {
			return "", "", eventlog.Envelope{}, fmt.Errorf(
				"modeling: feature %q has no pinned version", feature,
			)
		}
	}
	requestHash, err := hashJSON(struct {
		Name          string
		Version       int
		ObservationAt time.Time
		KnowledgeAt   time.Time
	}{
		Name: request.DatasetName, Version: request.Version,
		ObservationAt: request.ObservationAt.UTC(), KnowledgeAt: request.KnowledgeAt.UTC(),
	})
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	idempotencyHash := sha256.Sum256([]byte(strings.TrimSpace(request.IdempotencyKey)))
	jobID, snapshotID := newID(), newID()
	expiresAt := h.now().Add(time.Duration(definition.Spec.RetentionDays) * 24 * time.Hour)
	envelope, err := h.appendUnique(ctx, id, events.TypeSnapshotRequested, events.SnapshotRequested{
		JobID: jobID, SnapshotID: snapshotID,
		DatasetName: request.DatasetName, DatasetVersion: request.Version,
		DatasetHash: definition.Hash, Spec: definition.Spec,
		ObservationAt: request.ObservationAt.UTC(), KnowledgeAt: request.KnowledgeAt.UTC(),
		RequestHash: requestHash, FeatureVersions: featureVersions, ExpiresAt: expiresAt,
	}, "modeling.snapshot.idempotency\x00"+hex.EncodeToString(idempotencyHash[:]))
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	return jobID, snapshotID, envelope, nil
}

type jobState struct {
	exists     bool
	kind       string
	attempt    int
	worker     string
	leaseUntil time.Time
	state      string
	retryable  bool
	cancelled  bool
	progress   int64
	total      int64
	compute    int64
	cost       float64
}

func (h *Handler) foldJob(
	ctx context.Context,
	id identity.Identity,
	jobID string,
) (jobState, error) {
	envelopes, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, events.StreamModeling, 0)
	if err != nil {
		return jobState{}, err
	}
	modelEnvelopes, err := h.log.ReadTenantStream(
		ctx, id.Org, id.Workspace, decisionevents.StreamModels, 0,
	)
	if err != nil {
		return jobState{}, err
	}
	envelopes = append(envelopes, modelEnvelopes...)
	sort.Slice(envelopes, func(i, j int) bool { return envelopes[i].Seq < envelopes[j].Seq })
	var state jobState
	for _, envelope := range envelopes {
		switch envelope.Type {
		case events.TypeSnapshotRequested:
			var payload events.SnapshotRequested
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID {
				state.exists, state.kind, state.state = true, "snapshot", "queued"
			}
		case events.TypeTrainingRequested:
			var payload events.TrainingRequested
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID {
				state.exists, state.kind, state.state = true, "training", "queued"
			}
		case events.TypeEvaluationRequested:
			var payload events.EvaluationRequested
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID {
				state.exists, state.kind, state.state = true, "evaluation", "queued"
			}
		case events.TypeBackfillRequested:
			var payload events.BackfillRequested
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID {
				state.exists, state.kind, state.state = true, "backfill", "queued"
			}
		case events.TypeJobClaimed:
			var payload events.JobClaimed
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID && payload.Attempt > state.attempt {
				state.attempt, state.worker, state.leaseUntil = payload.Attempt, payload.Worker, payload.LeaseUntil
				state.state = "running"
				state.progress, state.total, state.compute, state.cost = 0, 0, 0, 0
			}
		case events.TypeJobHeartbeat:
			var payload events.JobHeartbeat
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID && payload.Attempt == state.attempt &&
				payload.Worker == state.worker && state.state == "running" {
				state.leaseUntil = payload.LeaseUntil
			}
		case events.TypeJobFailed:
			var payload events.JobFailed
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID && payload.Attempt == state.attempt {
				state.state, state.retryable = "failed", payload.Retryable
			}
		case events.TypeJobProgressed:
			var payload events.JobProgressed
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID && payload.Attempt == state.attempt &&
				payload.Worker == state.worker && state.state == "running" {
				state.progress, state.total = payload.CompletedUnits, payload.TotalUnits
				state.compute, state.cost = payload.ComputeUnits, payload.EstimatedCostUSD
			}
		case events.TypeJobPauseRequested:
			var payload events.JobTransition
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID {
				state.state = "pausing"
			}
		case events.TypeJobPaused:
			var payload events.JobTransition
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID {
				state.state, state.worker, state.leaseUntil = "paused", "", time.Time{}
			}
		case events.TypeJobResumed, events.TypeJobRetryRequested:
			var payload events.JobTransition
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID {
				state.state, state.worker, state.leaseUntil = "queued", "", time.Time{}
				state.retryable = false
			}
		case events.TypeSnapshotPublished:
			var payload events.SnapshotPublished
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID && payload.Attempt == state.attempt {
				state.state = "completed"
			}
		case events.TypeBackfillPublished:
			var payload events.BackfillPublished
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID && payload.Attempt == state.attempt {
				state.state = "completed"
			}
		case events.TypeEvaluationPublished:
			var payload events.EvaluationPublished
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID && payload.Attempt == state.attempt {
				state.state = "completed"
			}
		case decisionevents.TypeModelDefined:
			var payload decisionevents.ModelDefined
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.Lineage != nil && payload.Lineage.TrainingJobID == jobID &&
				payload.Training != nil && payload.Training.Attempt == state.attempt {
				state.state = "completed"
			}
		case events.TypeJobCancelRequested:
			var payload events.JobTransition
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID {
				state.state = "cancelling"
			}
		case events.TypeJobCancelled:
			var payload events.JobTransition
			if err := decode(envelope, &payload); err != nil {
				return jobState{}, err
			}
			if payload.JobID == jobID {
				state.state, state.cancelled = "cancelled", true
			}
		}
	}
	return state, nil
}

// ownRunningAttempt proves the caller still holds the job's active attempt.
func (h *Handler) ownRunningAttempt(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	attempt int,
	worker string,
) error {
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return err
	}
	if state.state != "running" || state.attempt != attempt || state.worker != worker {
		return errors.New("modeling: caller does not own the active job attempt")
	}
	return nil
}

// RequestEvaluation creates a durable independent evaluation over exact
// published artifact and snapshot manifests.
func (h *Handler) RequestEvaluation(
	ctx context.Context,
	id identity.Identity,
	request domain.EvaluationRequest,
	artifactID string,
	artifactHash string,
	modelName string,
	snapshot domain.SnapshotManifest,
) (string, string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	if err := request.Validate(); err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	if request.ArtifactID != artifactID || request.SnapshotID != snapshot.SnapshotID ||
		artifactHash == "" || modelName == "" || snapshot.RowsHash == "" {
		return "", "", eventlog.Envelope{}, errors.New(
			"modeling: evaluation lineage is incomplete or mismatched",
		)
	}
	requestHash, err := hashJSON(struct {
		Request      domain.EvaluationRequest
		ArtifactHash string
		SnapshotHash string
	}{
		Request: request, ArtifactHash: artifactHash, SnapshotHash: snapshot.RowsHash,
	})
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	keyHash := sha256.Sum256([]byte(strings.TrimSpace(request.IdempotencyKey)))
	jobID, evaluationID := newID(), newID()
	envelope, err := h.appendUnique(
		ctx, id, events.TypeEvaluationRequested, events.EvaluationRequested{
			JobID: jobID, EvaluationID: evaluationID, Request: request,
			ArtifactID: artifactID, ArtifactHash: artifactHash, ModelName: modelName,
			Snapshot: snapshot, RequestHash: requestHash,
		},
		"modeling.evaluation.idempotency\x00"+hex.EncodeToString(keyHash[:]),
	)
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	return jobID, evaluationID, envelope, nil
}

// PublishEvaluation completes the active evaluator attempt.
func (h *Handler) PublishEvaluation(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	attempt int,
	worker string,
	manifest domain.EvaluationManifest,
) (eventlog.Envelope, error) {
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.state != "running" || state.attempt != attempt || state.worker != worker {
		return eventlog.Envelope{}, errors.New(
			"modeling: evaluation publication does not own active attempt",
		)
	}
	if manifest.EvaluationID == "" || manifest.ArtifactID == "" ||
		manifest.ArtifactHash == "" || manifest.SnapshotID == "" ||
		manifest.SnapshotHash == "" || len(manifest.ReportHash) != 64 {
		return eventlog.Envelope{}, errors.New("modeling: evaluation manifest is incomplete")
	}
	return h.appendUnique(
		ctx, id, events.TypeEvaluationPublished,
		events.EvaluationPublished{JobID: jobID, Attempt: attempt, Manifest: manifest},
		"modeling.job.outcome\x00"+jobID+"\x00"+fmt.Sprint(attempt),
	)
}

// RequestBackfill creates a durable feature-materialization job.
func (h *Handler) RequestBackfill(
	ctx context.Context,
	id identity.Identity,
	request domain.BackfillRequest,
	featureVersions map[string]int,
) (string, string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	if err := request.Validate(); err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	if len(featureVersions) != len(request.Features) {
		return "", "", eventlog.Envelope{}, errors.New(
			"modeling: every backfill feature must have a pinned version",
		)
	}
	for _, feature := range request.Features {
		if featureVersions[feature] <= 0 {
			return "", "", eventlog.Envelope{}, fmt.Errorf(
				"modeling: backfill feature %q has no pinned version", feature,
			)
		}
	}
	requestHash, err := hashJSON(struct {
		Request         domain.BackfillRequest
		FeatureVersions map[string]int
	}{Request: request, FeatureVersions: featureVersions})
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	keyHash := sha256.Sum256([]byte(strings.TrimSpace(request.IdempotencyKey)))
	jobID, backfillID := newID(), newID()
	envelope, err := h.appendUnique(ctx, id, events.TypeBackfillRequested, events.BackfillRequested{
		JobID: jobID, BackfillID: backfillID, Request: request,
		FeatureVersions: featureVersions, RequestHash: requestHash,
	}, "modeling.backfill.idempotency\x00"+hex.EncodeToString(keyHash[:]))
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	return jobID, backfillID, envelope, nil
}

// PublishBackfill completes the active attempt with its verified manifest.
func (h *Handler) PublishBackfill(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	attempt int,
	worker string,
	manifest domain.BackfillManifest,
) (eventlog.Envelope, error) {
	return h.publishJobManifest(
		ctx, id, jobID, attempt, worker, "backfill",
		manifest.BackfillID, manifest.RowsHash, manifest.StorageRef,
		events.TypeBackfillPublished, events.BackfillPublished{
			JobID: jobID, Attempt: attempt, Manifest: manifest,
		},
	)
}

// publishJobManifest proves attempt ownership, rejects an incomplete manifest,
// and appends the exactly-once publication event.
func (h *Handler) publishJobManifest(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	attempt int,
	worker string,
	kind string,
	manifestID string,
	rowsHash string,
	storageRef string,
	eventType string,
	payload any,
) (eventlog.Envelope, error) {
	if err := h.ownRunningAttempt(ctx, id, jobID, attempt, worker); err != nil {
		return eventlog.Envelope{}, err
	}
	if manifestID == "" || rowsHash == "" || storageRef == "" {
		return eventlog.Envelope{}, fmt.Errorf("modeling: %s manifest is incomplete", kind)
	}
	return h.appendUnique(ctx, id, eventType, payload,
		"modeling.job.outcome\x00"+jobID+"\x00"+fmt.Sprint(attempt))
}

// RequestTraining creates a durable, allowlisted training job over an immutable
// snapshot.
func (h *Handler) RequestTraining(
	ctx context.Context,
	id identity.Identity,
	request domain.TrainingRequest,
	snapshot domain.SnapshotManifest,
) (string, string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	if err := request.Validate(); err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	if snapshot.SnapshotID != request.SnapshotID || snapshot.RowsHash == "" ||
		snapshot.DatasetName == "" || snapshot.DatasetVersion <= 0 {
		return "", "", eventlog.Envelope{}, errors.New(
			"modeling: training snapshot manifest is incomplete or mismatched",
		)
	}
	requestHash, err := hashJSON(struct {
		Request      domain.TrainingRequest
		SnapshotHash string
	}{Request: request, SnapshotHash: snapshot.RowsHash})
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	keyHash := sha256.Sum256([]byte(strings.TrimSpace(request.IdempotencyKey)))
	jobID, artifactID := newID(), newID()
	envelope, err := h.appendUnique(ctx, id, events.TypeTrainingRequested, events.TrainingRequested{
		JobID: jobID, ArtifactID: artifactID, Request: request,
		Snapshot: snapshot, RequestHash: requestHash,
	}, "modeling.training.idempotency\x00"+hex.EncodeToString(keyHash[:]))
	if err != nil {
		return "", "", eventlog.Envelope{}, err
	}
	return jobID, artifactID, envelope, nil
}

// ClaimJob leases one job attempt. Competing replicas derive the same next
// attempt and race on one durable unique claim.
func (h *Handler) ClaimJob(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	worker string,
	lease time.Duration,
) (int, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return 0, eventlog.Envelope{}, err
	}
	if strings.TrimSpace(worker) == "" || lease <= 0 {
		return 0, eventlog.Envelope{}, errors.New("modeling: worker and positive lease are required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return 0, eventlog.Envelope{}, err
	}
	if !state.exists {
		return 0, eventlog.Envelope{}, fmt.Errorf("modeling: unknown job %q", jobID)
	}
	now := h.now()
	claimable := state.state == "queued" ||
		(state.state == "running" && !state.leaseUntil.After(now)) ||
		(state.state == "failed" && state.retryable)
	if !claimable {
		return 0, eventlog.Envelope{}, fmt.Errorf("modeling: job %q is not claimable in state %s", jobID, state.state)
	}
	attempt := state.attempt + 1
	if attempt > defaultJobAttempts {
		return 0, eventlog.Envelope{}, fmt.Errorf("modeling: job %q exhausted %d attempts", jobID, defaultJobAttempts)
	}
	envelope, err := h.appendUnique(ctx, id, events.TypeJobClaimed, events.JobClaimed{
		JobID: jobID, Attempt: attempt, Worker: worker, LeaseUntil: now.Add(lease),
	}, "modeling.job.claim\x00"+jobID+"\x00"+fmt.Sprint(attempt))
	return attempt, envelope, err
}

// HeartbeatJob extends the active attempt.
func (h *Handler) HeartbeatJob(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	attempt int,
	worker string,
	lease time.Duration,
) (eventlog.Envelope, error) {
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.state != "running" || state.attempt != attempt || state.worker != worker {
		return eventlog.Envelope{}, fmt.Errorf("modeling: heartbeat does not own active job attempt")
	}
	return h.appendUnique(ctx, id, events.TypeJobHeartbeat, events.JobHeartbeat{
		JobID: jobID, Attempt: attempt, Worker: worker, LeaseUntil: h.now().Add(lease),
	}, "")
}

// FailJob records a fenced attempt failure.
func (h *Handler) FailJob(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	attempt int,
	worker string,
	cause error,
	retryable bool,
) (eventlog.Envelope, error) {
	if cause == nil {
		return eventlog.Envelope{}, errors.New("modeling: job failure cause is required")
	}
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.state != "running" || state.attempt != attempt || state.worker != worker {
		return eventlog.Envelope{}, fmt.Errorf("modeling: failure does not own active job attempt")
	}
	if attempt >= defaultJobAttempts {
		retryable = false
	}
	return h.appendUnique(ctx, id, events.TypeJobFailed, events.JobFailed{
		JobID: jobID, Attempt: attempt, Error: cause.Error(), Retryable: retryable,
	}, "modeling.job.outcome\x00"+jobID+"\x00"+fmt.Sprint(attempt))
}

// ReportJobProgress appends monotonic progress for the active fenced attempt.
func (h *Handler) ReportJobProgress(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	attempt int,
	worker string,
	completedUnits int64,
	totalUnits int64,
	phase string,
	computeUnits int64,
	estimatedCostUSD float64,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if completedUnits < 0 || totalUnits <= 0 || completedUnits > totalUnits ||
		computeUnits < 0 || estimatedCostUSD < 0 ||
		math.IsNaN(estimatedCostUSD) || math.IsInf(estimatedCostUSD, 0) ||
		strings.TrimSpace(phase) == "" {
		return eventlog.Envelope{}, errors.New("modeling: valid job progress evidence is required")
	}
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.state != "running" || state.attempt != attempt || state.worker != worker {
		return eventlog.Envelope{}, errors.New("modeling: progress does not own active job attempt")
	}
	if completedUnits < state.progress ||
		(state.total > 0 && totalUnits != state.total) ||
		computeUnits < state.compute || estimatedCostUSD < state.cost {
		return eventlog.Envelope{}, errors.New("modeling: job progress must be monotonic")
	}
	return h.appendUnique(ctx, id, events.TypeJobProgressed, events.JobProgressed{
		JobID: jobID, Attempt: attempt, Worker: worker,
		CompletedUnits: completedUnits, TotalUnits: totalUnits,
		Phase: strings.TrimSpace(phase), ComputeUnits: computeUnits,
		EstimatedCostUSD: estimatedCostUSD,
	}, "")
}

// PublishSnapshot atomically completes the active attempt with its manifest.
func (h *Handler) PublishSnapshot(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	attempt int,
	worker string,
	manifest domain.SnapshotManifest,
) (eventlog.Envelope, error) {
	return h.publishJobManifest(
		ctx, id, jobID, attempt, worker, "snapshot",
		manifest.SnapshotID, manifest.RowsHash, manifest.StorageRef,
		events.TypeSnapshotPublished, events.SnapshotPublished{
			JobID: jobID, Attempt: attempt, Manifest: manifest,
		},
	)
}

// CancelJob requests cancellation. A running worker observes the projection and
// records the terminal cancellation; a queued job may be finished immediately.
func (h *Handler) CancelJob(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("modeling: cancellation reason is required")
	}
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !state.exists || state.state == "completed" || state.state == "cancelled" {
		return eventlog.Envelope{}, fmt.Errorf("modeling: job %q cannot be cancelled in state %s", jobID, state.state)
	}
	return h.appendUnique(ctx, id, events.TypeJobCancelRequested,
		events.JobTransition{JobID: jobID, Reason: reason},
		"modeling.job.cancel-request\x00"+jobID)
}

// PauseJob requests cooperative suspension of queued, failed, or active work.
func (h *Handler) PauseJob(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("modeling: pause reason is required")
	}
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !state.exists ||
		(state.state != "queued" && state.state != "running" && state.state != "failed") {
		return eventlog.Envelope{}, fmt.Errorf(
			"modeling: job %q cannot be paused in state %s", jobID, state.state,
		)
	}
	return h.appendUnique(ctx, id, events.TypeJobPauseRequested,
		events.JobTransition{JobID: jobID, Reason: strings.TrimSpace(reason)},
		"modeling.job.pause-request\x00"+jobID+"\x00"+fmt.Sprint(state.attempt))
}

// FinishPause records worker acknowledgement of a pause request.
func (h *Handler) FinishPause(
	ctx context.Context,
	id identity.Identity,
	jobID string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.state != "pausing" {
		return eventlog.Envelope{}, fmt.Errorf("modeling: job %q is not pausing", jobID)
	}
	return h.appendUnique(ctx, id, events.TypeJobPaused,
		events.JobTransition{JobID: jobID},
		"modeling.job.paused\x00"+jobID+"\x00"+fmt.Sprint(state.attempt))
}

// ResumeJob requeues paused work without changing its immutable request.
func (h *Handler) ResumeJob(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("modeling: resume reason is required")
	}
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.state != "paused" {
		return eventlog.Envelope{}, fmt.Errorf(
			"modeling: job %q cannot be resumed in state %s", jobID, state.state,
		)
	}
	return h.appendUnique(ctx, id, events.TypeJobResumed,
		events.JobTransition{JobID: jobID, Reason: strings.TrimSpace(reason)},
		"modeling.job.resume\x00"+jobID+"\x00"+fmt.Sprint(state.attempt))
}

// RetryJob explicitly requeues a failed job while preserving its request and
// attempt history.
func (h *Handler) RetryJob(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	reason string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("modeling: retry reason is required")
	}
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.state != "failed" {
		return eventlog.Envelope{}, fmt.Errorf(
			"modeling: job %q cannot be retried in state %s", jobID, state.state,
		)
	}
	if state.attempt >= defaultJobAttempts {
		return eventlog.Envelope{}, fmt.Errorf(
			"modeling: job %q exhausted %d attempts", jobID, defaultJobAttempts,
		)
	}
	return h.appendUnique(ctx, id, events.TypeJobRetryRequested,
		events.JobTransition{JobID: jobID, Reason: strings.TrimSpace(reason)},
		"modeling.job.retry\x00"+jobID+"\x00"+fmt.Sprint(state.attempt))
}

// FinishCancellation records worker acknowledgement.
func (h *Handler) FinishCancellation(
	ctx context.Context,
	id identity.Identity,
	jobID string,
) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	state, err := h.foldJob(ctx, id, jobID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.state != "cancelling" {
		return eventlog.Envelope{}, fmt.Errorf("modeling: job %q is not cancelling", jobID)
	}
	return h.appendUnique(ctx, id, events.TypeJobCancelled,
		events.JobTransition{JobID: jobID},
		"modeling.job.cancelled\x00"+jobID)
}
