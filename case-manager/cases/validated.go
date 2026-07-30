// SPDX-License-Identifier: AGPL-3.0-or-later

package cases

import (
	"context"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// ValidatedOutcome is a two-review evidence record suitable for downstream
// agent/model evaluation. A disagreement is excluded until the independent
// reviewer explicitly overrides, so one primary reviewer is never ground truth.
type ValidatedOutcome struct {
	CaseID               string         `json:"case_id"`
	CaseType             string         `json:"case_type"`
	CaseTypeVersion      int            `json:"case_type_version"`
	SourceDecisionID     string         `json:"source_decision_id,omitempty"`
	PrimaryDisposition   string         `json:"primary_disposition"`
	PrimaryReasonCode    string         `json:"primary_reason_code"`
	ReviewedDisposition  string         `json:"reviewed_disposition"`
	ReviewedReasonCode   string         `json:"reviewed_reason_code"`
	EffectiveDisposition string         `json:"effective_disposition"`
	EffectiveReasonCode  string         `json:"effective_reason_code"`
	Validation           string         `json:"validation"`
	Evidence             []EvidenceLink `json:"evidence"`
	ValidatedAt          time.Time      `json:"validated_at"`
}

// ListValidatedOutcomes exposes only independently validated records.
func ListValidatedOutcomes(
	ctx context.Context,
	st store.Store,
	id identity.Identity,
) ([]ValidatedOutcome, error) {
	views, err := List(ctx, st, id, Filter{})
	if err != nil {
		return nil, err
	}
	out := make([]ValidatedOutcome, 0)
	for _, view := range views {
		if view.QA == nil || !view.QA.Validated {
			continue
		}
		validation := "agreement"
		if view.QA.Override {
			validation = "override"
		}
		out = append(out, ValidatedOutcome{
			CaseID: view.CaseID, CaseType: view.CaseType, CaseTypeVersion: view.CaseTypeVersion,
			SourceDecisionID: view.SourceID, PrimaryDisposition: view.Disposition,
			PrimaryReasonCode: view.ReasonCode, ReviewedDisposition: view.QA.Disposition,
			ReviewedReasonCode: view.QA.ReasonCode, EffectiveDisposition: view.QA.Effective,
			EffectiveReasonCode: view.QA.EffectiveReason, Validation: validation,
			Evidence: append([]EvidenceLink(nil), view.Evidence...), ValidatedAt: view.UpdatedAt,
		})
	}
	return out, nil
}
