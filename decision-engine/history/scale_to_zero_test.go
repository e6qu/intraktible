// SPDX-License-Identifier: AGPL-3.0-or-later

package history_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// TestSuspendedDecisionSurvivesColdRebuild proves the scale-to-zero property of durable
// human-task suspension: a paused decision holds NO resident process or in-memory state
// — it is just a DecisionSuspended event in the durable log. So the whole read model can
// be discarded and rebuilt from scratch (a process restart / scale-from-zero) and the
// suspended decision still resumes deterministically from the rehydrated state.
func TestSuspendedDecisionSurvivesColdRebuild(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	now := time.Now()

	state := domain.SuspendState{
		NodeID: "review", Resume: "out", OutputKey: "review",
		Record: map[string]any{"applicant": "a1"},
		Case:   domain.ManualReviewCase{CompanyName: "Acme", CaseType: "underwriting", SLADays: 3},
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.AppendJSON(ctx, log, id.Org, id.Workspace, id.Actor,
		events.StreamDecisions, events.TypeDecisionStarted, now, events.DecisionStarted{
			DecisionID: "dec_1", FlowID: "f1", Slug: "s", Version: 1, Environment: "production",
			Data: json.RawMessage(`{"applicant":"a1"}`),
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.AppendJSON(ctx, log, id.Org, id.Workspace, id.Actor,
		events.StreamDecisions, events.TypeDecisionSuspended, now, events.DecisionSuspended{
			DecisionID: "dec_1", FlowID: "f1", Version: 1, NodeID: "review", ResumeNode: "out",
			State: stateJSON,
		}); err != nil {
		t.Fatal(err)
	}

	// COLD REBUILD: a brand-new, empty store rebuilt purely from the durable log — no
	// resident process carried the suspended instance across.
	st := store.NewMemory()
	if err := projection.New(log, st, history.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	rec, ok, err := history.Read(ctx, st, id, "dec_1")
	if err != nil || !ok {
		t.Fatalf("read after rebuild: ok=%v err=%v", ok, err)
	}
	if rec.Status != "suspended" || len(rec.SuspendState) == 0 {
		t.Fatalf("suspended state did not survive the cold rebuild: %+v", rec)
	}

	// The rehydrated state is sufficient to resume to completion — the decision was fully
	// reconstructable from durable storage alone.
	var got domain.SuspendState
	if err := json.Unmarshal(rec.SuspendState, &got); err != nil {
		t.Fatal(err)
	}
	graph := events.Graph{
		Nodes: []events.Node{{ID: "out", Type: events.NodeOutput, Config: json.RawMessage(`{"fields":["decision"]}`)}},
	}
	run := domain.Resume(graph, got, map[string]any{"decision": "approve"})
	if run.Status != domain.StatusCompleted || run.Output["decision"] != "approve" {
		t.Fatalf("resume from rebuilt state failed: %+v", run)
	}
}
