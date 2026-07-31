// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	contextevents "github.com/e6qu/intraktible/context-layer/events"
	modelingcmd "github.com/e6qu/intraktible/modeling/command"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestModelingSchedulerExpiresRowsAndReconcilesFreshness(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := eventlog.NewMemory()
	defer func() { _ = log.Close() }()
	st := store.NewMemory()
	runtime := projection.New(log, st, modelprojection.Projector{})
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}

	current := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return current }
	handler := modelingcmd.NewHandler(log).WithNow(now)
	maker := identity.Identity{Org: "demo", Workspace: "main", Actor: "maker"}
	checker := identity.Identity{Org: "demo", Workspace: "main", Actor: "checker"}
	ref := domain.SchemaRef{Kind: domain.SchemaKindEntity, EntityType: "applicant"}
	spec := domain.SchemaSpec{
		Ref: ref, OwnerTeam: "data-platform", Purposes: []string{"underwriting"},
		Compatibility: domain.CompatibilityBackward,
		Fields: []domain.FieldSpec{{
			Name: "age", Type: domain.FieldInteger,
			Classification: domain.ClassificationConfidential,
		}},
		Quality: domain.QualityContract{
			Action: domain.QualityRefer, FreshnessSeconds: 60,
		},
	}
	if _, err := handler.DefineSchema(ctx, maker, spec); err != nil {
		t.Fatal(err)
	}
	requestID, _, err := handler.RequestSchemaApproval(ctx, maker, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.DecideSchemaApproval(ctx, checker, ref, requestID, true, "valid"); err != nil {
		t.Fatal(err)
	}

	dataset := domain.DatasetSpec{
		Name: "retained", OwnerTeam: "science", EntityType: "applicant",
		Features: []string{"age_score"}, Purpose: "model-development",
		ConsentRequirement: domain.ConsentRequirement{Mode: domain.ConsentNotRequired},
		RetentionDays:      1,
		Label: domain.LabelSpec{
			EventName: "outcome", Field: "bad", Kind: domain.LabelBinary,
			PositiveValue: "true", HorizonHours: 24,
		},
		Partitions: domain.PartitionSpec{TrainBPS: 8000, ValidationBPS: 1000, TestBPS: 1000},
	}
	if _, err := handler.DefineDataset(ctx, maker, dataset); err != nil {
		t.Fatal(err)
	}
	jobID, snapshotID, _, err := handler.RequestSnapshot(
		ctx, maker, domain.SnapshotRequest{
			DatasetName: dataset.Name, Version: 1,
			ObservationAt: current.Add(-time.Hour), KnowledgeAt: current,
			IdempotencyKey: "retention-snapshot",
		},
		map[string]int{"age_score": 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := handler.ClaimJob(ctx, maker, jobID, "test-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.SnapshotManifest{
		SnapshotID: snapshotID, DatasetName: dataset.Name, DatasetVersion: 1,
		DatasetHash: "dataset-hash", RowsHash: "rows-hash", RowCount: 1,
		FeatureVersions: map[string]int{"age_score": 1},
		SchemaVersions:  map[string][]int{}, ObservationAt: current.Add(-time.Hour),
		KnowledgeAt: current, StorageRef: "snapshot/" + snapshotID + "/rows-hash",
		Purpose: dataset.Purpose, ExpiresAt: current.Add(24 * time.Hour), PublishedAt: current,
	}
	if err := store.PutDoc(
		ctx, st, modelprojection.CollectionSnapshotBlobs,
		store.Key(maker.Org, maker.Workspace, manifest.StorageRef),
		snapshotBlob{},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.PublishSnapshot(
		ctx, maker, jobID, attempt, "test-worker", manifest,
	); err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)

	current = current.Add(48 * time.Hour)
	scheduler := NewScheduler(handler, st).WithNow(now)
	summary, err := scheduler.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.SnapshotsExpired != 1 || summary.FreshnessOpened != 1 {
		t.Fatalf("first tick = %+v", summary)
	}
	waitApplied(t, runtime, log)
	if _, found, err := store.GetDoc[snapshotBlob](
		ctx, st, modelprojection.CollectionSnapshotBlobs,
		store.Key(maker.Org, maker.Workspace, manifest.StorageRef),
	); err != nil || found {
		t.Fatalf("expired blob found=%v err=%v", found, err)
	}

	incidents, err := modelprojection.ListQualityIncidents(ctx, st, maker)
	if err != nil || len(incidents) != 1 || incidents[0].IncidentType != "freshness" ||
		incidents[0].Status != "open" {
		t.Fatalf("incidents=%+v err=%v", incidents, err)
	}
	evidence := contextevents.SchemaEvidence{
		Version: 1, Hash: incidents[0].SchemaHash, Action: string(domain.QualityRefer),
		EvaluatedAt: current,
	}
	if _, err := eventlog.AppendJSON(
		ctx, log, maker.Org, maker.Workspace, maker.Actor,
		contextevents.StreamContext, contextevents.TypeEntityRecorded, current,
		contextevents.EntityRecorded{
			EntityType: "applicant", EntityID: "fresh",
			Attributes: json.RawMessage(`{"age":31}`), SchemaEvidence: evidence,
		},
	); err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)
	recovery, err := scheduler.Tick(ctx)
	if err != nil || recovery.FreshnessRecovered != 1 {
		t.Fatalf("recovery tick=%+v err=%v", recovery, err)
	}
	waitApplied(t, runtime, log)
	incidents, err = modelprojection.ListQualityIncidents(ctx, st, maker)
	if err != nil || incidents[0].Status != "resolved" {
		t.Fatalf("recovered incidents=%+v err=%v", incidents, err)
	}
}
