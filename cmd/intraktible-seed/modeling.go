// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	modelEntityType = "model_applicant"
	modelDataset    = "default-risk-development"
	modelName       = "default-risk-candidate"
)

type modelSeedRow struct {
	id        string
	segment   string
	amount    float64
	defaulted bool
}

func modelSeedRows() []modelSeedRow {
	return []modelSeedRow{
		{"MDL-001", "thin-file", 92, true},
		{"MDL-002", "established", 12, false},
		{"MDL-003", "thin-file", 86, true},
		{"MDL-004", "established", 18, false},
		{"MDL-005", "thin-file", 74, true},
		{"MDL-006", "established", 24, false},
		{"MDL-007", "thin-file", 81, true},
		{"MDL-008", "established", 29, false},
		{"MDL-009", "thin-file", 67, true},
		{"MDL-010", "established", 34, false},
	}
}

// modelingConfigActions creates the governed contracts and immutable dataset
// before any modeled source record is accepted.
func (s *seeder) modelingConfigActions(cfg *timeCursor) []action {
	at := cfg.step(8 * time.Minute)
	return []action{{at: at, name: "modeling source contracts", run: func() {
		s.defineAndApproveSchema(map[string]any{
			"ref":                   map[string]any{"kind": "entity", "entity_type": modelEntityType},
			"description":           "Point-in-time applicant cohort used by the public modeling journey.",
			"owner_team":            "risk-data-science",
			"purposes":              []string{"credit_underwriting", "model_development"},
			"compatibility":         "backward",
			"additional_properties": false,
			"fields": []map[string]any{
				{
					"name": "cohort_key", "type": "string", "required": true,
					"identifier": true, "classification": "confidential",
					"pattern": "^MDL-[0-9]{3}$", "min_length": 7, "max_length": 7,
				},
				{"name": "risk_segment", "type": "string", "required": true, "classification": "internal"},
				{"name": "region", "type": "string", "required": true, "classification": "internal"},
			},
			"quality": map[string]any{
				"action": "block", "completeness_min": 1,
				"unique_fields": []string{"cohort_key"},
			},
		})
		s.defineAndApproveSchema(map[string]any{
			"ref": map[string]any{
				"kind": "event", "entity_type": modelEntityType, "event_name": "risk_signal",
			},
			"description":           "Correctable event-time risk signal.",
			"owner_team":            "risk-data-science",
			"purposes":              []string{"credit_underwriting", "model_development"},
			"compatibility":         "backward",
			"additional_properties": false,
			"fields": []map[string]any{
				{"name": "amount", "type": "number", "classification": "confidential"},
			},
			"quality": map[string]any{"action": "refer", "completeness_min": 1},
		})
		s.defineAndApproveSchema(map[string]any{
			"ref": map[string]any{
				"kind": "event", "entity_type": modelEntityType, "event_name": "repayment_outcome",
			},
			"description":           "Observed supervised outcome inside the dataset label horizon.",
			"owner_team":            "credit-operations",
			"purposes":              []string{"model_development", "model_validation"},
			"compatibility":         "backward",
			"additional_properties": false,
			"fields": []map[string]any{
				{"name": "defaulted", "type": "boolean", "required": true, "classification": "confidential"},
			},
			"quality": map[string]any{"action": "block", "completeness_min": 1},
		})
		s.call(actorPriya, http.MethodPost, "/v1/context/features", map[string]any{
			"name": "risk_signal_count_30d", "entity_type": modelEntityType,
			"event_name": "risk_signal", "aggregation": "count", "window_hours": 720,
		}, nil)
		s.call(actorPriya, http.MethodPost, "/v1/context/features", map[string]any{
			"name": "risk_signal_sum_30d", "entity_type": modelEntityType,
			"event_name": "risk_signal", "aggregation": "sum", "field": "amount",
			"window_hours": 720,
		}, nil)
		s.call(actorPriya, http.MethodPost, "/v1/modeling/datasets", map[string]any{
			"name":        modelDataset,
			"description": "Immutable point-in-time default-risk development cohort.",
			"owner_team":  "risk-data-science",
			"entity_type": modelEntityType,
			"features":    []string{"risk_signal_count_30d", "risk_signal_sum_30d"},
			"label": map[string]any{
				"event_name": "repayment_outcome", "field": "defaulted",
				"kind": "binary", "positive_value": "true", "horizon_hours": 48,
			},
			"segment_fields": []string{"risk_segment"},
			"purpose":        "model_development",
			"consent_requirement": map[string]any{
				"mode": "not_required",
			},
			"retention_days": 365,
			"partitions": map[string]any{
				"train_bps": 7000, "validation_bps": 1500, "test_bps": 1500,
			},
		}, nil)
	}}}
}

func (s *seeder) defineAndApproveSchema(spec map[string]any) {
	s.call(actorPriya, http.MethodPost, "/v1/modeling/schemas", spec, nil)
	ref := spec["ref"].(map[string]any)
	kind, entityType := ref["kind"].(string), ref["entity_type"].(string)
	path := fmt.Sprintf(
		"/v1/modeling/schemas/%s/%s/versions/1/approval-request",
		url.PathEscape(kind), url.PathEscape(entityType),
	)
	if eventName, ok := ref["event_name"].(string); ok {
		path += "?event_name=" + url.QueryEscape(eventName)
	}
	var request struct {
		RequestID string `json:"request_id"`
	}
	s.call(actorPriya, http.MethodPost, path, map[string]any{}, &request)
	s.call(actorMarcus, http.MethodPost,
		"/v1/modeling/schema-approval/"+url.PathEscape(request.RequestID)+"/decision",
		map[string]any{
			"ref": ref, "approve": true,
			"reason": "Contract ownership, permissible purposes, compatibility, and quality policy verified.",
		}, nil,
	)
}

// modelingDataActions records a bitemporal cohort, including one corrected
// signal and one referred quality violation.
func (s *seeder) modelingDataActions(anchor time.Time) []action {
	var actions []action
	for _, row := range modelSeedRows() {
		actions = append(actions, action{
			at: anchor.Add(-72 * time.Hour), name: "model entity " + row.id, run: func() {
				s.call(actorDiego, http.MethodPost, "/v1/context/entities", map[string]any{
					"entity_type": modelEntityType, "entity_id": row.id, "schema_version": 1,
					"attributes": map[string]any{
						"cohort_key": row.id, "risk_segment": row.segment, "region": "US",
					},
				}, nil)
			},
		}, action{
			at: anchor.Add(-48 * time.Hour), name: "model signal " + row.id, run: func() {
				var response struct {
					SourceEventID string `json:"source_event_id"`
				}
				s.call(actorDiego, http.MethodPost, "/v1/context/events", map[string]any{
					"entity_type": modelEntityType, "entity_id": row.id,
					"event_name": "risk_signal", "schema_version": 1,
					"event_id":    "signal-" + row.id,
					"data":        map[string]any{"amount": row.amount},
					"occurred_at": anchor.Add(-48 * time.Hour).Format(time.RFC3339),
				}, &response)
				s.setID("signal:"+row.id, response.SourceEventID)
			},
		}, action{
			at: anchor.Add(-12 * time.Hour), name: "model outcome " + row.id, run: func() {
				s.call(actorDiego, http.MethodPost, "/v1/context/events", map[string]any{
					"entity_type": modelEntityType, "entity_id": row.id,
					"event_name": "repayment_outcome", "schema_version": 1,
					"event_id":    "outcome-" + row.id,
					"data":        map[string]any{"defaulted": row.defaulted},
					"occurred_at": anchor.Add(-12 * time.Hour).Format(time.RFC3339),
				}, nil)
			},
		})
	}
	actions = append(actions, action{
		at: anchor.Add(-36 * time.Hour), name: "correct model signal", run: func() {
			s.call(actorDiego, http.MethodPost, "/v1/context/events", map[string]any{
				"entity_type": modelEntityType, "entity_id": "MDL-001",
				"event_name": "risk_signal", "schema_version": 1,
				"event_id":            "signal-MDL-001-corrected",
				"supersedes_event_id": s.id("signal:MDL-001"),
				"data":                map[string]any{"amount": 96},
				"occurred_at":         anchor.Add(-48 * time.Hour).Format(time.RFC3339),
			}, nil)
		},
	}, action{
		at: anchor.Add(-20 * time.Hour), name: "referred model signal", run: func() {
			s.call(actorDiego, http.MethodPost, "/v1/context/events", map[string]any{
				"entity_type": modelEntityType, "entity_id": "MDL-010",
				"event_name": "risk_signal", "schema_version": 1,
				"event_id":    "signal-MDL-010-incomplete",
				"data":        map[string]any{},
				"occurred_at": anchor.Add(-20 * time.Hour).Format(time.RFC3339),
			}, nil)
		},
	})
	return actions
}

func (s *seeder) runModelingJobs(anchor time.Time) {
	s.clk.Set(anchor.Add(-time.Hour))
	var snapshot struct {
		JobID      string `json:"job_id"`
		SnapshotID string `json:"snapshot_id"`
	}
	s.call(actorPriya, http.MethodPost,
		"/v1/modeling/datasets/"+url.PathEscape(modelDataset)+"/versions/1/snapshots",
		map[string]any{
			"observation_at":  anchor.Add(-24 * time.Hour).Format(time.RFC3339),
			"knowledge_at":    anchor.Add(-time.Hour).Format(time.RFC3339),
			"idempotency_key": "demo-snapshot-2026",
		}, &snapshot,
	)
	s.waitModelingJob(snapshot.JobID)
	s.setID("modeling:snapshot", snapshot.SnapshotID)

	var training struct {
		JobID      string `json:"job_id"`
		ArtifactID string `json:"artifact_id"`
	}
	s.call(actorPriya, http.MethodPost, "/v1/modeling/training-jobs", map[string]any{
		"model_name": modelName, "snapshot_id": snapshot.SnapshotID,
		"runtime": "intraktible-logistic/v1", "code_revision": "demo-model-notebook@2026.07",
		"parameters": map[string]any{
			"iterations": 400, "learning_rate": 0.08, "l2": 0.01,
		},
		"seed": 20260731, "idempotency_key": "demo-training-2026",
	}, &training)
	s.waitModelingJob(training.JobID)
	s.setID("modeling:artifact", training.ArtifactID)

	var evaluation struct {
		JobID        string `json:"job_id"`
		EvaluationID string `json:"evaluation_id"`
	}
	s.call(actorPriya, http.MethodPost, "/v1/modeling/evaluation-jobs", map[string]any{
		"artifact_id": training.ArtifactID, "snapshot_id": snapshot.SnapshotID,
		"purpose":         "independent_model_validation",
		"idempotency_key": "demo-evaluation-2026",
		"options":         map[string]any{},
	}, &evaluation)
	s.waitModelingJob(evaluation.JobID)
	s.setID("modeling:evaluation", evaluation.EvaluationID)
	s.call(actorMarcus, http.MethodPost,
		"/v1/modeling/artifacts/"+url.PathEscape(training.ArtifactID)+"/stage",
		map[string]any{
			"stage":  "validated",
			"reason": "Independent signature, provenance, vulnerability, and evaluation evidence review passed.",
		}, nil,
	)
	var evaluationView struct {
		Manifest struct {
			ReportHash string `json:"report_hash"`
			Report     struct {
				AUC      float64 `json:"auc"`
				Brier    float64 `json:"brier"`
				Accuracy float64 `json:"accuracy"`
			} `json:"report"`
		} `json:"manifest"`
	}
	s.call(actorMarcus, http.MethodGet,
		"/v1/modeling/evaluations/"+url.PathEscape(evaluation.EvaluationID),
		nil, &evaluationView,
	)
	s.call(actorMarcus, http.MethodPost,
		"/v1/models/"+url.PathEscape(modelName)+"/validation",
		map[string]any{
			"dataset": modelDataset,
			"metrics": map[string]any{
				"auc":      evaluationView.Manifest.Report.AUC,
				"brier":    evaluationView.Manifest.Report.Brier,
				"accuracy": evaluationView.Manifest.Report.Accuracy,
			},
			"notes":  "Independent validator reproduced the signed holdout report and reviewed all mandatory gates.",
			"passed": true, "artifact_id": training.ArtifactID,
			"snapshot_id":     snapshot.SnapshotID,
			"evaluation_hash": evaluationView.Manifest.ReportHash,
			"leakage_passed":  true, "calibration_reviewed": true,
			"fairness_reviewed": true, "threshold_reviewed": true,
		}, nil,
	)
	s.call(actorMarcus, http.MethodPost,
		"/v1/modeling/artifacts/"+url.PathEscape(training.ArtifactID)+"/stage",
		map[string]any{
			"stage":  "production",
			"reason": "Artifact supply-chain gate passed for production use.",
		}, nil,
	)
	var approval struct {
		RequestID string `json:"request_id"`
	}
	s.call(actorPriya, http.MethodPost,
		"/v1/models/"+url.PathEscape(modelName)+"/approval-request", nil, &approval,
	)
	s.call(actorMarcus, http.MethodPost,
		"/v1/models/"+url.PathEscape(modelName)+"/approve",
		map[string]any{
			"request_id": approval.RequestID,
			"reason":     "Signed artifact, independent evaluation, and all validation attestations verified.",
		}, nil,
	)

	var backfill struct {
		JobID      string `json:"job_id"`
		BackfillID string `json:"backfill_id"`
	}
	s.call(actorPriya, http.MethodPost, "/v1/modeling/backfills", map[string]any{
		"entity_type":     modelEntityType,
		"features":        []string{"risk_signal_count_30d", "risk_signal_sum_30d"},
		"as_of":           anchor.Add(-24 * time.Hour).Format(time.RFC3339),
		"knowledge_at":    anchor.Add(-time.Hour).Format(time.RFC3339),
		"idempotency_key": "demo-backfill-2026",
	}, &backfill)
	s.waitModelingJob(backfill.JobID)
	s.setID("modeling:backfill", backfill.BackfillID)
}

func (s *seeder) waitModelingJob(jobID string) {
	for attempt := 0; attempt < 10_000; attempt++ {
		var job struct {
			State string `json:"state"`
			Error string `json:"error"`
		}
		s.call(actorPriya, http.MethodGet, "/v1/modeling/jobs/"+url.PathEscape(jobID), nil, &job)
		switch job.State {
		case "completed":
			return
		case "failed", "cancelled":
			fatalf("modeling job %s ended %s: %s", jobID, job.State, job.Error)
		case "queued", "running", "cancel_requested":
			time.Sleep(25 * time.Millisecond)
		default:
			fatalf("modeling job %s has unknown state %q", jobID, job.State)
		}
	}
	fatalf("modeling job %s did not reach a terminal state", jobID)
}
