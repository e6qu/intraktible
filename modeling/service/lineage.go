// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	decisionmodels "github.com/e6qu/intraktible/decision-engine/models"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
)

// ModelLineageView is the complete source-to-serving chain.
type ModelLineageView struct {
	Model           decisionmodels.ModelView     `json:"model"`
	Artifact        modelprojection.ArtifactView `json:"artifact"`
	Snapshot        modelprojection.SnapshotView `json:"snapshot"`
	Dataset         modelprojection.DatasetView  `json:"dataset"`
	TrainingJob     modelprojection.JobView      `json:"training_job"`
	SnapshotJob     modelprojection.JobView      `json:"snapshot_job"`
	FeatureVersions map[string]int               `json:"feature_versions"`
	SchemaVersions  map[string][]int             `json:"schema_versions"`
}

func (s *Service) modelLineage(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	lineage, err := s.readModelLineage(r.Context(), id, r.PathValue("model_name"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	httpx.JSON(w, http.StatusOK, lineage)
}

func (s *Service) readModelLineage(
	ctx context.Context,
	id identity.Identity,
	modelName string,
) (ModelLineageView, error) {
	model, found, err := decisionmodels.Read(ctx, s.store, id, modelName)
	if err != nil {
		return ModelLineageView{}, err
	}
	if !found {
		return ModelLineageView{}, fmt.Errorf("modeling: unknown model %q", modelName)
	}
	if model.Lineage == nil {
		return ModelLineageView{}, fmt.Errorf("modeling: model %q was not produced by a governed training job", modelName)
	}
	artifact, found, err := modelprojection.ReadArtifact(ctx, s.store, id, model.Lineage.ArtifactID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("artifact lineage is missing")
		}
		return ModelLineageView{}, err
	}
	snapshot, found, err := modelprojection.ReadSnapshot(ctx, s.store, id, model.Lineage.SnapshotID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("snapshot lineage is missing")
		}
		return ModelLineageView{}, err
	}
	dataset, found, err := modelprojection.ReadDataset(ctx, s.store, id, model.Lineage.DatasetName)
	if err != nil || !found {
		if err == nil {
			err = errors.New("dataset lineage is missing")
		}
		return ModelLineageView{}, err
	}
	trainingJob, found, err := modelprojection.ReadJob(ctx, s.store, id, model.Lineage.TrainingJobID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("training job lineage is missing")
		}
		return ModelLineageView{}, err
	}
	snapshotJob, found, err := modelprojection.ReadJob(ctx, s.store, id, snapshot.JobID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("snapshot job lineage is missing")
		}
		return ModelLineageView{}, err
	}
	return ModelLineageView{
		Model: model, Artifact: artifact, Snapshot: snapshot, Dataset: dataset,
		TrainingJob: trainingJob, SnapshotJob: snapshotJob,
		FeatureVersions: snapshot.Manifest.FeatureVersions,
		SchemaVersions:  snapshot.Manifest.SchemaVersions,
	}, nil
}

// ModelComparison is a governed champion/challenger evidence delta.
type ModelComparison struct {
	Champion             string                  `json:"champion"`
	Challenger           string                  `json:"challenger"`
	SameSnapshot         bool                    `json:"same_snapshot"`
	ChampionEvaluation   domain.BinaryEvaluation `json:"champion_evaluation"`
	ChallengerEvaluation domain.BinaryEvaluation `json:"challenger_evaluation"`
	AUCDelta             float64                 `json:"auc_delta"`
	BrierDelta           float64                 `json:"brier_delta"`
	AccuracyDelta        float64                 `json:"accuracy_delta"`
}

func (s *Service) compareModels(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	champion := r.URL.Query().Get("champion")
	challenger := r.URL.Query().Get("challenger")
	if champion == "" || challenger == "" || champion == challenger {
		httpx.Error(w, http.StatusBadRequest, errors.New(
			"modeling: distinct champion and challenger names are required",
		))
		return
	}
	left, err := s.readModelLineage(r.Context(), id, champion)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	right, err := s.readModelLineage(r.Context(), id, challenger)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err)
		return
	}
	var leftEvaluation, rightEvaluation domain.BinaryEvaluation
	if err := json.Unmarshal(left.Artifact.Publication.EvaluationReport, &leftEvaluation); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	if err := json.Unmarshal(right.Artifact.Publication.EvaluationReport, &rightEvaluation); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ModelComparison{
		Champion: champion, Challenger: challenger,
		SameSnapshot:       left.Snapshot.Manifest.RowsHash == right.Snapshot.Manifest.RowsHash,
		ChampionEvaluation: leftEvaluation, ChallengerEvaluation: rightEvaluation,
		AUCDelta:      rightEvaluation.AUC - leftEvaluation.AUC,
		BrierDelta:    rightEvaluation.Brier - leftEvaluation.Brier,
		AccuracyDelta: rightEvaluation.Accuracy - leftEvaluation.Accuracy,
	})
}
