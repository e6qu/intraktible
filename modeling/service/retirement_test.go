// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/context-layer/features"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// TestSchemaDependentsBlocksRetirementWhileADatasetUsesTheContract proves the
// dependent-aware retirement gate: an entity schema and the event schemas behind
// its features/label are all reported as dependants, and retirement is refused.
func TestSchemaDependentsBlocksRetirementWhileADatasetUsesTheContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "data-owner"}
	const entityType = "applicant"

	// A feature reading the risk_signal event, plus a dataset over the applicant
	// entity whose label reads the repayment event.
	if err := store.PutDoc(
		ctx, st, features.Collection,
		store.Key(id.Org, id.Workspace, entityType+"/risk_signal_count"),
		features.FeatureView{
			Name: "risk_signal_count", EntityType: entityType, EventName: "risk_signal",
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.PutDoc(
		ctx, st, modelprojection.CollectionDatasets,
		store.Key(id.Org, id.Workspace, "default-risk"),
		modelprojection.DatasetView{
			Org: id.Org, Workspace: id.Workspace, Name: "default-risk",
			Versions: []modelprojection.DatasetVersionView{{
				Version: 1,
				Spec: domain.DatasetSpec{
					EntityType: entityType,
					Features:   []string{"risk_signal_count"},
					Label:      domain.LabelSpec{EventName: "repayment", Field: "defaulted"},
				},
			}},
		},
	); err != nil {
		t.Fatal(err)
	}
	// An unrelated dataset over a different entity must not be reported.
	if err := store.PutDoc(
		ctx, st, modelprojection.CollectionDatasets,
		store.Key(id.Org, id.Workspace, "fraud-rules"),
		modelprojection.DatasetView{
			Org: id.Org, Workspace: id.Workspace, Name: "fraud-rules",
			Versions: []modelprojection.DatasetVersionView{{
				Version: 1,
				Spec: domain.DatasetSpec{
					EntityType: "transaction",
					Features:   []string{"amount"},
					Label:      domain.LabelSpec{EventName: "chargeback", Field: "amount"},
				},
			}},
		},
	); err != nil {
		t.Fatal(err)
	}

	svc := &Service{store: st}

	entityDependents, err := svc.schemaDependents(ctx, id, domain.SchemaRef{
		Kind: domain.SchemaKindEntity, EntityType: entityType,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entityDependents) != 1 || entityDependents[0] != "default-risk" {
		t.Fatalf("entity dependants = %v, want [default-risk]", entityDependents)
	}

	featureEventDependents, err := svc.schemaDependents(ctx, id, domain.SchemaRef{
		Kind: domain.SchemaKindEvent, EntityType: entityType, EventName: "risk_signal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(featureEventDependents) != 1 || featureEventDependents[0] != "default-risk" {
		t.Fatalf("feature-event dependants = %v, want [default-risk]", featureEventDependents)
	}

	labelEventDependents, err := svc.schemaDependents(ctx, id, domain.SchemaRef{
		Kind: domain.SchemaKindEvent, EntityType: entityType, EventName: "repayment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(labelEventDependents) != 1 || labelEventDependents[0] != "default-risk" {
		t.Fatalf("label-event dependants = %v, want [default-risk]", labelEventDependents)
	}

	// An event no dataset references is not a dependant.
	unrelated, err := svc.schemaDependents(ctx, id, domain.SchemaRef{
		Kind: domain.SchemaKindEvent, EntityType: entityType, EventName: "marketing_click",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated event dependants = %v, want none", unrelated)
	}

	// The error reported to a retiring operator names the dependant.
	if err := dependantsError(entityDependents); err == nil {
		t.Fatal("dependantsError returned nil for a non-empty dependant list")
	}
}
