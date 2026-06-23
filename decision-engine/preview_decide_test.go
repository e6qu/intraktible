// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// TestPreviewReturnsResultRecordsNothing proves the builder's "Test decision":
// Preview runs the flow and returns the full DecideResult (status, output,
// disposition), but appends no decision events — so nothing lands in history,
// metrics, or the audit log, and no case is opened.
func TestPreviewReturnsResultRecordsNothing(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}

	st := store.NewMemory()
	// A manual_review flow: a real decide here would record a decision AND escalate a
	// case, so a record-free preview must produce neither.
	publishFlow(t, ctx, log, st, id, "escalate", "Escalate", flowtest.ManualReviewGraph())

	// Count the events the publish wrote so the assertion below is "nothing more".
	before, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}

	dh := command.NewDecideHandler(log, st)
	res, err := dh.Preview(ctx, id, "escalate", "production", map[string]any{}, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}

	// The preview returns a full result: completed status and the flow's reason code,
	// but no recorded decision id (nothing was recorded to assign one).
	if res.Status != domain.StatusCompleted {
		t.Fatalf("preview status=%s err=%s", res.Status, res.Error)
	}
	if res.DecisionID != "" {
		t.Fatalf("preview must not record a decision id, got %q", res.DecisionID)
	}
	if rc, ok := res.Output["reason_codes"].([]any); !ok || len(rc) == 0 {
		t.Fatalf("preview output should still carry the reason code trace: %v", res.Output)
	}

	// Nothing was appended to the log by the preview.
	after, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("preview appended %d event(s); it must record nothing", len(after)-len(before))
	}

	// And no decision is in history.
	hist := store.NewMemory()
	if err := projection.New(log, hist, history.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := history.ListPage(ctx, hist, id, history.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("preview recorded %d decision(s) in history; want 0", page.Total)
	}
}

// TestPreviewAndDecideAgree proves Preview runs the same flow logic as Decide:
// the same input yields the same status and output, so the builder's test mirrors
// production behavior.
func TestPreviewAndDecideAgree(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishDecisionFlow(t, ctx, log, st, id)

	dh := command.NewDecideHandler(log, st)
	input := map[string]any{"fico": 680, "bonus": 40}

	prev, err := dh.Preview(ctx, id, "scoring", "production", cloneMap(input), command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	live, err := dh.Decide(ctx, id, "scoring", "production", cloneMap(input), command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if prev.Status != live.Status || prev.Output["decision"] != live.Output["decision"] {
		t.Fatalf("preview (%s/%v) and decide (%s/%v) disagree", prev.Status, prev.Output["decision"], live.Status, live.Output["decision"])
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
