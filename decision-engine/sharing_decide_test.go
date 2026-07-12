// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// fakeSharing is an in-memory SharingGate (GLBA opt-out).
type fakeSharing struct{ optedOut map[string]bool }

func (f *fakeSharing) HasOptedOut(_ context.Context, _ identity.Identity, subject string) (bool, error) {
	return f.optedOut[subject], nil
}

// sharesNPIGraph: input -> connect(vendor, shares_npi) -> output.
func sharesNPIGraph() events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "share", Type: events.NodeConnect, Config: json.RawMessage(
				`{"connector":"vendor","output":"v","shares_npi":true}`)},
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{{From: "in", To: "share"}, {From: "share", To: "out"}},
	}
}

type sharingEnv struct {
	ctx  context.Context
	dh   *command.DecideHandler
	id   identity.Identity
	conn *fakeConnector
	gate *fakeSharing
}

func sharingTestSetup(t *testing.T) sharingEnv {
	t.Helper()
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "bank"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "share", "Share", sharesNPIGraph())
	conn, gate := &fakeConnector{}, &fakeSharing{optedOut: map[string]bool{}}
	dh := command.NewDecideHandler(log, st, command.WithConnectors(conn), command.WithSharing(gate))
	return sharingEnv{ctx: ctx, dh: dh, id: id, conn: conn, gate: gate}
}

func TestSharingBlockedWhenOptedOut(t *testing.T) {
	e := sharingTestSetup(t)
	e.gate.optedOut["applicant/a1"] = true

	_, err := e.dh.Decide(e.ctx, e.id, "share", "sandbox", map[string]any{"amount": 1000}, applicant())
	if err == nil || !strings.Contains(err.Error(), "opted out") {
		t.Fatalf("expected an opt-out error, got %v", err)
	}
	if e.conn.fetched != 0 {
		t.Fatalf("the NPI-sharing connector was fetched %d times despite the opt-out", e.conn.fetched)
	}
}

func TestSharingPermittedWhenNotOptedOut(t *testing.T) {
	e := sharingTestSetup(t)

	res, err := e.dh.Decide(e.ctx, e.id, "share", "sandbox", map[string]any{"amount": 1000}, applicant())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" || e.conn.fetched != 1 {
		t.Fatalf("status=%s fetched=%d", res.Status, e.conn.fetched)
	}
}

func TestSharingNoSubjectFails(t *testing.T) {
	e := sharingTestSetup(t)

	// No entity ref → no subject whose opt-out can be checked → the share is refused.
	_, err := e.dh.Decide(e.ctx, e.id, "share", "sandbox", map[string]any{"amount": 1000}, command.EntityRef{})
	if err == nil || !strings.Contains(err.Error(), "no subject") {
		t.Fatalf("expected a no-subject error, got %v", err)
	}
	if e.conn.fetched != 0 {
		t.Fatal("the connector must not be fetched without a subject")
	}
}
