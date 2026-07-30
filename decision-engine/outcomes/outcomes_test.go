// SPDX-License-Identifier: AGPL-3.0-or-later

package outcomes_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/experiments"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/outcomes"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func appendJSON(t *testing.T, log eventlog.Log, id identity.Identity, stream, typ string, at time.Time, payload any) eventlog.Envelope {
	t.Helper()
	event, err := eventlog.AppendJSON(context.Background(), log, id.Org, id.Workspace, id.Actor, stream, typ, at, payload)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestOutcomeDerivesLineageAndCorrectsIdempotently(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	clock := now
	decisionID := "decision-1"
	if err := store.PutDoc(ctx, st, history.Collection, store.Key(id.Org, id.Workspace, decisionID), history.Record{
		Org: id.Org, Workspace: id.Workspace, DecisionID: decisionID,
		FlowID: "flow-1", Slug: "offers", Version: 2,
		Environment: "production", Status: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutDoc(ctx, st, experiments.ExposureCollection, store.Key(id.Org, id.Workspace, decisionID), experiments.Exposure{
		Org: id.Org, Workspace: id.Workspace, ExperimentID: "exp-1", Cohort: 3,
		DecisionID: decisionID, FlowID: "flow-1", Environment: "production",
		ArmKey: "offer-b", ArmName: "Offer B", ArmKind: experiments.ArmChallenger,
		Version: 2, SubjectHash: "digest", ReachedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	appendJSON(t, log, id, events.StreamModels, events.TypeModelDefined, now.Add(-3*time.Hour),
		events.ModelDefined{Name: "risk", Spec: json.RawMessage(`{"kind":"logistic"}`)})
	appendJSON(t, log, id, events.StreamDecisions, events.TypeDecisionStarted, now.Add(-2*time.Hour),
		events.DecisionStarted{DecisionID: decisionID, FlowID: "flow-1", Slug: "offers", Version: 2, Environment: "production"})
	appendJSON(t, log, id, events.StreamDecisions, events.TypeNodeEvaluated, now.Add(-90*time.Minute),
		events.NodeEvaluated{
			DecisionID: decisionID, NodeID: "predict-risk", NodeType: events.NodePredict,
			Output: json.RawMessage(`{"risk":{"model":"risk","probability":0.73}}`),
		})
	appendJSON(t, log, id, events.StreamDecisions, events.TypeDecisionCompleted, now.Add(-time.Hour),
		events.DecisionCompleted{DecisionID: decisionID, FlowID: "flow-1", Version: 2})

	handler := outcomes.NewHandler(log, st).WithNow(func() time.Time { return clock })
	command := outcomes.RecordCommand{
		DecisionID: decisionID, Key: "converted", Kind: outcomes.KindBinary, Value: 1,
		EventTime: now, ObservationWindowDays: 30,
		Source:       outcomes.Source{System: "loan-core", RecordID: "loan-42", Lineage: "settlement"},
		LabelVersion: "conversion-v1",
	}
	recorded, event, err := handler.Record(ctx, id, command, "outcome-key")
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Treatment == nil || recorded.Treatment.ExperimentID != "exp-1" ||
		recorded.Treatment.ArmKey != "offer-b" || recorded.FlowVersion != 2 {
		t.Fatalf("treatment was not derived from the decision: %+v", recorded)
	}
	if len(recorded.Predictions) != 1 || recorded.Predictions[0].Model != "risk" ||
		recorded.Predictions[0].ModelVersion != 1 ||
		recorded.Predictions[0].Probability != 0.73 {
		t.Fatalf("model facts were not derived from the trace: %+v", recorded.Predictions)
	}
	if err := (outcomes.Projector{}).Apply(ctx, event, st); err != nil {
		t.Fatal(err)
	}
	retry, retryEvent, err := handler.Record(ctx, id, command, "outcome-key")
	if err != nil || retry.OutcomeID != recorded.OutcomeID || retryEvent.Seq != 0 {
		t.Fatalf("record retry was not idempotent: %+v event=%+v err=%v", retry, retryEvent, err)
	}
	conflict := command
	conflict.Value = 0
	if _, _, err := handler.Record(ctx, id, conflict, "different-key"); err == nil {
		t.Fatal("a second current fact for the same decision and metric must be rejected")
	}

	correctionCommand := outcomes.RecordCommand{
		Value: 0, EventTime: now.Add(time.Hour), ObservationWindowDays: 30,
		Source:       outcomes.Source{System: "loan-core", RecordID: "loan-42", Lineage: "reversal"},
		LabelVersion: "conversion-v1",
	}
	clock = correctionCommand.EventTime
	correction, correctionEvent, err := handler.Correct(
		ctx, id, recorded.OutcomeID, correctionCommand, "payment reversed", "correction-key",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := (outcomes.Projector{}).Apply(ctx, correctionEvent, st); err != nil {
		t.Fatal(err)
	}
	if correction.Revision != 2 {
		t.Fatalf("correction revision = %d, want 2", correction.Revision)
	}
	retriedCorrection, retryCorrectionEvent, err := handler.Correct(
		ctx, id, recorded.OutcomeID, correctionCommand, "payment reversed", "correction-key",
	)
	if err != nil || retriedCorrection.Revision != 2 || retryCorrectionEvent.Seq != 0 {
		t.Fatalf("correction retry was not idempotent: %+v event=%+v err=%v", retriedCorrection, retryCorrectionEvent, err)
	}
	view, ok, err := outcomes.Read(ctx, st, id, recorded.OutcomeID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if view.Current.Value != 0 || len(view.History) != 2 || view.History[0].Value != 1 {
		t.Fatalf("correction history is incomplete: %+v", view)
	}
}

func TestOutcomeRejectsUnlinkedAndInvalidFacts(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	now := time.Now().UTC()
	handler := outcomes.NewHandler(log, st).WithNow(func() time.Time { return now })
	_, _, err := handler.Record(ctx, id, outcomes.RecordCommand{
		DecisionID: "missing", Key: "defaulted", Kind: outcomes.KindBinary,
		Value: 1, EventTime: now, Source: outcomes.Source{System: "core", RecordID: "1"},
		LabelVersion: "v1",
	}, "key")
	if err == nil {
		t.Fatal("an unknown decision must be rejected")
	}
}
