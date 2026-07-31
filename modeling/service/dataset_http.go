// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

func (s *Service) defineDataset(w http.ResponseWriter, r *http.Request) {
	var spec domain.DatasetSpec
	httpx.Emit(w, r, &spec, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.DefineDataset(r.Context(), id, spec)
	})
}

func (s *Service) listDatasets(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	datasets, err := modelprojection.ListDatasets(r.Context(), s.store, id)
	httpx.WriteList(w, "datasets", datasets, err)
}

func (s *Service) getDataset(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	dataset, found, err := modelprojection.ReadDataset(r.Context(), s.store, id, r.PathValue("name"))
	httpx.WriteOne(w, dataset, found, err, "dataset not found")
}

type snapshotRequest struct {
	ObservationAt  time.Time `json:"observation_at"`
	KnowledgeAt    time.Time `json:"knowledge_at"`
	IdempotencyKey string    `json:"idempotency_key"`
}

func (s *Service) requestSnapshot(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request snapshotRequest
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version <= 0 {
		httpx.Error(w, http.StatusBadRequest, errors.New("modeling: version must be a positive integer"))
		return
	}
	dataset, found, err := modelprojection.ReadDataset(r.Context(), s.store, id, r.PathValue("name"))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("dataset not found"))
		return
	}
	var spec *domain.DatasetSpec
	for index := range dataset.Versions {
		if dataset.Versions[index].Version == version {
			spec = &dataset.Versions[index].Spec
			break
		}
	}
	if spec == nil {
		httpx.Error(w, http.StatusNotFound, errors.New("dataset version not found"))
		return
	}
	definitions, err := features.List(r.Context(), s.store, id, spec.EntityType)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	available := make(map[string]int, len(definitions))
	for _, definition := range definitions {
		available[definition.Name] = definition.Version
	}
	pinned := make(map[string]int, len(spec.Features))
	for _, name := range spec.Features {
		if available[name] <= 0 {
			httpx.Error(w, http.StatusBadRequest, fmt.Errorf(
				"modeling: dataset feature %q is not defined for %q", name, spec.EntityType,
			))
			return
		}
		pinned[name] = available[name]
	}
	jobID, snapshotID, envelope, err := s.cmd.RequestSnapshot(
		r.Context(), id, domain.SnapshotRequest{
			DatasetName: r.PathValue("name"), Version: version,
			ObservationAt: request.ObservationAt, KnowledgeAt: request.KnowledgeAt,
			IdempotencyKey: request.IdempotencyKey,
		}, pinned,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"event_id": envelope.ID, "seq": envelope.Seq,
		"job_id": jobID, "snapshot_id": snapshotID,
	})
}

func (s *Service) listSnapshots(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	snapshots, err := modelprojection.ListSnapshots(r.Context(), s.store, id)
	httpx.WriteList(w, "snapshots", snapshots, err)
}

func (s *Service) getSnapshot(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	snapshot, found, err := modelprojection.ReadSnapshot(
		r.Context(), s.store, id, r.PathValue("snapshot_id"),
	)
	httpx.WriteOne(w, snapshot, found, err, "snapshot not found")
}

func (s *Service) getSnapshotRows(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	rows, manifest, err := s.ReadSnapshotRows(r.Context(), id, r.PathValue("snapshot_id"))
	if err != nil {
		if errors.Is(err, errSnapshotContentErased) {
			httpx.Error(w, http.StatusGone, err)
			return
		}
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"manifest": manifest, "rows": rows})
}

func (s *Service) listJobs(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	jobs, err := modelprojection.ListJobs(r.Context(), s.store, id)
	httpx.WriteList(w, "jobs", jobs, err)
}

func (s *Service) getJob(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	job, found, err := modelprojection.ReadJob(r.Context(), s.store, id, r.PathValue("job_id"))
	httpx.WriteOne(w, job, found, err, "job not found")
}

type cancelJobRequest struct {
	Reason string `json:"reason"`
}

func (s *Service) pauseJob(w http.ResponseWriter, r *http.Request) {
	var request cancelJobRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.PauseJob(r.Context(), id, r.PathValue("job_id"), request.Reason)
	})
}

func (s *Service) resumeJob(w http.ResponseWriter, r *http.Request) {
	var request cancelJobRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.ResumeJob(r.Context(), id, r.PathValue("job_id"), request.Reason)
	})
}

func (s *Service) retryJob(w http.ResponseWriter, r *http.Request) {
	var request cancelJobRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.RetryJob(r.Context(), id, r.PathValue("job_id"), request.Reason)
	})
}

func (s *Service) cancelJob(w http.ResponseWriter, r *http.Request) {
	var request cancelJobRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.CancelJob(r.Context(), id, r.PathValue("job_id"), request.Reason)
	})
}

func (s *Service) requestTraining(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		httpx.Error(w, http.StatusBadRequest, errors.New(
			"modeling: training requires the decision-engine module",
		))
		return
	}
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request domain.TrainingRequest
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	snapshot, found, err := modelprojection.ReadSnapshot(
		r.Context(), s.store, id, request.SnapshotID,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("snapshot not found"))
		return
	}
	if !snapshot.Manifest.ExpiresAt.After(s.now()) {
		httpx.Error(w, http.StatusBadRequest, errors.New("modeling: snapshot has expired"))
		return
	}
	jobID, artifactID, envelope, err := s.cmd.RequestTraining(
		r.Context(), id, request, snapshot.Manifest,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"event_id": envelope.ID, "seq": envelope.Seq,
		"job_id": jobID, "artifact_id": artifactID,
	})
}

func (s *Service) listArtifacts(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	artifacts, err := modelprojection.ListArtifacts(r.Context(), s.store, id)
	httpx.WriteList(w, "artifacts", artifacts, err)
}

func (s *Service) registerArtifact(w http.ResponseWriter, r *http.Request) {
	var registration domain.ArtifactRegistration
	httpx.Emit(w, r, &registration, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.RegisterExternalArtifact(r.Context(), id, registration)
	})
}

type artifactStageRequest struct {
	Stage  domain.ArtifactStage `json:"stage"`
	Reason string               `json:"reason"`
}

func (s *Service) changeArtifactStage(w http.ResponseWriter, r *http.Request) {
	var request artifactStageRequest
	httpx.Emit(w, r, &request, func(id identity.Identity) (eventlog.Envelope, error) {
		return s.cmd.ChangeArtifactStage(
			r.Context(), id, r.PathValue("artifact_id"), request.Stage, request.Reason,
		)
	})
}

func (s *Service) requestEvaluation(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request domain.EvaluationRequest
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	artifact, found, err := modelprojection.ReadArtifact(
		r.Context(), s.store, id, request.ArtifactID,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}
	if artifact.Origin != domain.ArtifactPlatformTrained {
		httpx.Error(w, http.StatusBadRequest, errors.New(
			"modeling: external artifacts require a configured runtime adapter before evaluation",
		))
		return
	}
	snapshot, found, err := modelprojection.ReadSnapshot(
		r.Context(), s.store, id, request.SnapshotID,
	)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, errors.New("snapshot not found"))
		return
	}
	if snapshot.State == "expired" || !snapshot.Manifest.ExpiresAt.After(s.now()) {
		httpx.Error(w, http.StatusBadRequest, errors.New("modeling: snapshot has expired"))
		return
	}
	jobID, evaluationID, envelope, err := s.cmd.RequestEvaluation(
		r.Context(), id, request, artifact.ArtifactID,
		artifact.ArtifactHash, artifact.ModelName, snapshot.Manifest,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"event_id": envelope.ID, "seq": envelope.Seq,
		"job_id": jobID, "evaluation_id": evaluationID,
	})
}

func (s *Service) listEvaluations(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	evaluations, err := modelprojection.ListEvaluations(r.Context(), s.store, id)
	httpx.WriteList(w, "evaluations", evaluations, err)
}

func (s *Service) getEvaluation(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	evaluation, found, err := modelprojection.ReadEvaluation(
		r.Context(), s.store, id, r.PathValue("evaluation_id"),
	)
	httpx.WriteOne(w, evaluation, found, err, "evaluation not found")
}

func (s *Service) getArtifact(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	artifact, found, err := modelprojection.ReadArtifact(
		r.Context(), s.store, id, r.PathValue("artifact_id"),
	)
	httpx.WriteOne(w, artifact, found, err, "artifact not found")
}

func (s *Service) verifyArtifact(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	if err := s.VerifyArtifact(r.Context(), id, r.PathValue("artifact_id")); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"valid": true})
}

func (s *Service) requestBackfill(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	var request domain.BackfillRequest
	if err := httpx.DecodeJSON(r, &request); err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	definitions, err := features.List(r.Context(), s.store, id, request.EntityType)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	available := make(map[string]int, len(definitions))
	for _, definition := range definitions {
		available[definition.Name] = definition.Version
	}
	pinned := make(map[string]int, len(request.Features))
	for _, name := range request.Features {
		if available[name] <= 0 {
			httpx.Error(w, http.StatusBadRequest, fmt.Errorf(
				"modeling: backfill feature %q is not defined for %q", name, request.EntityType,
			))
			return
		}
		pinned[name] = available[name]
	}
	jobID, backfillID, envelope, err := s.cmd.RequestBackfill(
		r.Context(), id, request, pinned,
	)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{
		"event_id": envelope.ID, "seq": envelope.Seq,
		"job_id": jobID, "backfill_id": backfillID,
	})
}

func (s *Service) listMaterializations(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	items, err := modelprojection.ListMaterializations(r.Context(), s.store, id)
	httpx.WriteList(w, "materializations", items, err)
}

func (s *Service) getMaterialization(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	item, found, err := modelprojection.ReadMaterialization(
		r.Context(), s.store, id, r.PathValue("backfill_id"),
	)
	httpx.WriteOne(w, item, found, err, "materialization not found")
}
