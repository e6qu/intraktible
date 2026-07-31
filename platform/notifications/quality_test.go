// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications_test

import (
	"context"
	"testing"
	"time"

	contextevents "github.com/e6qu/intraktible/context-layer/events"
	modelingevents "github.com/e6qu/intraktible/modeling/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestQualityIncidentAlertsOperatorThroughAcknowledgementAndResolution(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	record, err := eventlog.AppendJSON(
		ctx, log, "demo", "main", "source-writer", contextevents.StreamContext,
		contextevents.TypeEntityRecorded, now, contextevents.EntityRecorded{
			EntityType: "applicant", EntityID: "app-1",
			SchemaEvidence: contextevents.SchemaEvidence{
				Version: 1, Hash: "schema-hash", Action: "refer",
				Violations: []contextevents.QualityViolation{{
					Code: "required", Field: "income", Message: "income is required",
				}},
				EvaluatedAt: now,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.AppendJSON(
		ctx, log, "demo", "main", "operator-1", modelingevents.StreamModeling,
		modelingevents.TypeQualityIncidentAcknowledged, now.Add(time.Second),
		modelingevents.QualityIncidentAcknowledged{
			IncidentID: record.ID, Note: "Data Ops owns remediation",
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.AppendJSON(
		ctx, log, "demo", "main", "operator-1", modelingevents.StreamModeling,
		modelingevents.TypeQualityIncidentResolved, now.Add(2*time.Second),
		modelingevents.QualityIncidentResolved{
			IncidentID: record.ID, Reason: "source corrected",
		},
	); err != nil {
		t.Fatal(err)
	}

	st := store.NewMemory()
	if _, err := projection.New(log, st, notifications.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	operator := identity.Identity{
		Org: "demo", Workspace: "main", Actor: "operator-2",
	}
	inbox, err := notifications.List(
		ctx, st, operator, notifications.Access{OperatorAlerts: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 0 {
		t.Fatalf("resolved quality incident remained actionable: %+v", inbox)
	}
	all, err := store.ListDocs[notifications.View](
		ctx, st, notifications.Collection, store.Key("demo", "main", notifications.OperatorQueue+":"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !all[0].Resolved ||
		all[0].ActionID != record.ID ||
		all[0].SubjectType != "quality_incident" {
		t.Fatalf("quality alert lifecycle = %+v", all)
	}
}
