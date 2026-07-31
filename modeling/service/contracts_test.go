// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	contextdomain "github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func TestContractAdapterCarriesUniqueAndRelationshipChecksToAppendBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "data-owner"}
	ref := domain.SchemaRef{Kind: domain.SchemaKindEntity, EntityType: "applicant"}
	spec := domain.SchemaSpec{
		Ref: ref, OwnerTeam: "risk-data", Purposes: []string{"underwriting"},
		Compatibility: domain.CompatibilityBackward,
		Fields: []domain.FieldSpec{
			{
				Name: "government_id", Type: domain.FieldString, Required: true,
				Identifier: true, Classification: domain.ClassificationRestricted,
			},
			{
				Name: "employer_id", Type: domain.FieldString,
				Classification: domain.ClassificationConfidential,
			},
		},
		Relationships: []domain.Relationship{{
			Field: "employer_id", TargetEntityType: "employer",
		}},
		Quality: domain.QualityContract{
			Action: domain.QualityRefer, UniqueFields: []string{"government_id"},
		},
	}
	hash, err := domain.HashSchema(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutDoc(
		ctx, st, modelprojection.CollectionSchemas,
		store.Key(id.Org, id.Workspace, ref.Key()),
		modelprojection.SchemaView{
			Org: id.Org, Workspace: id.Workspace, Ref: ref, ActiveVersion: 1,
			Versions: []modelprojection.SchemaVersionView{{
				Version: 1, Spec: spec, Hash: hash,
			}},
		},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	adapter := New(nil, st, WithNow(func() time.Time { return now }))
	evidence, err := adapter.ValidateEntity(
		ctx, id, ref.EntityType, 1,
		json.RawMessage(`{"government_id":"ID-1","employer_id":"EMP-1"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Hash != hash || evidence.EvaluatedAt != now ||
		!strings.HasPrefix(evidence.UniqueClaim, "source.unique\x00entity/applicant\x00") {
		t.Fatalf("evidence = %+v", evidence)
	}
	wantRelationships := []contextdomain.RelationshipCheck{{
		Field: "employer_id", TargetEntityType: "employer", TargetEntityID: "EMP-1",
	}}
	if len(evidence.RelationshipChecks) != 1 ||
		evidence.RelationshipChecks[0] != wantRelationships[0] {
		t.Fatalf("relationship checks = %+v, want %+v", evidence.RelationshipChecks, wantRelationships)
	}
}

func TestApprovedStalePinsPolicyApprovalAndRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "producer"}
	ref := domain.SchemaRef{
		Kind: domain.SchemaKindEvent, EntityType: "applicant", EventName: "risk",
	}
	spec := domain.SchemaSpec{
		Ref: ref, OwnerTeam: "risk-data", Purposes: []string{"underwriting"},
		Compatibility: domain.CompatibilityBackward,
		Fields: []domain.FieldSpec{{
			Name: "score", Type: domain.FieldNumber, Required: true,
			Classification: domain.ClassificationConfidential,
		}},
		Quality: domain.QualityContract{
			Action: domain.QualityApprovedStale, FreshnessSeconds: 60,
		},
	}
	hash, err := domain.HashSchema(spec)
	if err != nil {
		t.Fatal(err)
	}
	approvedAt := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	if err := store.PutDoc(
		ctx, st, modelprojection.CollectionSchemas,
		store.Key(id.Org, id.Workspace, ref.Key()),
		modelprojection.SchemaView{
			Org: id.Org, Workspace: id.Workspace, Ref: ref, ActiveVersion: 1,
			Versions: []modelprojection.SchemaVersionView{{
				Version: 1, Spec: spec, Hash: hash, ApprovedBy: "independent-checker",
				ApprovedAt: &approvedAt, ApprovalRequestID: "approval-1",
				ApprovalReason: "Stale risk values may be used for at most one hour.",
			}},
		},
	); err != nil {
		t.Fatal(err)
	}
	now := approvedAt.Add(2 * time.Hour)
	adapter := New(nil, st, WithNow(func() time.Time { return now }))
	evidence, err := adapter.ValidateEvent(
		ctx, id, ref.EntityType, ref.EventName, now.Add(-2*time.Hour), 1,
		json.RawMessage(`{"score":0.71}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Violations) != 1 || evidence.Violations[0].Code != "freshness" ||
		evidence.Action != string(domain.QualityApprovedStale) ||
		evidence.PolicyApprovalID != "approval-1" ||
		evidence.PolicyApprovedBy != "independent-checker" ||
		!evidence.PolicyApprovedAt.Equal(approvedAt) ||
		evidence.PolicyApprovalReason == "" {
		t.Fatalf("approved stale evidence = %+v", evidence)
	}
	if _, err := adapter.ValidateEvent(
		ctx, id, ref.EntityType, ref.EventName, now.Add(-2*time.Hour), 1,
		json.RawMessage(`{"score":"unknown"}`),
	); err == nil || !strings.Contains(err.Error(), "rejected by schema") {
		t.Fatalf("invalid approved_stale document = %v", err)
	}
}
