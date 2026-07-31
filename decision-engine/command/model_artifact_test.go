// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/command"
	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	modelingdomain "github.com/e6qu/intraktible/modeling/domain"
	modelingevents "github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/eventlog"
)

func TestTrainedModelRequiresValidatedThenProductionArtifact(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	handler := command.NewHandler(log)
	owner, validator := idFor("owner"), idFor("validator")
	hash := strings.Repeat("a", 64)
	artifactID := "artifact-1"

	if _, err := handler.DefineModelWithLineage(
		ctx, owner, "trained-risk", modelSpec,
		decisionevents.ModelLineage{
			TrainingJobID: "training-1", ArtifactID: artifactID, ArtifactHash: hash,
			SnapshotID: "snapshot-1", SnapshotHash: hash, DatasetName: "risk",
			DatasetVersion: 1, Runtime: "intraktible-logistic/v1",
			CodeRevision: "git:0123456789ab", ParametersHash: hash, Seed: 17,
		},
		decisionevents.TrainingPublication{
			Attempt: 1, Signature: "signature", PublicKey: "public-key",
			StorageRef: "artifact://artifact-1", ModelSpecHash: hash,
			TrainingReport:   json.RawMessage(`{"loss":0.1}`),
			EvaluationReport: json.RawMessage(`{"auc":0.8}`),
			EvaluationHash:   hash, PublishedAt: time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC),
		},
	); err != nil {
		t.Fatal(err)
	}

	validation := decisionevents.ModelValidationRecorded{
		Dataset: "risk", Metrics: map[string]float64{"auc": 0.8},
		Notes: "Independent evidence reviewed", Passed: true,
		ArtifactID: artifactID, SnapshotID: "snapshot-1", EvaluationHash: hash,
		LeakagePassed: true, CalibrationReviewed: true,
		FairnessReviewed: true, ThresholdReviewed: true,
	}
	if _, err := handler.RecordModelValidation(
		ctx, validator, "trained-risk", validation,
	); err == nil || !strings.Contains(err.Error(), "must be validated") {
		t.Fatalf("validation before artifact validation = %v", err)
	}
	appendArtifactStage(
		t, log, validator.Actor, artifactID,
		modelingdomain.ArtifactRegistered, modelingdomain.ArtifactValidated,
	)
	if _, err := handler.RecordModelValidation(
		ctx, validator, "trained-risk", validation,
	); err != nil {
		t.Fatal(err)
	}
	requestID, _, err := handler.RequestModelApproval(ctx, owner, "trained-risk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ApproveModelApproval(
		ctx, validator, "trained-risk", requestID, "review passed",
	); err == nil || !strings.Contains(err.Error(), "must be production") {
		t.Fatalf("approval before production artifact = %v", err)
	}
	appendArtifactStage(
		t, log, validator.Actor, artifactID,
		modelingdomain.ArtifactValidated, modelingdomain.ArtifactProduction,
	)
	if _, err := handler.ApproveModelApproval(
		ctx, validator, "trained-risk", requestID, "review passed",
	); err != nil {
		t.Fatal(err)
	}
}

func appendArtifactStage(
	t *testing.T,
	log eventlog.Log,
	actor string,
	artifactID string,
	from modelingdomain.ArtifactStage,
	to modelingdomain.ArtifactStage,
) {
	t.Helper()
	payload, err := json.Marshal(modelingevents.ArtifactStageChanged{
		ArtifactID: artifactID, From: from, To: to, Reason: "review passed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Append(context.Background(), eventlog.Envelope{
		Org: "o", Workspace: "w", Actor: actor,
		Stream: modelingevents.StreamModeling, Type: modelingevents.TypeArtifactStageChanged,
		Time: time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC), Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
}
