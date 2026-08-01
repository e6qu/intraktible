// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	decisionmodels "github.com/e6qu/intraktible/decision-engine/models"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/consent"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/scheduler"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	artifactBlobCollection        = "modeling_artifact_blobs"
	materializationBlobCollection = "modeling_materialization_blobs"
	jobLease                      = 30 * time.Second
	workerPoll                    = 200 * time.Millisecond
	maxSnapshotBytes              = 64 << 20
	maxJobRuntime                 = 30 * time.Minute
	estimatedComputeUnitUSD       = 0.000001
)

var (
	errJobPauseRequested  = errors.New("modeling: job pause requested")
	errJobCancelRequested = errors.New("modeling: job cancellation requested")
)

type workerState struct {
	wg sync.WaitGroup
}

type snapshotBlob struct {
	Rows []sealedSubjectRow `json:"rows"`
}

type artifactContent struct {
	ModelSpec        json.RawMessage            `json:"model_spec"`
	TrainingReport   decisionmodels.TrainReport `json:"training_report"`
	EvaluationReport domain.BinaryEvaluation    `json:"evaluation_report"`
	Request          domain.TrainingRequest     `json:"request"`
	SnapshotHash     string                     `json:"snapshot_hash"`
}

type artifactBlob struct {
	Content   artifactContent `json:"content"`
	Hash      string          `json:"hash"`
	Signature string          `json:"signature"`
	PublicKey string          `json:"public_key"`
}

type materializationBlob struct {
	Rows []sealedSubjectRow `json:"rows"`
}

type sealedSubjectRow struct {
	Subject string `json:"subject"`
	Sealed  []byte `json:"sealed"`
}

var errSnapshotContentErased = errors.New(
	"modeling: snapshot content has been erased for at least one source subject",
)

// StartWorkers launches a bounded cross-tenant modeling worker pool.
func (s *Service) StartWorkers(ctx context.Context, count int) {
	if count < 1 {
		panic("modeling: worker count must be positive")
	}
	for range count {
		worker := "modeling-" + newWorkerID()
		s.workers.wg.Add(1)
		go func() {
			defer s.workers.wg.Done()
			s.runWorker(ctx, worker)
		}()
	}
}

// DrainWorkers waits until cancellation stops every modeling worker.
func (s *Service) DrainWorkers() {
	s.workers.wg.Wait()
}

func (s *Service) runWorker(ctx context.Context, worker string) {
	scheduler.RunWorker(ctx, workerPoll, "modeling_worker", "modeling", worker, s.Tick)
}

// Tick claims and processes at most one modeling job.
func (s *Service) Tick(ctx context.Context, worker string) (bool, error) {
	records, err := s.store.List(ctx, modelprojection.CollectionJobs, "")
	if err != nil {
		return false, err
	}
	for _, record := range records {
		var view modelprojection.JobView
		if err := json.Unmarshal(record.Doc, &view); err != nil {
			return false, fmt.Errorf("modeling: decode job projection %q: %w", record.Key, err)
		}
		id, err := identity.New(view.Org, view.Workspace, "modeling-worker:"+worker)
		if err != nil {
			return false, err
		}
		if view.State == "cancelling" {
			_, err := s.cmd.FinishCancellation(ctx, id, view.JobID)
			if errors.Is(err, eventlog.ErrConflict) {
				continue
			}
			return true, err
		}
		if view.State == "pausing" {
			_, err := s.cmd.FinishPause(ctx, id, view.JobID)
			if errors.Is(err, eventlog.ErrConflict) {
				continue
			}
			return true, err
		}
		claimable := view.State == "queued" ||
			(view.State == "failed" && view.Retryable) ||
			(view.State == "running" && !view.LeaseUntil.After(s.now()))
		if !claimable {
			continue
		}
		attempt, _, err := s.cmd.ClaimJob(ctx, id, view.JobID, worker, jobLease)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		if err != nil {
			// A projection may briefly lag a competing worker's claim.
			if stringsContainsStateConflict(err.Error()) {
				continue
			}
			return false, err
		}
		return true, s.processJob(ctx, id, view, attempt, worker)
	}
	return false, nil
}

func stringsContainsStateConflict(message string) bool {
	return strings.Contains(message, "not claimable") || strings.Contains(message, "exhausted")
}

func (s *Service) processJob(
	ctx context.Context,
	id identity.Identity,
	view modelprojection.JobView,
	attempt int,
	worker string,
) error {
	runCtx, cancel := context.WithTimeout(ctx, maxJobRuntime)
	defer cancel()
	heartbeatErrors := make(chan error, 1)
	go s.watchJob(runCtx, cancel, id, view.JobID, attempt, worker, heartbeatErrors)

	processErr := s.reportProgress(
		runCtx, id, view.JobID, attempt, worker, 0, "validating", 0,
	)
	if processErr == nil {
		switch view.Kind {
		case "snapshot":
			processErr = s.buildSnapshot(runCtx, id, view, attempt, worker)
		case "training":
			processErr = s.trainModel(runCtx, id, view, attempt, worker)
		case "evaluation":
			processErr = s.evaluateModel(runCtx, id, view, attempt, worker)
		case "backfill":
			processErr = s.buildBackfill(runCtx, id, view, attempt, worker)
		default:
			processErr = fmt.Errorf("modeling: unsupported job kind %q", view.Kind)
		}
	}
	cancel()
	if heartbeatErr := <-heartbeatErrors; heartbeatErr != nil {
		if processErr == nil ||
			errors.Is(heartbeatErr, errJobPauseRequested) ||
			errors.Is(heartbeatErr, errJobCancelRequested) {
			processErr = heartbeatErr
		}
	}
	if errors.Is(processErr, errJobPauseRequested) {
		_, err := s.cmd.FinishPause(
			context.WithoutCancel(ctx), id, view.JobID,
		)
		return err
	}
	if errors.Is(processErr, errJobCancelRequested) {
		_, err := s.cmd.FinishCancellation(
			context.WithoutCancel(ctx), id, view.JobID,
		)
		return err
	}
	if processErr == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	retryable := !errors.Is(processErr, context.DeadlineExceeded) &&
		!errors.Is(processErr, errSnapshotContentErased)
	_, appendErr := s.cmd.FailJob(
		context.WithoutCancel(ctx), id, view.JobID, attempt, worker, processErr, retryable,
	)
	return appendErr
}

func (s *Service) reportProgress(
	ctx context.Context,
	id identity.Identity,
	jobID string,
	attempt int,
	worker string,
	completed int64,
	phase string,
	computeUnits int64,
) error {
	_, err := s.cmd.ReportJobProgress(
		ctx, id, jobID, attempt, worker, completed, 4, phase,
		computeUnits, float64(computeUnits)*estimatedComputeUnitUSD,
	)
	return err
}

func (s *Service) evaluateModel(
	ctx context.Context,
	id identity.Identity,
	job modelprojection.JobView,
	attempt int,
	worker string,
) error {
	if job.EvaluationRequest.ArtifactID != job.ArtifactID ||
		job.EvaluationRequest.SnapshotID != job.SnapshotID {
		return errors.New("modeling: evaluation job lineage is inconsistent")
	}
	if err := s.VerifyArtifact(ctx, id, job.ArtifactID); err != nil {
		return err
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 1, "artifact verified", 0,
	); err != nil {
		return err
	}
	artifact, found, err := modelprojection.ReadArtifact(ctx, s.store, id, job.ArtifactID)
	if err != nil {
		return err
	}
	if !found || artifact.Lineage.ArtifactHash != job.ArtifactHash ||
		artifact.ModelName != job.ModelName {
		return errors.New("modeling: evaluation artifact lineage changed")
	}
	blob, found, err := store.GetDoc[artifactBlob](
		ctx, s.store, artifactBlobCollection,
		store.Key(id.Org, id.Workspace, artifact.Publication.StorageRef),
	)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("modeling: evaluation artifact bytes are unavailable")
	}
	spec, err := decisionmodels.ParseSpec(blob.Content.ModelSpec)
	if err != nil {
		return err
	}
	rows, snapshot, err := s.ReadSnapshotRows(ctx, id, job.SnapshotID)
	if err != nil {
		return err
	}
	if snapshot.RowsHash != job.SnapshotManifest.RowsHash {
		return errors.New("modeling: evaluation snapshot hash changed")
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 2, "snapshot loaded", int64(len(rows)),
	); err != nil {
		return err
	}
	dataset, found, err := modelprojection.ReadDataset(
		ctx, s.store, id, snapshot.DatasetName,
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("modeling: evaluation dataset %q is unavailable", snapshot.DatasetName)
	}
	var datasetSpec *domain.DatasetSpec
	for index := range dataset.Versions {
		if dataset.Versions[index].Version == snapshot.DatasetVersion {
			datasetSpec = &dataset.Versions[index].Spec
			break
		}
	}
	if datasetSpec == nil || datasetSpec.Label.Kind != domain.LabelBinary {
		return errors.New("modeling: independent binary evaluation requires a binary dataset")
	}
	scored, err := scoreHoldoutRows(datasetSpec, spec, rows)
	if err != nil {
		return err
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 3, "holdout scored", int64(len(rows)),
	); err != nil {
		return err
	}
	report, err := domain.EvaluateBinary(scored, job.EvaluationRequest.Options)
	if err != nil {
		return fmt.Errorf("modeling: independent evaluation: %w", err)
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return err
	}
	reportHash := sha256.Sum256(reportJSON)
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 4, "evaluation verified", int64(len(rows)),
	); err != nil {
		return err
	}
	_, err = s.cmd.PublishEvaluation(
		context.WithoutCancel(ctx), id, job.JobID, attempt, worker,
		domain.EvaluationManifest{
			EvaluationID: job.EvaluationID, ArtifactID: job.ArtifactID,
			ArtifactHash: job.ArtifactHash, ModelName: job.ModelName,
			SnapshotID: snapshot.SnapshotID, SnapshotHash: snapshot.RowsHash,
			Purpose: job.EvaluationRequest.Purpose, Report: report,
			ReportHash: hex.EncodeToString(reportHash[:]), EvaluatedAt: s.now(),
		},
	)
	return err
}

func (s *Service) buildBackfill(
	ctx context.Context,
	id identity.Identity,
	job modelprojection.JobView,
	attempt int,
	worker string,
) error {
	if s.contentSealer == nil {
		return errors.New("modeling: subject content sealer is required for backfills")
	}
	definitions, err := features.List(ctx, s.store, id, job.BackfillRequest.EntityType)
	if err != nil {
		return err
	}
	available := make(map[string]int, len(definitions))
	for _, definition := range definitions {
		available[definition.Name] = definition.Version
	}
	for name, pinned := range job.FeatureVersions {
		if available[name] != pinned {
			return fmt.Errorf(
				"modeling: backfill feature %q changed from version %d to %d",
				name, pinned, available[name],
			)
		}
	}
	entityViews, err := entities.ListEntitiesAt(
		ctx, s.store, id, job.BackfillRequest.EntityType,
		job.BackfillRequest.KnowledgeAt,
	)
	if err != nil {
		return err
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 1, "source cohort loaded", int64(len(entityViews)),
	); err != nil {
		return err
	}
	sort.Slice(entityViews, func(i, j int) bool {
		return entityViews[i].EntityID < entityViews[j].EntityID
	})
	rows := make([]domain.MaterializedFeatureRow, 0, len(entityViews))
	for _, entity := range entityViews {
		values, err := features.ComputeAt(
			ctx, s.store, id, entity.EntityType, entity.EntityID,
			job.BackfillRequest.AsOf, job.BackfillRequest.KnowledgeAt,
		)
		if err != nil {
			return err
		}
		all := make(map[string]float64, len(values))
		for _, value := range values {
			all[value.Name] = value.Value
		}
		selected := make(map[string]float64, len(job.BackfillRequest.Features))
		for _, name := range job.BackfillRequest.Features {
			value, ok := all[name]
			if !ok {
				return fmt.Errorf("modeling: backfill feature %q is unavailable", name)
			}
			selected[name] = value
		}
		rows = append(rows, domain.MaterializedFeatureRow{
			EntityID: entity.EntityID, Values: selected,
		})
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 2, "features materialized", int64(len(rows)),
	); err != nil {
		return err
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	if len(payload) > maxSnapshotBytes {
		return fmt.Errorf("modeling: backfill is %d bytes; maximum is %d", len(payload), maxSnapshotBytes)
	}
	hashBytes := sha256.Sum256(payload)
	rowsHash := hex.EncodeToString(hashBytes[:])
	storageRef := "backfill/" + job.BackfillID + "/" + rowsHash
	sealedRows, err := sealRows(
		ctx, s.contentSealer, id, job.BackfillRequest.EntityType, rows,
		func(row domain.MaterializedFeatureRow) string { return row.EntityID },
	)
	if err != nil {
		return err
	}
	if err := store.PutDoc(ctx, s.store, materializationBlobCollection,
		store.Key(id.Org, id.Workspace, storageRef), materializationBlob{Rows: sealedRows}); err != nil {
		return err
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 3, "content written", int64(len(rows)),
	); err != nil {
		return err
	}
	stored, found, err := store.GetDoc[materializationBlob](
		ctx, s.store, materializationBlobCollection,
		store.Key(id.Org, id.Workspace, storageRef),
	)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("modeling: backfill blob disappeared before publication")
	}
	openedRows, err := openRows[domain.MaterializedFeatureRow](
		ctx, s.contentSealer, id, stored.Rows,
	)
	if err != nil {
		return err
	}
	storedPayload, err := json.Marshal(openedRows)
	if err != nil {
		return err
	}
	storedHash := sha256.Sum256(storedPayload)
	if hex.EncodeToString(storedHash[:]) != rowsHash {
		return errors.New("modeling: backfill write verification failed")
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 4, "content verified", int64(len(rows)),
	); err != nil {
		return err
	}
	_, err = s.cmd.PublishBackfill(
		context.WithoutCancel(ctx), id, job.JobID, attempt, worker,
		domain.BackfillManifest{
			BackfillID: job.BackfillID, EntityType: job.BackfillRequest.EntityType,
			Features: job.BackfillRequest.Features, FeatureVersions: job.FeatureVersions,
			AsOf: job.BackfillRequest.AsOf, KnowledgeAt: job.BackfillRequest.KnowledgeAt,
			RowCount: len(rows), SizeBytes: int64(len(payload)),
			RowsHash: rowsHash, StorageRef: storageRef,
			PublishedAt: s.now(),
		},
	)
	return err
}

func (s *Service) trainModel(
	ctx context.Context,
	id identity.Identity,
	job modelprojection.JobView,
	attempt int,
	worker string,
) error {
	if s.models == nil {
		return errors.New("modeling: training requires the decision-engine model registry")
	}
	if job.TrainingRequest.Runtime != domain.RuntimeLogisticV1 {
		return fmt.Errorf("modeling: unsupported training runtime %q", job.TrainingRequest.Runtime)
	}
	if job.SnapshotManifest.SnapshotID != job.TrainingRequest.SnapshotID {
		return errors.New("modeling: training job snapshot lineage is inconsistent")
	}
	rows, manifest, err := s.ReadSnapshotRows(ctx, id, job.TrainingRequest.SnapshotID)
	if err != nil {
		return err
	}
	if manifest.RowsHash != job.SnapshotManifest.RowsHash {
		return errors.New("modeling: training snapshot hash changed")
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 1, "snapshot loaded", int64(len(rows)),
	); err != nil {
		return err
	}
	if job.SnapshotManifest.DatasetName == "" {
		return errors.New("modeling: training dataset lineage is incomplete")
	}
	dataset, found, err := modelprojection.ReadDataset(
		ctx, s.store, id, manifest.DatasetName,
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("modeling: dataset %q is unavailable", manifest.DatasetName)
	}
	var datasetSpec *domain.DatasetSpec
	for index := range dataset.Versions {
		if dataset.Versions[index].Version == manifest.DatasetVersion {
			datasetSpec = &dataset.Versions[index].Spec
			break
		}
	}
	if datasetSpec == nil {
		return fmt.Errorf(
			"modeling: dataset %q version %d is unavailable",
			manifest.DatasetName, manifest.DatasetVersion,
		)
	}
	if datasetSpec.Label.Kind != domain.LabelBinary {
		return fmt.Errorf(
			"modeling: runtime %s requires a binary label, got %s",
			job.TrainingRequest.Runtime, datasetSpec.Label.Kind,
		)
	}
	trainingRows := make([]decisionmodels.Row, 0, len(rows))
	for _, row := range rows {
		if row.Partition != "train" || !row.LabelPresent {
			continue
		}
		label, err := domain.NumericLabel(datasetSpec.Label, row.Label)
		if err != nil {
			return err
		}
		trainingRows = append(trainingRows, decisionmodels.Row{
			Features: row.Features, Label: label,
		})
	}
	parameters := job.TrainingRequest.Parameters
	spec, report, err := decisionmodels.FitLogistic(trainingRows, decisionmodels.TrainOptions{
		Features: datasetSpec.Features, Iterations: parameters.Iterations,
		LearningRate: parameters.LearningRate, L2: parameters.L2, Folds: parameters.Folds,
	})
	if err != nil {
		return err
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 2, "model fitted", int64(len(rows)),
	); err != nil {
		return err
	}
	evaluationRows, err := scoreHoldoutRows(datasetSpec, spec, rows)
	if err != nil {
		return err
	}
	evaluation, err := domain.EvaluateBinary(evaluationRows, domain.EvaluationOptions{})
	if err != nil {
		return fmt.Errorf("modeling: holdout evaluation: %w", err)
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 3, "holdout evaluated", int64(len(rows)),
	); err != nil {
		return err
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("modeling: marshal model spec: %w", err)
	}
	specHash := sha256.Sum256(specJSON)
	parametersJSON, err := json.Marshal(parameters)
	if err != nil {
		return err
	}
	parametersHash := sha256.Sum256(parametersJSON)
	artifactRequest := job.TrainingRequest
	artifactRequest.IdempotencyKey = ""
	content := artifactContent{
		ModelSpec: specJSON, TrainingReport: report, EvaluationReport: evaluation,
		Request: artifactRequest, SnapshotHash: manifest.RowsHash,
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("modeling: marshal artifact: %w", err)
	}
	artifactHashBytes := sha256.Sum256(contentJSON)
	artifactHash := hex.EncodeToString(artifactHashBytes[:])
	signatureBytes := ed25519.Sign(s.privateKey, artifactHashBytes[:])
	signature := base64.RawURLEncoding.EncodeToString(signatureBytes)
	publicKey := base64.RawURLEncoding.EncodeToString(s.publicKey)
	storageRef := "artifact/" + job.ArtifactID + "/" + artifactHash
	blob := artifactBlob{
		Content: content, Hash: artifactHash, Signature: signature, PublicKey: publicKey,
	}
	if err := store.PutDoc(ctx, s.store, artifactBlobCollection,
		store.Key(id.Org, id.Workspace, storageRef), blob); err != nil {
		return err
	}
	stored, found, err := store.GetDoc[artifactBlob](
		ctx, s.store, artifactBlobCollection, store.Key(id.Org, id.Workspace, storageRef),
	)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("modeling: artifact disappeared before registration")
	}
	if err := verifyArtifactBlob(stored); err != nil {
		return err
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 4, "artifact signed and verified", int64(len(rows)),
	); err != nil {
		return err
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return err
	}
	evaluationJSON, err := json.Marshal(evaluation)
	if err != nil {
		return err
	}
	evaluationHash := sha256.Sum256(evaluationJSON)
	lineage := decisionevents.ModelLineage{
		TrainingJobID: job.JobID, ArtifactID: job.ArtifactID, ArtifactHash: artifactHash,
		SnapshotID: manifest.SnapshotID, SnapshotHash: manifest.RowsHash,
		DatasetName: manifest.DatasetName, DatasetVersion: manifest.DatasetVersion,
		Runtime: string(job.TrainingRequest.Runtime), CodeRevision: job.TrainingRequest.CodeRevision,
		ParametersHash: hex.EncodeToString(parametersHash[:]), Seed: job.TrainingRequest.Seed,
	}
	publication := decisionevents.TrainingPublication{
		Attempt: attempt, Signature: signature, PublicKey: publicKey,
		StorageRef: storageRef, ModelSpecHash: hex.EncodeToString(specHash[:]),
		TrainingReport: reportJSON, EvaluationReport: evaluationJSON,
		EvaluationHash: hex.EncodeToString(evaluationHash[:]), PublishedAt: s.now(),
	}
	_, err = s.models.DefineModelWithLineage(
		context.WithoutCancel(ctx), id, job.TrainingRequest.ModelName,
		specJSON, lineage, publication,
	)
	if errors.Is(err, eventlog.ErrConflict) {
		return nil
	}
	return err
}

func verifyArtifactBlob(blob artifactBlob) error {
	contentJSON, err := json.Marshal(blob.Content)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(contentJSON)
	if hex.EncodeToString(hash[:]) != blob.Hash {
		return fmt.Errorf("modeling: artifact content hash mismatch")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(blob.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("modeling: artifact public key is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(blob.Signature)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), hash[:], signature) {
		return errors.New("modeling: artifact signature is invalid")
	}
	return nil
}

func (s *Service) watchJob(
	ctx context.Context,
	cancel context.CancelFunc,
	id identity.Identity,
	jobID string,
	attempt int,
	worker string,
	out chan<- error,
) {
	defer close(out)
	controlTicker := time.NewTicker(workerPoll)
	heartbeatTicker := time.NewTicker(jobLease / 3)
	defer controlTicker.Stop()
	defer heartbeatTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-controlTicker.C:
			job, found, err := modelprojection.ReadJob(ctx, s.store, id, jobID)
			if err != nil {
				cancel()
				out <- err
				return
			}
			if !found {
				cancel()
				out <- fmt.Errorf("modeling: active job %q disappeared", jobID)
				return
			}
			switch job.State {
			case "pausing":
				// Cancel the work BEFORE reporting the control error, so the worker
				// loop and any observer never sees the error before the work context
				// is cancelled.
				cancel()
				out <- errJobPauseRequested
				return
			case "cancelling":
				cancel()
				out <- errJobCancelRequested
				return
			}
		case <-heartbeatTicker.C:
			if _, err := s.cmd.HeartbeatJob(
				ctx, id, jobID, attempt, worker, jobLease,
			); err != nil {
				out <- err
				return
			}
		}
	}
}

func (s *Service) buildSnapshot(
	ctx context.Context,
	id identity.Identity,
	job modelprojection.JobView,
	attempt int,
	worker string,
) error {
	if s.contentSealer == nil {
		return errors.New("modeling: subject content sealer is required for snapshots")
	}
	definitions, err := features.List(ctx, s.store, id, job.DatasetSpec.EntityType)
	if err != nil {
		return err
	}
	currentVersions := make(map[string]int, len(definitions))
	for _, definition := range definitions {
		currentVersions[definition.Name] = definition.Version
	}
	for name, pinned := range job.FeatureVersions {
		if currentVersions[name] != pinned {
			return fmt.Errorf(
				"modeling: feature %q changed from pinned version %d to %d before snapshot execution",
				name, pinned, currentVersions[name],
			)
		}
	}
	entityViews, err := entities.ListEntitiesAt(
		ctx, s.store, id, job.DatasetSpec.EntityType, job.KnowledgeAt,
	)
	if err != nil {
		return err
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 1, "source cohort loaded", int64(len(entityViews)),
	); err != nil {
		return err
	}
	candidateCount := len(entityViews)
	populationExcluded := 0
	consentExcluded := 0
	eligibleEntities := make([]entities.EntityView, 0, len(entityViews))
	for _, entity := range entityViews {
		eligible, _, err := domain.PopulationDecision(
			entity.Attributes,
			job.DatasetSpec.InclusionRules,
			job.DatasetSpec.ExclusionRules,
		)
		if err != nil {
			return fmt.Errorf("modeling: entity %q population rules: %w", entity.EntityID, err)
		}
		if !eligible {
			populationExcluded++
			continue
		}
		if job.DatasetSpec.ConsentRequirement.Mode == domain.ConsentActive {
			authorized, err := consent.HasAt(
				ctx, s.store, id, entity.EntityType+"/"+entity.EntityID,
				job.DatasetSpec.ConsentRequirement.Purpose, job.KnowledgeAt,
			)
			if err != nil {
				return err
			}
			if !authorized {
				consentExcluded++
				continue
			}
		}
		eligibleEntities = append(eligibleEntities, entity)
	}
	entityViews = eligibleEntities
	if len(entityViews) == 0 {
		return fmt.Errorf(
			"modeling: snapshot population is empty after %d rule exclusions and %d consent exclusions",
			populationExcluded, consentExcluded,
		)
	}
	sort.Slice(entityViews, func(i, j int) bool {
		return entityViews[i].EntityID < entityViews[j].EntityID
	})
	rows := make([]domain.DatasetRow, 0, len(entityViews))
	schemaSets := map[string]map[int]bool{}
	for _, entity := range entityViews {
		values, err := features.ComputeAt(
			ctx, s.store, id, entity.EntityType, entity.EntityID,
			job.ObservationAt, job.KnowledgeAt,
		)
		if err != nil {
			return err
		}
		allFeatures := make(map[string]float64, len(values))
		for _, value := range values {
			allFeatures[value.Name] = value.Value
		}
		selected := make(map[string]float64, len(job.DatasetSpec.Features))
		for _, name := range job.DatasetSpec.Features {
			value, ok := allFeatures[name]
			if !ok {
				return fmt.Errorf("modeling: feature %q is absent for entity %q", name, entity.EntityID)
			}
			selected[name] = value
		}
		label, present, labelSchema, err := s.resolveLabel(ctx, id, job, entity.EntityID)
		if err != nil {
			return err
		}
		segments, err := resolveSegments(entity.Attributes, job.DatasetSpec.SegmentFields)
		if err != nil {
			return fmt.Errorf("modeling: entity %q segments: %w", entity.EntityID, err)
		}
		rows = append(rows, domain.DatasetRow{
			EntityID: entity.EntityID, Features: selected, Label: label,
			LabelPresent: present, Censored: !present, Segments: segments,
			Partition:     domain.PartitionFor(entity.EntityID, job.DatasetSpec.Partitions),
			ObservationAt: job.ObservationAt, KnowledgeAt: job.KnowledgeAt,
		})
		if entity.SchemaVersion > 0 {
			addSchemaVersion(schemaSets, "entity/"+entity.EntityType, entity.SchemaVersion)
		}
		if labelSchema > 0 {
			addSchemaVersion(schemaSets,
				"event/"+entity.EntityType+"/"+job.DatasetSpec.Label.EventName, labelSchema)
		}
	}
	if len(rows) == 0 {
		return errors.New("modeling: snapshot source cohort is empty")
	}
	if job.ExpiresAt == nil {
		return errors.New("modeling: snapshot job retention expiry is missing")
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 2, "features and labels materialized",
		int64(candidateCount),
	); err != nil {
		return err
	}
	rowsHash, payload, err := domain.HashRows(rows)
	if err != nil {
		return err
	}
	if len(payload) > maxSnapshotBytes {
		return fmt.Errorf("modeling: snapshot is %d bytes; maximum is %d", len(payload), maxSnapshotBytes)
	}
	storageRef := "snapshot/" + job.SnapshotID + "/" + rowsHash
	sealedRows, err := sealRows(
		ctx, s.contentSealer, id, job.DatasetSpec.EntityType, rows,
		func(row domain.DatasetRow) string { return row.EntityID },
	)
	if err != nil {
		return err
	}
	blob := snapshotBlob{Rows: sealedRows}
	if err := store.PutDoc(ctx, s.store, modelprojection.CollectionSnapshotBlobs,
		store.Key(id.Org, id.Workspace, storageRef), blob); err != nil {
		return err
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 3, "content written", int64(candidateCount),
	); err != nil {
		return err
	}
	stored, found, err := store.GetDoc[snapshotBlob](
		ctx, s.store, modelprojection.CollectionSnapshotBlobs,
		store.Key(id.Org, id.Workspace, storageRef),
	)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("modeling: snapshot blob disappeared before publication")
	}
	openedRows, err := openRows[domain.DatasetRow](
		ctx, s.contentSealer, id, stored.Rows,
	)
	if err != nil {
		return err
	}
	verifiedHash, _, err := domain.HashRows(openedRows)
	if err != nil {
		return err
	}
	if verifiedHash != rowsHash {
		return fmt.Errorf("modeling: snapshot write verification hash %s, want %s", verifiedHash, rowsHash)
	}
	if err := s.reportProgress(
		ctx, id, job.JobID, attempt, worker, 4, "content verified", int64(candidateCount),
	); err != nil {
		return err
	}
	partitionCounts := map[string]int{"train": 0, "validation": 0, "test": 0}
	labelled := 0
	for _, row := range rows {
		partitionCounts[row.Partition]++
		if row.LabelPresent {
			labelled++
		}
	}
	qualityFindings, err := qualityFindingCount(
		ctx, s.store, id, job.DatasetSpec.EntityType, rows, job.KnowledgeAt,
	)
	if err != nil {
		return err
	}
	manifest := domain.SnapshotManifest{
		SnapshotID: job.SnapshotID, DatasetName: job.DatasetName,
		DatasetVersion: job.DatasetVersion, DatasetHash: job.DatasetHash,
		RowsHash: rowsHash, RowCount: len(rows), LabelledCount: labelled,
		CensoredCount: len(rows) - labelled, PartitionCounts: partitionCounts,
		CandidateCount:          candidateCount,
		PopulationExcludedCount: populationExcluded,
		ConsentExcludedCount:    consentExcluded,
		QualityFindingCount:     qualityFindings,
		FeatureCompleteness:     1,
		FeatureVersions:         job.FeatureVersions, SchemaVersions: flattenSchemaVersions(schemaSets),
		ObservationAt: job.ObservationAt, KnowledgeAt: job.KnowledgeAt,
		StorageRef: storageRef, Purpose: job.DatasetSpec.Purpose,
		ExpiresAt: *job.ExpiresAt, PublishedAt: s.now(),
	}
	_, err = s.cmd.PublishSnapshot(
		context.WithoutCancel(ctx), id, job.JobID, attempt, worker, manifest,
	)
	return err
}

func qualityFindingCount(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
	entityType string,
	rows []domain.DatasetRow,
	knowledgeAt time.Time,
) (int, error) {
	included := make(map[string]bool, len(rows))
	for _, row := range rows {
		included[row.EntityID] = true
	}
	observations, err := modelprojection.ListQualityObservations(ctx, st, id)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, observation := range observations {
		if observation.Ref.EntityType == entityType &&
			included[observation.EntityID] &&
			!observation.ObservedAt.After(knowledgeAt) {
			count += len(observation.Violations)
		}
	}
	return count, nil
}

func (s *Service) resolveLabel(
	ctx context.Context,
	id identity.Identity,
	job modelprojection.JobView,
	entityID string,
) (json.RawMessage, bool, int, error) {
	history, err := entities.ListEvents(
		ctx, s.store, id, job.DatasetSpec.EntityType, entityID,
	)
	if err != nil {
		return nil, false, 0, err
	}
	horizon := job.ObservationAt.Add(
		time.Duration(job.DatasetSpec.Label.HorizonHours) * time.Hour,
	)
	var found json.RawMessage
	schemaVersion := 0
	for _, event := range history {
		if event.EventName != job.DatasetSpec.Label.EventName ||
			!event.OccurredAt.After(job.ObservationAt) || event.OccurredAt.After(horizon) ||
			event.ReceivedAt.After(job.KnowledgeAt) ||
			event.SupersededAt != nil && !event.SupersededAt.After(job.KnowledgeAt) ||
			event.RetractedAt != nil && !event.RetractedAt.After(job.KnowledgeAt) {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(event.Data, &object); err != nil {
			return nil, false, 0, fmt.Errorf("modeling: label event %q data: %w", event.EventID, err)
		}
		value, ok := object[job.DatasetSpec.Label.Field]
		if !ok || string(value) == "null" {
			continue
		}
		if found != nil {
			return nil, false, 0, fmt.Errorf(
				"modeling: entity %q has multiple effective label events in the observation horizon",
				entityID,
			)
		}
		found = append(json.RawMessage(nil), value...)
		schemaVersion = event.SchemaVersion
	}
	return found, found != nil, schemaVersion, nil
}

// scoreHoldoutRows labels and scores the non-train rows of a snapshot with
// the given model spec.
func scoreHoldoutRows(
	datasetSpec *domain.DatasetSpec,
	spec decisionmodels.Spec,
	rows []domain.DatasetRow,
) ([]domain.ScoredRow, error) {
	scored := make([]domain.ScoredRow, 0, len(rows))
	for _, row := range rows {
		if row.Partition == "train" || !row.LabelPresent {
			continue
		}
		label, err := domain.NumericLabel(datasetSpec.Label, row.Label)
		if err != nil {
			return nil, err
		}
		inputs := make(map[string]any, len(row.Features))
		for name, value := range row.Features {
			inputs[name] = value
		}
		prediction, err := decisionmodels.Evaluate(spec, inputs)
		if err != nil {
			return nil, err
		}
		score := prediction.Score
		if prediction.Probability != nil {
			score = *prediction.Probability
		}
		scored = append(scored, domain.ScoredRow{
			EntityID: row.EntityID, Score: score, Label: label,
			Partition: row.Partition, Segments: row.Segments,
			ObservationAt: row.ObservationAt,
		})
	}
	return scored, nil
}

func resolveSegments(raw json.RawMessage, fields []string) (map[string]string, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	segments := make(map[string]string, len(fields))
	for _, field := range fields {
		value, ok := object[field]
		if !ok || string(value) == "null" {
			segments[field] = "[missing]"
			continue
		}
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			return nil, fmt.Errorf("decode segment %q: %w", field, err)
		}
		if text, ok := decoded.(string); ok {
			segments[field] = text
		} else {
			segments[field] = string(value)
		}
	}
	return segments, nil
}

func addSchemaVersion(sets map[string]map[int]bool, source string, version int) {
	if sets[source] == nil {
		sets[source] = map[int]bool{}
	}
	sets[source][version] = true
}

func flattenSchemaVersions(sets map[string]map[int]bool) map[string][]int {
	out := make(map[string][]int, len(sets))
	for source, versions := range sets {
		for version := range versions {
			out[source] = append(out[source], version)
		}
		sort.Ints(out[source])
	}
	return out
}

func newWorkerID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("modeling: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(value[:])
}

// ReadSnapshotRows loads and verifies one published operational snapshot.
func (s *Service) ReadSnapshotRows(
	ctx context.Context,
	id identity.Identity,
	snapshotID string,
) ([]domain.DatasetRow, domain.SnapshotManifest, error) {
	view, found, err := modelprojection.ReadSnapshot(ctx, s.store, id, snapshotID)
	if err != nil {
		return nil, domain.SnapshotManifest{}, err
	}
	if !found {
		return nil, domain.SnapshotManifest{}, fmt.Errorf("modeling: unknown snapshot %q", snapshotID)
	}
	if view.State == "expired" || !view.Manifest.ExpiresAt.After(s.now()) {
		return nil, domain.SnapshotManifest{}, fmt.Errorf("modeling: snapshot %q has expired", snapshotID)
	}
	blob, found, err := store.GetDoc[snapshotBlob](
		ctx, s.store, modelprojection.CollectionSnapshotBlobs,
		store.Key(id.Org, id.Workspace, view.Manifest.StorageRef),
	)
	if err != nil {
		return nil, domain.SnapshotManifest{}, err
	}
	if !found {
		return nil, domain.SnapshotManifest{}, fmt.Errorf(
			"modeling: snapshot %q blob is unavailable", snapshotID,
		)
	}
	if s.contentSealer == nil {
		return nil, domain.SnapshotManifest{}, errors.New(
			"modeling: subject content sealer is required to read snapshot rows",
		)
	}
	rows, err := openRows[domain.DatasetRow](ctx, s.contentSealer, id, blob.Rows)
	if err != nil {
		if errors.Is(err, erasure.ErrErased) {
			return nil, domain.SnapshotManifest{}, errSnapshotContentErased
		}
		return nil, domain.SnapshotManifest{}, err
	}
	hash, _, err := domain.HashRows(rows)
	if err != nil {
		return nil, domain.SnapshotManifest{}, err
	}
	if hash != view.Manifest.RowsHash {
		return nil, domain.SnapshotManifest{}, fmt.Errorf(
			"modeling: snapshot %q hash %s, want %s", snapshotID, hash, view.Manifest.RowsHash,
		)
	}
	return rows, view.Manifest, nil
}

func sealRows[T any](
	ctx context.Context,
	sealer ContentSealer,
	id identity.Identity,
	entityType string,
	rows []T,
	entityID func(T) string,
) ([]sealedSubjectRow, error) {
	sealed := make([]sealedSubjectRow, 0, len(rows))
	for _, row := range rows {
		subject := entityType + "/" + entityID(row)
		plain, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		ciphertext, err := sealer.Seal(ctx, id, subject, plain)
		if err != nil {
			return nil, fmt.Errorf("modeling: seal row for subject %q: %w", subject, err)
		}
		sealed = append(sealed, sealedSubjectRow{Subject: subject, Sealed: ciphertext})
	}
	return sealed, nil
}

func openRows[T any](
	ctx context.Context,
	sealer ContentSealer,
	id identity.Identity,
	rows []sealedSubjectRow,
) ([]T, error) {
	opened := make([]T, 0, len(rows))
	for _, row := range rows {
		plain, err := sealer.Open(ctx, id, row.Subject, row.Sealed)
		if err != nil {
			return nil, fmt.Errorf("modeling: open row for subject %q: %w", row.Subject, err)
		}
		var decoded T
		if err := json.Unmarshal(plain, &decoded); err != nil {
			return nil, fmt.Errorf("modeling: decode row for subject %q: %w", row.Subject, err)
		}
		opened = append(opened, decoded)
	}
	return opened, nil
}

// VerifyArtifact re-reads the operational bytes, content hash, signature, and
// immutable registry manifest.
func (s *Service) VerifyArtifact(
	ctx context.Context,
	id identity.Identity,
	artifactID string,
) error {
	view, found, err := modelprojection.ReadArtifact(ctx, s.store, id, artifactID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("modeling: unknown artifact %q", artifactID)
	}
	if view.Origin == domain.ArtifactExternal {
		if view.External == nil {
			return fmt.Errorf(
				"modeling: external artifact %q registration is missing", artifactID,
			)
		}
		if err := view.External.Validate(view.RegisteredAt); err != nil {
			return fmt.Errorf("modeling: external artifact %q: %w", artifactID, err)
		}
		if view.ArtifactHash != view.External.ArtifactHash {
			return fmt.Errorf("modeling: external artifact %q hash changed", artifactID)
		}
		return nil
	}
	blob, found, err := store.GetDoc[artifactBlob](
		ctx, s.store, artifactBlobCollection,
		store.Key(id.Org, id.Workspace, view.Publication.StorageRef),
	)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("modeling: artifact %q blob is unavailable", artifactID)
	}
	if err := verifyArtifactBlob(blob); err != nil {
		return err
	}
	if blob.Hash != view.Lineage.ArtifactHash ||
		blob.Signature != view.Publication.Signature ||
		blob.PublicKey != view.Publication.PublicKey {
		return fmt.Errorf("modeling: artifact %q manifest does not match stored bytes", artifactID)
	}
	return nil
}
