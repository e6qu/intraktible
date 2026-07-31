// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"testing"
	"time"

	contextfeatures "github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func TestGovernedFeatureJoinsSourceMaterializationCostAndConsumers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "viewer"}
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	definition := contextfeatures.FeatureView{
		Org: id.Org, Workspace: id.Workspace, Name: "risk_sum",
		EntityType: "applicant", EventName: "risk_signal", Aggregation: "sum",
		Field: "amount", WindowHours: 24, Version: 2, UpdatedAt: now.Add(-time.Hour),
	}
	if err := store.PutDoc(
		ctx, st, contextfeatures.Collection,
		store.Key(id.Org, id.Workspace, "applicant/risk_sum"), definition,
	); err != nil {
		t.Fatal(err)
	}
	ref := domain.SchemaRef{
		Kind: domain.SchemaKindEvent, EntityType: "applicant", EventName: "risk_signal",
	}
	approvedAt := now.Add(-2 * time.Hour)
	if err := store.PutDoc(
		ctx, st, modelprojection.CollectionSchemas,
		store.Key(id.Org, id.Workspace, ref.Key()),
		modelprojection.SchemaView{
			Org: id.Org, Workspace: id.Workspace, Ref: ref, ActiveVersion: 3,
			Versions: []modelprojection.SchemaVersionView{{
				Version: 3, Hash: "schema-hash", ApprovedAt: &approvedAt,
				Spec: domain.SchemaSpec{
					Ref:     ref,
					Quality: domain.QualityContract{FreshnessSeconds: 300},
				},
			}},
		},
	); err != nil {
		t.Fatal(err)
	}
	dataset := modelprojection.DatasetView{
		Org: id.Org, Workspace: id.Workspace, Name: "risk-development",
		Versions: []modelprojection.DatasetVersionView{{
			Version: 1, Spec: domain.DatasetSpec{
				EntityType: "applicant", Features: []string{"risk_sum"},
			},
		}},
	}
	if err := store.PutDoc(
		ctx, st, modelprojection.CollectionDatasets,
		store.Key(id.Org, id.Workspace, dataset.Name), dataset,
	); err != nil {
		t.Fatal(err)
	}
	manifest := domain.BackfillManifest{
		BackfillID: "backfill-1", EntityType: "applicant",
		Features: []string{"risk_sum"}, RowCount: 41, SizeBytes: 2048,
		PublishedAt: now.Add(-10 * time.Minute),
	}
	if err := store.PutDoc(
		ctx, st, modelprojection.CollectionMaterializations,
		store.Key(id.Org, id.Workspace, manifest.BackfillID),
		modelprojection.MaterializationView{
			Org: id.Org, Workspace: id.Workspace, JobID: "job-1", Manifest: manifest,
		},
	); err != nil {
		t.Fatal(err)
	}
	job := modelprojection.JobView{
		Org: id.Org, Workspace: id.Workspace, JobID: "job-1", Kind: "backfill",
		State: "completed", BackfillRequest: domain.BackfillRequest{
			EntityType: "applicant", Features: []string{"risk_sum"},
		},
		ComputeUnits: 41, EstimatedCostUSD: 0.000041, UpdatedAt: now,
	}
	if err := store.PutDoc(
		ctx, st, modelprojection.CollectionJobs,
		store.Key(id.Org, id.Workspace, job.JobID), job,
	); err != nil {
		t.Fatal(err)
	}

	items, err := New(nil, st).governedFeatures(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("features = %+v", items)
	}
	got := items[0]
	if got.SourceSchemaVersion != 3 || got.FreshnessSeconds != 300 ||
		got.Materialization != "completed" || got.Cardinality != 41 ||
		got.StorageBytes != 2048 || got.ComputeUnits != 41 ||
		len(got.DownstreamConsumers) != 1 ||
		got.DownstreamConsumers[0] != "dataset:risk-development@1" {
		t.Fatalf("governed feature = %+v", got)
	}
}
