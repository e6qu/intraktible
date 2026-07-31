// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	contextevents "github.com/e6qu/intraktible/context-layer/events"
	modelingcmd "github.com/e6qu/intraktible/modeling/command"
	"github.com/e6qu/intraktible/modeling/domain"
	modelingevents "github.com/e6qu/intraktible/modeling/events"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestQualityIncidentRequiresAcknowledgementAndProjectsImpact(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator-1"}
	ref := domain.SchemaRef{Kind: domain.SchemaKindEvent, EntityType: "applicant", EventName: "risk"}
	spec := domain.SchemaSpec{
		Ref: ref, OwnerTeam: "risk-data", Purposes: []string{"underwriting"},
		Compatibility: domain.CompatibilityBackward,
		Fields: []domain.FieldSpec{{
			Name: "score", Type: domain.FieldNumber, Required: true,
			Classification: domain.ClassificationConfidential,
		}},
		Quality: domain.QualityContract{Action: domain.QualityRefer},
	}
	hash, err := domain.HashSchema(spec)
	if err != nil {
		t.Fatal(err)
	}
	appendFact := func(stream string, typ string, payload any) eventlog.Envelope {
		envelope, appendErr := eventlog.AppendJSON(
			ctx, log, id.Org, id.Workspace, "data-owner", stream, typ, now, payload,
		)
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		now = now.Add(time.Second)
		return envelope
	}
	appendFact(modelingevents.StreamModeling, modelingevents.TypeSchemaVersionDefined,
		modelingevents.SchemaVersionDefined{Ref: ref, Version: 1, Spec: spec, Hash: hash})
	appendFact(modelingevents.StreamModeling, modelingevents.TypeDatasetDefined,
		modelingevents.DatasetDefined{
			Name: "risk-development", Version: 1,
			Spec: domain.DatasetSpec{Name: "risk-development", EntityType: "applicant"},
			Hash: "dataset-hash",
		})
	record := appendFact(contextevents.StreamContext, contextevents.TypeEventRecorded,
		contextevents.EventRecorded{
			EntityType: "applicant", EntityID: "app-1", EventName: "risk",
			EventID: "source-risk-1", OccurredAt: now.Add(-time.Hour), ReceivedAt: now,
			Data: json.RawMessage(`{}`),
			SchemaEvidence: contextevents.SchemaEvidence{
				Version: 1, Hash: hash, Action: "refer", EvaluatedAt: now,
				Violations: []contextevents.QualityViolation{{
					Code: "required", Field: "score", Message: "score is required",
				}},
			},
		})

	handler := modelingcmd.NewHandler(log).WithNow(func() time.Time { return now })
	if _, err := handler.ResolveQualityIncident(
		ctx, id, record.ID, "fixed before triage",
	); err == nil || !strings.Contains(err.Error(), "must be acknowledged") {
		t.Fatalf("unacknowledged resolution error = %v", err)
	}
	if _, err := handler.AcknowledgeQualityIncident(
		ctx, id, record.ID, "Risk Data owns remediation",
	); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := handler.ResolveQualityIncident(
		ctx, id, record.ID, "source corrected and affected snapshot rebuilt",
	); err != nil {
		t.Fatal(err)
	}

	st := store.NewMemory()
	if _, err := projection.New(log, st, modelprojection.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	incidents, err := modelprojection.ListQualityIncidents(ctx, st, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents) != 1 {
		t.Fatalf("incidents = %+v", incidents)
	}
	incident := incidents[0]
	if incident.Status != "resolved" || incident.AcknowledgedBy != id.Actor ||
		incident.ResolvedBy != id.Actor || incident.OwnerTeam != "risk-data" ||
		incident.SourceEventID != "source-risk-1" ||
		len(incident.AffectedSubjects) != 1 ||
		incident.AffectedSubjects[0] != "applicant/app-1" ||
		!contains(incident.AffectedAssets, "schema:event/applicant/risk") ||
		!contains(incident.AffectedAssets, "dataset:risk-development") {
		t.Fatalf("incident lifecycle and impact = %+v", incident)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
