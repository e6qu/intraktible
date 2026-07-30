// SPDX-License-Identifier: AGPL-3.0-or-later

package experiments_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/experiments"
	"github.com/e6qu/intraktible/decision-engine/outcomes"
	"github.com/e6qu/intraktible/platform/entity"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func validSpec() experiments.Spec {
	return experiments.Spec{
		Name: "Offer conversion", Hypothesis: "the challenger increases conversion",
		Owner: "product@example.com", FlowID: "flow-1", Environment: "sandbox",
		SubjectKeyExpression: "customer_id", EligibilityExpression: "eligible",
		Salt: "cohort-salt",
		Arms: []experiments.Arm{
			{Key: "control", Name: "Control", Kind: experiments.ArmChampion, Version: 1, AllocationBPS: 5000},
			{Key: "offer-b", Name: "Offer B", Kind: experiments.ArmChallenger, Version: 2, AllocationBPS: 5000},
		},
		PrimaryMetric: experiments.Metric{
			Key: "converted", Name: "Conversion", Kind: experiments.MetricBinary,
			Direction: experiments.DirectionIncrease,
		},
		Guardrails: []experiments.Metric{{
			Key: "loss", Name: "Loss", Kind: experiments.MetricContinuous,
			Direction: experiments.DirectionDecrease, MaxRegression: 5,
		}},
		MinimumSamplePerArm: 10, MinimumEffect: 0.02, Confidence: 0.95,
		ObservationWindowDays: 30,
	}
}

func TestAssignmentIsStableAcrossRetriesAndReplicas(t *testing.T) {
	spec := validSpec()
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"customer_id": "customer-42", "eligible": true}
	first, eligible, err := experiments.Assign(spec, "exp-1", 7, input, entity.Ref{})
	if err != nil || !eligible {
		t.Fatalf("first assignment: %+v eligible=%v err=%v", first, eligible, err)
	}
	for i := 0; i < 100; i++ {
		got, eligible, err := experiments.Assign(spec, "exp-1", 7, input, entity.Ref{})
		if err != nil || !eligible || got != first {
			t.Fatalf("retry %d crossed arms: got %+v, want %+v, err=%v", i, got, first, err)
		}
	}
	next, eligible, err := experiments.Assign(spec, "exp-1", 8, input, entity.Ref{})
	if err != nil || !eligible {
		t.Fatal(err)
	}
	if next.SubjectHash == first.SubjectHash {
		t.Fatal("a new cohort must have a new subject digest namespace")
	}
}

func TestAssignmentHonorsEligibilityAndAllocation(t *testing.T) {
	spec := validSpec()
	if _, eligible, err := experiments.Assign(
		spec, "exp-1", 1,
		map[string]any{"customer_id": "x", "eligible": false}, entity.Ref{},
	); err != nil || eligible {
		t.Fatalf("ineligible subject assigned: eligible=%v err=%v", eligible, err)
	}
	counts := map[string]int{}
	for i := 0; i < 10_000; i++ {
		assignment, eligible, err := experiments.Assign(
			spec, "exp-1", 1,
			map[string]any{"customer_id": fmt.Sprintf("subject-%d", i), "eligible": true},
			entity.Ref{},
		)
		if err != nil || !eligible {
			t.Fatalf("subject %d: eligible=%v err=%v", i, eligible, err)
		}
		counts[assignment.ArmKey]++
	}
	if counts["control"] < 4700 || counts["control"] > 5300 {
		t.Fatalf("unexpected deterministic allocation: %+v", counts)
	}
}

func TestAnalysisWinnerAndSafetyStates(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "analyst"}
	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	build := func(t *testing.T, controlWins, treatmentWins int) (*store.Memory, string) {
		t.Helper()
		st := store.NewMemory()
		spec := validSpec()
		spec.Guardrails = nil
		experimentID := "exp-1"
		view := experiments.View{
			Org: id.Org, Workspace: id.Workspace, ExperimentID: experimentID,
			Cohort: 1, State: experiments.StateCompleted, Spec: spec,
		}
		if err := store.PutDoc(ctx, st, experiments.Collection, store.Key(id.Org, id.Workspace, experimentID), view); err != nil {
			t.Fatal(err)
		}
		addArm := func(arm experiments.Arm, wins int) {
			for i := 0; i < 100; i++ {
				decisionID := fmt.Sprintf("%s-%d", arm.Key, i)
				exposure := experiments.Exposure{
					Org: id.Org, Workspace: id.Workspace, ExperimentID: experimentID,
					Cohort: 1, DecisionID: decisionID, FlowID: spec.FlowID,
					Environment: spec.Environment, ArmKey: arm.Key, ArmName: arm.Name,
					ArmKind: arm.Kind, Version: arm.Version,
					SubjectHash: fmt.Sprintf("hash-%s-%d", arm.Key, i), ReachedAt: base,
				}
				if err := store.PutDoc(ctx, st, experiments.ExposureCollection, store.Key(id.Org, id.Workspace, decisionID), exposure); err != nil {
					t.Fatal(err)
				}
				value := 0.0
				if i < wins {
					value = 1
				}
				revision := outcomes.Revision{
					Revision: 1, Value: value, EventTime: base.Add(time.Hour),
					LabelVersion: "conversion-v1",
				}
				outcome := outcomes.View{
					OutcomeID: "out-" + decisionID, DecisionID: decisionID,
					Key: spec.PrimaryMetric.Key, Kind: outcomes.KindBinary,
					FlowID: spec.FlowID, FlowVersion: arm.Version, Environment: spec.Environment,
					Treatment: &outcomes.TreatmentFact{
						ExperimentID: experimentID, Cohort: 1, ArmKey: arm.Key,
						ArmName: arm.Name, ArmKind: string(arm.Kind), Version: arm.Version,
					},
					Current: revision, History: []outcomes.Revision{revision},
				}
				if err := store.PutDoc(ctx, st, outcomes.Collection, store.Key(id.Org, id.Workspace, outcome.OutcomeID), outcome); err != nil {
					t.Fatal(err)
				}
			}
		}
		addArm(spec.Arms[0], controlWins)
		addArm(spec.Arms[1], treatmentWins)
		return st, experimentID
	}

	st, experimentID := build(t, 20, 45)
	report, err := experiments.Analyze(ctx, st, id, experimentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != experiments.StatusWinner || report.WinnerArmKey != "offer-b" {
		t.Fatalf("expected a valid winner, got %+v", report)
	}

	st, experimentID = build(t, 20, 22)
	report, err = experiments.Analyze(ctx, st, id, experimentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status == experiments.StatusWinner {
		t.Fatalf("small effect must not be called a winner: %+v", report)
	}
}

func TestAnalysisRefusesUnderpoweredAndSRMMismatchedCohorts(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "analyst"}
	st := store.NewMemory()
	spec := validSpec()
	view := experiments.View{
		Org: id.Org, Workspace: id.Workspace, ExperimentID: "exp",
		Cohort: 1, State: experiments.StateRunning, Spec: spec,
	}
	if err := store.PutDoc(ctx, st, experiments.Collection, store.Key(id.Org, id.Workspace, "exp"), view); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 100; i++ {
		arm := spec.Arms[0]
		if i >= 99 {
			arm = spec.Arms[1]
		}
		decisionID := fmt.Sprintf("d-%d", i)
		exposure := experiments.Exposure{
			Org: id.Org, Workspace: id.Workspace, ExperimentID: "exp", Cohort: 1,
			DecisionID: decisionID, ArmKey: arm.Key, ArmName: arm.Name,
			ArmKind: arm.Kind, Version: arm.Version, ReachedAt: now,
		}
		if err := store.PutDoc(ctx, st, experiments.ExposureCollection, store.Key(id.Org, id.Workspace, decisionID), exposure); err != nil {
			t.Fatal(err)
		}
	}
	report, err := experiments.Analyze(ctx, st, id, "exp")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != experiments.StatusInvalid {
		t.Fatalf("SRM mismatch must be invalid, got %+v", report)
	}
}

func TestCollectingAnalysisHasCompleteEmptyMetricShape(t *testing.T) {
	ctx := context.Background()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "analyst"}
	st := store.NewMemory()
	spec := validSpec()
	if err := store.PutDoc(ctx, st, experiments.Collection, store.Key(id.Org, id.Workspace, "empty"), experiments.View{
		Org: id.Org, Workspace: id.Workspace, ExperimentID: "empty",
		Cohort: 1, State: experiments.StateDraft, Spec: spec,
	}); err != nil {
		t.Fatal(err)
	}
	report, err := experiments.Analyze(ctx, st, id, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != experiments.StatusCollecting ||
		len(report.Primary.Arms) != len(spec.Arms) ||
		report.Primary.Comparisons == nil ||
		len(report.Guardrails) != len(spec.Guardrails) ||
		report.Guardrails[0].Arms == nil ||
		report.Guardrails[0].Comparisons == nil {
		t.Fatalf("collecting analysis shape = %+v", report)
	}
}

func TestStopWindowSchedulerCompletesOnceAcrossProjectionLag(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := eventlog.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "maker"}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	stopAt := now.Add(time.Hour)
	spec := validSpec()
	spec.StopAt = &stopAt
	appendEvent := func(eventType string, payload any, at time.Time) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := log.Append(ctx, eventlog.Envelope{
			Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
			Stream: experiments.Stream, Type: eventType, Time: at, Payload: raw,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(experiments.TypeCreated, experiments.Created{
		ExperimentID: "exp-window", Spec: spec,
	}, now)
	appendEvent(experiments.TypeStarted, experiments.Transition{
		ExperimentID: "exp-window", Cohort: 1,
	}, now.Add(time.Second))

	st := store.NewMemory()
	runtime := projection.New(log, st, experiments.Projector{})
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wait := func() {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for runtime.Applied() < log.Head() {
			if err := runtime.Err(); err != nil {
				t.Fatal(err)
			}
			if time.Now().After(deadline) {
				t.Fatalf("projection stopped at %d, log head %d", runtime.Applied(), log.Head())
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait()
	handler := experiments.NewHandler(log, st).WithNow(func() time.Time { return now })
	scheduler := experiments.NewScheduler(handler, st).WithNow(func() time.Time { return now })
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if log.Head() != 2 {
		t.Fatalf("experiment closed before stop window: head=%d", log.Head())
	}

	now = stopAt
	// Run twice before awaiting the projection. The second tick observes the
	// stale running view but reconciles the already-completed aggregate.
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	wait()
	view, ok, err := experiments.Read(ctx, st, id, "exp-window")
	if err != nil || !ok || view.State != experiments.StateCompleted {
		t.Fatalf("scheduled completion: ok=%v err=%v view=%+v", ok, err, view)
	}
	all, err := log.ReadTenantStream(ctx, id.Org, id.Workspace, experiments.Stream, 0)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for _, event := range all {
		if event.Type == experiments.TypeCompleted {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("completion facts = %d, want one", completed)
	}
}
