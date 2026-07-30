// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	intraktible "github.com/e6qu/intraktible/client"
	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/assertions"
	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/experiments"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/outcomes"
	"github.com/e6qu/intraktible/decision-engine/population"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	engineservice "github.com/e6qu/intraktible/decision-engine/service"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

// TestExperimentOutcomeAndPopulationHTTPJourney proves that the public API and
// SDK compose into one replayable journey: immutable versions become a stable
// treatment, only completed decisions become exposures, business facts retain
// correction history, and record-free population work survives as a durable
// result manifest.
func TestExperimentOutcomeAndPopulationHTTPJourney(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "maker"}
	clock := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	flowHandler := command.NewHandler(log)
	experimentHandler := experiments.NewHandler(log, st)
	decideHandler := command.NewDecideHandler(log, st, command.WithExperiments(experimentHandler))
	outcomeHandler := outcomes.NewHandler(log, st).WithNow(func() time.Time { return clock })
	populationHandler := population.NewHandler(log, st, decideHandler, experimentHandler)
	engine := engineservice.New(
		flowHandler, decideHandler, preapproval.NewHandler(log), st,
	)
	routes := func(mux *http.ServeMux) {
		engine.Routes(mux)
		experiments.New(experimentHandler, st, flowHandler).Routes(mux)
		outcomes.New(outcomeHandler, st).Routes(mux)
		population.New(populationHandler, st).Routes(mux)
	}
	api := testutil.StartAPI(
		t, log, st, "test-key", id, routes,
		flows.Projector{}, history.Projector{}, analytics.Projector{},
		experiments.Projector{}, outcomes.Projector{}, population.Projector{},
		assertions.Projector{},
	)

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "offers", "name": "Offers"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("control")}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("treatment")}, http.StatusCreated, nil)

	sdk := intraktible.New(api.Server.URL, api.Key, intraktible.WithHTTPClient(api.Server.Client()))
	experimentID, err := sdk.CreateExperiment(ctx, intraktible.ExperimentSpec{
		Name: "Offer conversion", Hypothesis: "the treatment increases conversion",
		Owner: id.Actor, FlowID: created.FlowID, Environment: "sandbox",
		SubjectKeyExpression: "customer_id", Salt: "offer-conversion-v1",
		Arms: []intraktible.ExperimentArm{
			{Key: "control", Name: "Control", Kind: "champion", Version: 1, AllocationBPS: 5000},
			{Key: "treatment", Name: "Treatment", Kind: "challenger", Version: 2, AllocationBPS: 5000},
		},
		PrimaryMetric: intraktible.ExperimentMetric{
			Key: "converted", Name: "Converted", Kind: "binary", Direction: "increase",
		},
		MinimumSamplePerArm: 2, MinimumEffect: 0.01, Confidence: 0.95,
		ObservationWindowDays: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	var (
		projectedExperiment intraktible.Experiment
		experimentReadErr   error
	)
	if !testutil.Eventually(t, func() bool {
		projectedExperiment, experimentReadErr = sdk.GetExperiment(ctx, experimentID)
		return experimentReadErr == nil
	}) {
		t.Fatalf(
			"created experiment was not projected before its transition: experiment=%+v read_err=%v",
			projectedExperiment, experimentReadErr,
		)
	}
	if err := sdk.TransitionExperiment(ctx, experimentID, intraktible.ExperimentStart, ""); err != nil {
		t.Fatal(err)
	}

	first, err := sdk.Decide(ctx, "offers", "sandbox", intraktible.DecideRequest{
		Data: map[string]any{"customer_id": "customer-42"}, IdempotencyKey: "decision-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := sdk.Decide(ctx, "offers", "sandbox", intraktible.DecideRequest{
		Data: map[string]any{"customer_id": "customer-42"}, IdempotencyKey: "decision-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.DecisionID == second.DecisionID || first.ExperimentID != experimentID ||
		first.ExperimentArm == "" || first.ExperimentArm != second.ExperimentArm ||
		first.ExperimentCohort != 1 || second.ExperimentCohort != 1 {
		t.Fatalf("unstable or missing cohort assignment: first=%+v second=%+v", first, second)
	}

	observedAt := clock.Add(-time.Hour)
	outcomeID, err := sdk.RecordOutcome(ctx, intraktible.OutcomeRecord{
		DecisionID: first.DecisionID, Key: "converted", Kind: "binary", Value: 0,
		EventTime: observedAt, Source: intraktible.OutcomeSource{
			System: "warehouse", RecordID: "conversion-42", Lineage: "orders.v3",
		},
		LabelVersion: "conversion-v1",
	}, "outcome-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := sdk.CorrectOutcome(ctx, outcomeID, intraktible.OutcomeCorrection{
		Value: 1, EventTime: observedAt.Add(time.Hour),
		Source:       intraktible.OutcomeSource{System: "warehouse", RecordID: "conversion-42-r2"},
		LabelVersion: "conversion-v2", Reason: "late settlement",
	}, "outcome-correction-1"); err != nil {
		t.Fatal(err)
	}
	recorded, err := sdk.ListOutcomes(ctx, first.DecisionID, "converted")
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || len(recorded[0].History) != 2 ||
		!strings.Contains(string(recorded[0].Treatment), experimentID) {
		t.Fatalf("outcome lost treatment or correction lineage: %+v", recorded)
	}
	report, err := sdk.ExperimentAnalysis(ctx, experimentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status == "winner" {
		t.Fatalf("underpowered cohort was declared a winner: %+v", report)
	}

	jobID, err := sdk.CreatePopulationJob(ctx, intraktible.PopulationJobCreate{
		Kind: "backtest", Slug: "offers", Environment: "sandbox",
		Items: []intraktible.PopulationItem{
			{Data: map[string]any{"customer_id": "population-a"}},
			{Data: map[string]any{"customer_id": "population-b"}},
			{Data: map[string]any{"customer_id": "population-c"}},
		},
		MaxAttempts: 2, Concurrency: 2, RetentionDays: 7,
	}, "population-1")
	if err != nil {
		t.Fatal(err)
	}
	var (
		job     intraktible.PopulationJob
		tickErr error
		readErr error
	)
	completed := testutil.EventuallyWithin(t, 3*time.Second, func() bool {
		if _, tickErr = populationHandler.Tick(ctx, "worker-e2e"); tickErr != nil {
			return true
		}
		job, readErr = sdk.GetPopulationJob(ctx, jobID)
		return readErr == nil && job.State == "completed"
	})
	if tickErr != nil {
		t.Fatal(tickErr)
	}
	if !completed {
		t.Fatalf("population job was not projected as completed: job=%+v read_err=%v", job, readErr)
	}
	if job.State != "completed" || job.Succeeded != 3 || job.Failed != 0 {
		t.Fatalf("population job did not finish exactly once: %+v", job)
	}
	results, err := sdk.PopulationResults(ctx, jobID)
	if err != nil || strings.Count(string(results), "\n") != 3 {
		t.Fatalf("terminal NDJSON manifest: rows=%q err=%v", results, err)
	}

	replayed := store.NewMemory()
	if err := projection.New(
		log, replayed, flows.Projector{}, history.Projector{},
		experiments.Projector{}, outcomes.Projector{}, population.Projector{},
	).Start(ctx); err != nil {
		t.Fatal(err)
	}
	experiment, ok, err := experiments.Read(ctx, replayed, id, experimentID)
	if err != nil || !ok || experiment.State != experiments.StateRunning {
		t.Fatalf("experiment replay: ok=%v view=%+v err=%v", ok, experiment, err)
	}
	replayedOutcomes, err := outcomes.List(ctx, replayed, id, first.DecisionID, "converted")
	if err != nil || len(replayedOutcomes) != 1 || len(replayedOutcomes[0].History) != 2 {
		t.Fatalf("outcome replay: %+v err=%v", replayedOutcomes, err)
	}
	replayedJob, ok, err := population.Read(ctx, replayed, id, jobID)
	if err != nil || !ok || replayedJob.State != population.StateCompleted ||
		replayedJob.Succeeded != 3 {
		t.Fatalf("population replay: ok=%v view=%+v err=%v", ok, replayedJob, err)
	}
}
