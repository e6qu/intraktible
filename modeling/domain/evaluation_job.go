// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"errors"
	"strings"
	"time"
)

// EvaluationRequest pins an independently executed evaluation to exact
// artifact and snapshot bytes.
type EvaluationRequest struct {
	ArtifactID     string            `json:"artifact_id"`
	SnapshotID     string            `json:"snapshot_id"`
	Options        EvaluationOptions `json:"options"`
	Purpose        string            `json:"purpose"`
	IdempotencyKey string            `json:"idempotency_key"`
}

// Validate checks evaluation lineage and replay identity.
func (request EvaluationRequest) Validate() error {
	switch {
	case strings.TrimSpace(request.ArtifactID) == "":
		return errors.New("modeling: evaluation artifact_id is required")
	case strings.TrimSpace(request.SnapshotID) == "":
		return errors.New("modeling: evaluation snapshot_id is required")
	case strings.TrimSpace(request.Purpose) == "":
		return errors.New("modeling: evaluation purpose is required")
	case len(strings.TrimSpace(request.IdempotencyKey)) < 8:
		return errors.New("modeling: evaluation idempotency_key must be at least 8 characters")
	default:
		return nil
	}
}

// EvaluationManifest is independently reproduced model evidence.
type EvaluationManifest struct {
	EvaluationID string           `json:"evaluation_id"`
	ArtifactID   string           `json:"artifact_id"`
	ArtifactHash string           `json:"artifact_hash"`
	ModelName    string           `json:"model_name"`
	SnapshotID   string           `json:"snapshot_id"`
	SnapshotHash string           `json:"snapshot_hash"`
	Purpose      string           `json:"purpose"`
	Report       BinaryEvaluation `json:"report"`
	ReportHash   string           `json:"report_hash"`
	EvaluatedAt  time.Time        `json:"evaluated_at"`
}
