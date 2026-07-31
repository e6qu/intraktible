// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// TrainingRuntime is the versioned, allowlisted execution environment.
type TrainingRuntime string

const (
	RuntimeLogisticV1 TrainingRuntime = "intraktible-logistic/v1"
)

// TrainingParameters are the deterministic logistic trainer controls.
type TrainingParameters struct {
	Iterations   int     `json:"iterations,omitempty"`
	LearningRate float64 `json:"learning_rate,omitempty"`
	L2           float64 `json:"l2,omitempty"`
	Folds        int     `json:"folds,omitempty"`
}

// TrainingRequest pins every input needed to reproduce a fit.
type TrainingRequest struct {
	ModelName      string             `json:"model_name"`
	SnapshotID     string             `json:"snapshot_id"`
	Runtime        TrainingRuntime    `json:"runtime"`
	CodeRevision   string             `json:"code_revision"`
	Parameters     TrainingParameters `json:"parameters"`
	Seed           int64              `json:"seed"`
	IdempotencyKey string             `json:"idempotency_key"`
}

// Validate rejects arbitrary runtimes and incomplete provenance.
func (request TrainingRequest) Validate() error {
	switch {
	case strings.TrimSpace(request.ModelName) == "":
		return errors.New("modeling: model_name is required")
	case strings.Contains(request.ModelName, "/"):
		return errors.New("modeling: model_name must not contain '/'")
	case strings.TrimSpace(request.SnapshotID) == "":
		return errors.New("modeling: snapshot_id is required")
	case request.Runtime != RuntimeLogisticV1:
		return fmt.Errorf("modeling: unsupported training runtime %q", request.Runtime)
	case strings.TrimSpace(request.CodeRevision) == "":
		return errors.New("modeling: code_revision is required")
	case len(strings.TrimSpace(request.IdempotencyKey)) < 8:
		return errors.New("modeling: idempotency_key must be at least 8 characters")
	case request.Parameters.Iterations < 0:
		return errors.New("modeling: iterations must not be negative")
	case request.Parameters.LearningRate < 0 ||
		math.IsNaN(request.Parameters.LearningRate) ||
		math.IsInf(request.Parameters.LearningRate, 0):
		return errors.New("modeling: learning_rate must be finite and non-negative")
	case request.Parameters.L2 < 0 ||
		math.IsNaN(request.Parameters.L2) ||
		math.IsInf(request.Parameters.L2, 0):
		return errors.New("modeling: l2 must be finite and non-negative")
	case request.Parameters.Folds < 0:
		return errors.New("modeling: folds must not be negative")
	default:
		return nil
	}
}

// ArtifactManifest is the signed, content-addressed training output.
type ArtifactManifest struct {
	ArtifactID     string          `json:"artifact_id"`
	ArtifactHash   string          `json:"artifact_hash"`
	Signature      string          `json:"signature"`
	PublicKey      string          `json:"public_key"`
	StorageRef     string          `json:"storage_ref"`
	ModelName      string          `json:"model_name"`
	SnapshotID     string          `json:"snapshot_id"`
	SnapshotHash   string          `json:"snapshot_hash"`
	DatasetName    string          `json:"dataset_name"`
	DatasetVersion int             `json:"dataset_version"`
	Runtime        TrainingRuntime `json:"runtime"`
	CodeRevision   string          `json:"code_revision"`
	ParametersHash string          `json:"parameters_hash"`
	Seed           int64           `json:"seed"`
	TrainingReport json.RawMessage `json:"training_report"`
	ModelSpecHash  string          `json:"model_spec_hash"`
	PublishedAt    time.Time       `json:"published_at"`
}

// ModelLineage is the portable provenance recorded on the production model.
type ModelLineage struct {
	TrainingJobID  string `json:"training_job_id"`
	ArtifactID     string `json:"artifact_id"`
	ArtifactHash   string `json:"artifact_hash"`
	SnapshotID     string `json:"snapshot_id"`
	SnapshotHash   string `json:"snapshot_hash"`
	DatasetName    string `json:"dataset_name"`
	DatasetVersion int    `json:"dataset_version"`
	Runtime        string `json:"runtime"`
	CodeRevision   string `json:"code_revision"`
	ParametersHash string `json:"parameters_hash"`
	Seed           int64  `json:"seed"`
}
