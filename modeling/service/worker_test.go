// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	contextcmd "github.com/e6qu/intraktible/context-layer/command"
	contextdomain "github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/entities"
	"github.com/e6qu/intraktible/context-layer/features"
	decisioncmd "github.com/e6qu/intraktible/decision-engine/command"
	decisionmodels "github.com/e6qu/intraktible/decision-engine/models"
	modelingcmd "github.com/e6qu/intraktible/modeling/command"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/consent"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func waitApplied(t *testing.T, runtime *projection.Runtime, log eventlog.Log) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.Applied() < log.Head() {
		if time.Now().After(deadline) {
			t.Fatalf("projection stopped at %d, log head %d", runtime.Applied(), log.Head())
		}
		time.Sleep(time.Millisecond)
	}
	if err := runtime.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestActiveWorkerObservesPauseControlAndCancelsWork(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "worker"}
	job := modelprojection.JobView{
		Org: id.Org, Workspace: id.Workspace, JobID: "job-1",
		State: "running", Attempt: 1, Worker: "worker-1",
	}
	key := store.Key(id.Org, id.Workspace, job.JobID)
	if err := store.PutDoc(ctx, st, modelprojection.CollectionJobs, key, job); err != nil {
		t.Fatal(err)
	}
	runCtx, stopWork := context.WithCancel(ctx)
	results := make(chan error, 1)
	service := New(nil, st)
	go service.watchJob(
		runCtx, stopWork, id, job.JobID, job.Attempt, job.Worker, results,
	)
	job.State = "pausing"
	if err := store.PutDoc(ctx, st, modelprojection.CollectionJobs, key, job); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-results:
		if !errors.Is(err, errJobPauseRequested) {
			t.Fatalf("worker control error = %v", err)
		}
		if runCtx.Err() == nil {
			t.Fatal("worker work context was not cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not observe pause request")
	}
}

func TestTrainingWorkerReproducesSignedArtifactAndRegistersLineage(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := eventlog.NewMemory()
	defer func() { _ = log.Close() }()
	st := store.NewMemory()
	runtime := projection.New(log, st, modelprojection.Projector{}, decisionmodels.Projector{})
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "modeler"}
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	modeling := modelingcmd.NewHandler(log).WithNow(func() time.Time { return now })
	datasetSpec := domain.DatasetSpec{
		Name: "risk-training", OwnerTeam: "risk-science", EntityType: "applicant",
		Features: []string{"x"},
		Label: domain.LabelSpec{
			EventName: "outcome", Field: "bad", Kind: domain.LabelBinary,
			PositiveValue: "true", HorizonHours: 24,
		},
		Purpose:            "model-development",
		ConsentRequirement: domain.ConsentRequirement{Mode: domain.ConsentNotRequired},
		RetentionDays:      30,
		Partitions:         domain.PartitionSpec{TrainBPS: 5000, ValidationBPS: 2500, TestBPS: 2500},
	}
	if _, err := modeling.DefineDataset(ctx, id, datasetSpec); err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)
	observationAt := now.Add(-7 * 24 * time.Hour)
	rows := []domain.DatasetRow{
		{EntityID: "t0", Features: map[string]float64{"x": -2}, Label: json.RawMessage(`false`), LabelPresent: true, Partition: "train", ObservationAt: observationAt, KnowledgeAt: now},
		{EntityID: "t1", Features: map[string]float64{"x": -1}, Label: json.RawMessage(`false`), LabelPresent: true, Partition: "train", ObservationAt: observationAt, KnowledgeAt: now},
		{EntityID: "t2", Features: map[string]float64{"x": 1}, Label: json.RawMessage(`true`), LabelPresent: true, Partition: "train", ObservationAt: observationAt, KnowledgeAt: now},
		{EntityID: "t3", Features: map[string]float64{"x": 2}, Label: json.RawMessage(`true`), LabelPresent: true, Partition: "train", ObservationAt: observationAt, KnowledgeAt: now},
		{EntityID: "v0", Features: map[string]float64{"x": -1.5}, Label: json.RawMessage(`false`), LabelPresent: true, Partition: "validation", ObservationAt: observationAt, KnowledgeAt: now},
		{EntityID: "v1", Features: map[string]float64{"x": -0.5}, Label: json.RawMessage(`false`), LabelPresent: true, Partition: "validation", ObservationAt: observationAt, KnowledgeAt: now},
		{EntityID: "v2", Features: map[string]float64{"x": 0.5}, Label: json.RawMessage(`true`), LabelPresent: true, Partition: "test", ObservationAt: observationAt, KnowledgeAt: now},
		{EntityID: "v3", Features: map[string]float64{"x": 1.5}, Label: json.RawMessage(`true`), LabelPresent: true, Partition: "test", ObservationAt: observationAt, KnowledgeAt: now},
	}
	rowsHash, _, err := domain.HashRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.SnapshotManifest{
		SnapshotID: "snapshot-training", DatasetName: datasetSpec.Name, DatasetVersion: 1,
		DatasetHash: "dataset-hash", RowsHash: rowsHash, RowCount: len(rows),
		LabelledCount: len(rows), PartitionCounts: map[string]int{"train": 4, "validation": 2, "test": 2},
		FeatureVersions: map[string]int{"x": 1}, SchemaVersions: map[string][]int{},
		ObservationAt: observationAt, KnowledgeAt: now,
		StorageRef: "snapshot/snapshot-training/" + rowsHash,
		Purpose:    datasetSpec.Purpose, ExpiresAt: now.Add(30 * 24 * time.Hour), PublishedAt: now,
	}
	vault := erasure.NewVault(st).WithNow(func() time.Time { return now })
	sealedRows, err := sealRows(
		ctx, vault, id, datasetSpec.EntityType, rows,
		func(row domain.DatasetRow) string { return row.EntityID },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutDoc(ctx, st, modelprojection.CollectionSnapshotBlobs,
		store.Key(id.Org, id.Workspace, manifest.StorageRef),
		snapshotBlob{Rows: sealedRows}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutDoc(ctx, st, modelprojection.CollectionSnapshots,
		store.Key(id.Org, id.Workspace, manifest.SnapshotID), modelprojection.SnapshotView{
			Org: id.Org, Workspace: id.Workspace, JobID: "snapshot-job",
			Manifest: manifest, PublishedBy: id.Actor, State: "available",
		}); err != nil {
		t.Fatal(err)
	}
	service := New(
		modeling, st, WithNow(func() time.Time { return now }),
		WithContentSealer(vault),
	)
	service.UseModelRegistrar(decisioncmd.NewHandler(log).WithNow(func() time.Time { return now }))
	run := func(idempotency string) modelprojection.ArtifactView {
		t.Helper()
		jobID, artifactID, _, err := modeling.RequestTraining(ctx, id, domain.TrainingRequest{
			ModelName: "credit-risk-v1", SnapshotID: manifest.SnapshotID,
			Runtime: domain.RuntimeLogisticV1, CodeRevision: "git:abc123", Seed: 17,
			Parameters: domain.TrainingParameters{
				Iterations: 100, LearningRate: 0.2, Folds: 2,
			},
			IdempotencyKey: idempotency,
		}, manifest)
		if err != nil {
			t.Fatal(err)
		}
		waitApplied(t, runtime, log)
		worked, err := service.Tick(ctx, "trainer")
		if err != nil || !worked {
			t.Fatalf("tick worked=%v err=%v", worked, err)
		}
		waitApplied(t, runtime, log)
		job, found, err := modelprojection.ReadJob(ctx, st, id, jobID)
		if err != nil || !found || job.State != "completed" {
			t.Fatalf("job=%+v found=%v err=%v", job, found, err)
		}
		artifact, found, err := modelprojection.ReadArtifact(ctx, st, id, artifactID)
		if err != nil || !found {
			t.Fatalf("artifact=%+v found=%v err=%v", artifact, found, err)
		}
		if err := service.VerifyArtifact(ctx, id, artifactID); err != nil {
			t.Fatal(err)
		}
		return artifact
	}
	first := run("training-attempt-one")
	second := run("training-attempt-two")
	if first.Lineage.ArtifactHash != second.Lineage.ArtifactHash {
		t.Fatalf("artifact hashes differ: %s != %s",
			first.Lineage.ArtifactHash, second.Lineage.ArtifactHash)
	}
	evaluationJobID, evaluationID, _, err := modeling.RequestEvaluation(
		ctx, id, domain.EvaluationRequest{
			ArtifactID: first.Lineage.ArtifactID, SnapshotID: manifest.SnapshotID,
			Purpose: "independent-validation", IdempotencyKey: "independent-evaluation",
		},
		first.Lineage.ArtifactID, first.Lineage.ArtifactHash,
		first.ModelName, manifest,
	)
	if err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)
	worked, err := service.Tick(ctx, "independent-validator")
	if err != nil || !worked {
		t.Fatalf("evaluation tick worked=%v err=%v", worked, err)
	}
	waitApplied(t, runtime, log)
	evaluationJob, found, err := modelprojection.ReadJob(ctx, st, id, evaluationJobID)
	if err != nil || !found || evaluationJob.State != "completed" {
		t.Fatalf("evaluation job=%+v found=%v err=%v", evaluationJob, found, err)
	}
	evaluation, found, err := modelprojection.ReadEvaluation(ctx, st, id, evaluationID)
	if err != nil || !found {
		t.Fatalf("evaluation=%+v found=%v err=%v", evaluation, found, err)
	}
	if evaluation.Manifest.ReportHash != first.Publication.EvaluationHash ||
		!evaluation.Manifest.Report.PassedLeakageChecks {
		t.Fatalf("independent evaluation = %+v", evaluation.Manifest)
	}
	model, found, err := decisionmodels.Read(ctx, st, id, "credit-risk-v1")
	if err != nil || !found || model.Lineage == nil || model.Lineage.SnapshotHash != rowsHash {
		t.Fatalf("model=%+v found=%v err=%v", model, found, err)
	}
}

func TestSnapshotWorkerPublishesVerifiedPointInTimeRows(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := eventlog.NewMemory()
	defer func() { _ = log.Close() }()
	st := store.NewMemory()
	runtime := projection.New(
		log, st, entities.Projector{}, features.Projector{},
		consent.Projector{}, modelprojection.Projector{},
	)
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "modeler"}
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	source := contextcmd.NewHandler(log).WithNow(func() time.Time { return now })
	modeling := modelingcmd.NewHandler(log).WithNow(func() time.Time { return now })
	consents := consent.NewHandler(log).WithNow(func() time.Time { return now })

	if _, err := source.DefineFeature(ctx, id, contextdomain.DefineFeature{
		Name: "txn_sum_72h", EntityType: "applicant", EventName: "transaction",
		Aggregation: contextdomain.AggSum, Field: "amount", WindowHours: 72,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RecordEntity(ctx, id, contextdomain.RecordEntity{
		EntityType: "applicant", EntityID: "a-1",
		Attributes: json.RawMessage(`{"region":"north"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RecordEntity(ctx, id, contextdomain.RecordEntity{
		EntityType: "applicant", EntityID: "excluded-1",
		Attributes: json.RawMessage(`{"region":"synthetic"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := consents.Grant(ctx, id, consent.GrantCmd{
		Subject: "applicant/a-1", Purpose: "credit-risk-validation",
		Basis: consent.BasisContract,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RecordEvent(ctx, id, contextdomain.RecordEvent{
		EntityType: "applicant", EntityID: "a-1", EventName: "transaction",
		EventID: "txn-1", Data: json.RawMessage(`{"amount":250}`),
		OccurredAt: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RecordEvent(ctx, id, contextdomain.RecordEvent{
		EntityType: "applicant", EntityID: "a-1", EventName: "outcome",
		EventID: "outcome-1", Data: json.RawMessage(`{"defaulted":true}`),
		OccurredAt: time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := modeling.DefineDataset(ctx, id, domain.DatasetSpec{
		Name: "credit-risk", OwnerTeam: "risk-science", EntityType: "applicant",
		Features: []string{"txn_sum_72h"},
		Label: domain.LabelSpec{
			EventName: "outcome", Field: "defaulted", Kind: domain.LabelBinary,
			PositiveValue: "true", HorizonHours: 72,
		},
		SegmentFields: []string{"region"}, Purpose: "credit-risk-validation",
		ExclusionRules: []domain.PopulationRule{{
			Name: "synthetic-source", Field: "region", Operator: domain.PopulationEquals,
			Value: json.RawMessage(`"synthetic"`), Reason: "synthetic records are not training data",
		}},
		ConsentRequirement: domain.ConsentRequirement{
			Mode: domain.ConsentActive, Purpose: "credit-risk-validation",
		},
		RetentionDays: 30,
		Partitions:    domain.PartitionSpec{TrainBPS: 7000, ValidationBPS: 1500, TestBPS: 1500},
	}); err != nil {
		t.Fatal(err)
	}
	knowledgeCutoff := now
	now = now.Add(24 * time.Hour)
	if _, err := consents.Withdraw(
		ctx, id, "applicant/a-1", "credit-risk-validation", "post-cutoff withdrawal",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RecordEntity(ctx, id, contextdomain.RecordEntity{
		EntityType: "applicant", EntityID: "a-1",
		Attributes: json.RawMessage(`{"region":"south"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RecordEntity(ctx, id, contextdomain.RecordEntity{
		EntityType: "applicant", EntityID: "a-2",
		Attributes: json.RawMessage(`{"region":"late-cohort"}`),
	}); err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)
	jobID, snapshotID, _, err := modeling.RequestSnapshot(ctx, id, domain.SnapshotRequest{
		DatasetName: "credit-risk", Version: 1,
		ObservationAt: time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC),
		KnowledgeAt:   knowledgeCutoff, IdempotencyKey: "snapshot-2026-01",
	}, map[string]int{"txn_sum_72h": 1})
	if err != nil {
		t.Fatal(err)
	}
	waitApplied(t, runtime, log)

	vault := erasure.NewVault(st).WithNow(func() time.Time { return now })
	service := New(
		modeling, st, WithNow(func() time.Time { return now }),
		WithContentSealer(vault),
	)
	worked, err := service.Tick(ctx, "worker-a")
	if err != nil || !worked {
		t.Fatalf("tick worked=%v err=%v", worked, err)
	}
	waitApplied(t, runtime, log)
	rows, manifest, err := service.ReadSnapshotRows(ctx, id, snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RowCount != 1 || manifest.LabelledCount != 1 ||
		manifest.CandidateCount != 2 || manifest.PopulationExcludedCount != 1 ||
		manifest.ConsentExcludedCount != 0 || manifest.FeatureCompleteness != 1 ||
		len(rows) != 1 {
		t.Fatalf("manifest=%+v rows=%+v", manifest, rows)
	}
	if rows[0].Features["txn_sum_72h"] != 250 || string(rows[0].Label) != "true" {
		t.Fatalf("row = %+v", rows[0])
	}
	if rows[0].Segments["region"] != "north" {
		t.Fatalf("point-in-time segment = %q, want north", rows[0].Segments["region"])
	}
	job, found, err := modelprojection.ReadJob(ctx, st, id, jobID)
	if err != nil || !found || job.State != "completed" {
		t.Fatalf("job=%+v found=%v err=%v", job, found, err)
	}
	if err := vault.Erase(ctx, id, "applicant/a-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ReadSnapshotRows(ctx, id, snapshotID); !errors.Is(
		err, errSnapshotContentErased,
	) {
		t.Fatalf("snapshot read after subject erasure = %v", err)
	}
}
