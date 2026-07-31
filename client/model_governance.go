// SPDX-License-Identifier: AGPL-3.0-or-later

package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/e6qu/intraktible/decision-engine/models"
)

// ModelView is the public governed model registry view.
type ModelView = models.ModelView

// ModelValidationRequest is the independent evidence required before a model
// version can pass maker-checker approval.
type ModelValidationRequest struct {
	Dataset             string             `json:"dataset"`
	Metrics             map[string]float64 `json:"metrics"`
	Notes               string             `json:"notes"`
	Passed              bool               `json:"passed"`
	ArtifactID          string             `json:"artifact_id,omitempty"`
	SnapshotID          string             `json:"snapshot_id,omitempty"`
	EvaluationHash      string             `json:"evaluation_hash,omitempty"`
	LeakagePassed       bool               `json:"leakage_passed,omitempty"`
	CalibrationReviewed bool               `json:"calibration_reviewed,omitempty"`
	FairnessReviewed    bool               `json:"fairness_reviewed,omitempty"`
	ThresholdReviewed   bool               `json:"threshold_reviewed,omitempty"`
}

// ListModels returns every governed model in the caller's workspace.
func (c *Client) ListModels(ctx context.Context) ([]ModelView, error) {
	out, err := do[struct {
		Models []ModelView `json:"models"`
	}](ctx, c, http.MethodGet, "/v1/models", nil)
	return out.Models, err
}

// GetModel reads one governed model including lineage, validation, approval,
// and retirement state.
func (c *Client) GetModel(ctx context.Context, name string) (ModelView, error) {
	return do[ModelView](ctx, c, http.MethodGet, "/v1/models/"+url.PathEscape(name), nil)
}

// RecordModelValidation records independent validation evidence for the
// model's current immutable version.
func (c *Client) RecordModelValidation(
	ctx context.Context,
	name string,
	request ModelValidationRequest,
) (CommandResult, error) {
	return do[CommandResult](
		ctx, c, http.MethodPost,
		"/v1/models/"+url.PathEscape(name)+"/validation", request,
	)
}

// RequestModelApproval starts maker-checker review for the current version.
func (c *Client) RequestModelApproval(ctx context.Context, name string) (string, error) {
	out, err := do[struct {
		RequestID string `json:"request_id"`
	}](
		ctx, c, http.MethodPost,
		"/v1/models/"+url.PathEscape(name)+"/approval-request", map[string]any{},
	)
	return out.RequestID, err
}

// DecideModelApproval records the checker's approval or rejection.
func (c *Client) DecideModelApproval(
	ctx context.Context,
	name string,
	requestID string,
	approve bool,
	reason string,
) (CommandResult, error) {
	action := "reject"
	if approve {
		action = "approve"
	}
	return do[CommandResult](
		ctx, c, http.MethodPost,
		"/v1/models/"+url.PathEscape(name)+"/"+action,
		map[string]string{"request_id": requestID, "reason": reason},
	)
}

// RetireModel blocks the current governed version from serving while retaining
// its complete lineage and audit history.
func (c *Client) RetireModel(
	ctx context.Context,
	name string,
	reason string,
) (CommandResult, error) {
	return do[CommandResult](
		ctx, c, http.MethodPost,
		"/v1/models/"+url.PathEscape(name)+"/retire",
		map[string]string{"reason": reason},
	)
}
