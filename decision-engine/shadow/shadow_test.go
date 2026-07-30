// SPDX-License-Identifier: AGPL-3.0-or-later

package shadow_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/shadow"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func appendEvaluation(
	t *testing.T,
	ctx context.Context,
	log eventlog.Log,
	id identity.Identity,
	at time.Time,
	evaluated events.ShadowEvaluated,
) {
	t.Helper()
	if _, err := eventlog.AppendJSON(
		ctx, log, id.Org, id.Workspace, id.Actor,
		events.StreamDecisions, events.TypeShadowEvaluated, at, evaluated,
	); err != nil {
		t.Fatal(err)
	}
}

func TestProjectorKeepsHomogeneousCohortsAndReplaysSamples(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "runner"}
	at := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)

	base := events.ShadowEvaluated{
		FlowID: "flow-1", Environment: "production",
		ExperimentID: "experiment-1", ExperimentCohort: 7, ExperimentArm: "champion",
		LiveVersion: 3, ShadowVersion: 4,
		MatchBasis: events.ShadowMatchPolicy,
		PolicyID:   "policy-1", PolicyVersion: 2,
		LiveStatus: "completed", ShadowStatus: "completed",
		LiveDisposition: "approve", ShadowDisposition: "approve",
		LiveCode: "AUTO", ShadowCode: "AUTO",
		LiveReason: "approved band", ShadowReason: "approved band",
	}
	matched := base
	matched.DecisionID, matched.Matched = "decision-match", true
	appendEvaluation(t, ctx, log, id, at, matched)

	diverged := base
	diverged.DecisionID = "decision-diverged"
	diverged.ShadowDisposition, diverged.ShadowCode = "refer", ""
	diverged.ChangedFields = []string{"score"}
	appendEvaluation(t, ctx, log, id, at.Add(time.Second), diverged)

	projected := store.NewMemory()
	if _, err := projection.New(log, projected, shadow.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	report, ok, err := shadow.Read(ctx, projected, id, "flow-1")
	if err != nil || !ok {
		t.Fatalf("read report: ok=%v err=%v", ok, err)
	}
	cohort := report.ByEnv["production"]
	if cohort.Total != 2 || cohort.Matched != 1 || cohort.Diverged != 1 ||
		cohort.LiveVersion != 3 || cohort.ShadowVersion != 4 ||
		cohort.MatchBasis != events.ShadowMatchPolicy ||
		len(cohort.Samples) != 1 ||
		cohort.Samples[0].DecisionID != "decision-diverged" ||
		!reflect.DeepEqual(cohort.Samples[0].ChangedFields, []string{"score"}) {
		t.Fatalf("first cohort = %+v", cohort)
	}

	// A champion change starts fresh even though the candidate did not change.
	nextChampion := base
	nextChampion.DecisionID, nextChampion.LiveVersion, nextChampion.Matched = "decision-next-live", 5, true
	appendEvaluation(t, ctx, log, id, at.Add(2*time.Second), nextChampion)
	if _, err := projection.New(log, projected, shadow.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	report, _, err = shadow.Read(ctx, projected, id, "flow-1")
	if err != nil {
		t.Fatal(err)
	}
	cohort = report.ByEnv["production"]
	if cohort.Total != 1 || cohort.Matched != 1 || cohort.LiveVersion != 5 ||
		len(cohort.Samples) != 0 {
		t.Fatalf("champion-change cohort = %+v", cohort)
	}
	if len(report.Cohorts) != 2 {
		t.Fatalf("exact cohorts = %d, want original and new champion", len(report.Cohorts))
	}

	// An experiment configuration change retains both exact cohorts; it never
	// blends or discards the prior comparison evidence.
	nextExperimentCohort := base
	nextExperimentCohort.DecisionID = "decision-next-experiment"
	nextExperimentCohort.ExperimentCohort = 8
	nextExperimentCohort.Matched = true
	appendEvaluation(t, ctx, log, id, at.Add(3*time.Second), nextExperimentCohort)
	if _, err := projection.New(log, projected, shadow.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	report, _, err = shadow.Read(ctx, projected, id, "flow-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cohorts) != 3 || report.ByEnv["production"].ExperimentCohort != 8 {
		t.Fatalf("experiment shadow cohorts = %+v", report)
	}

	// Rebuilding into a fresh store yields every exact cohort identically.
	fresh := store.NewMemory()
	if _, err := projection.New(log, fresh, shadow.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	replayed, _, err := shadow.Read(ctx, fresh, id, "flow-1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, replayed) {
		t.Fatalf("replayed report differs:\ncurrent=%+v\nreplayed=%+v", report, replayed)
	}
}
