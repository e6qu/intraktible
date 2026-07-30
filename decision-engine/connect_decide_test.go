// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// stubConnector is a fixed connector source keyed by connector name, proving the
// decide path resolves a reached Connect node without depending on the Context Layer.
type stubConnector map[string]string

func (s stubConnector) Fetch(_ context.Context, _ identity.Identity, connector string, _ json.RawMessage) (json.RawMessage, error) {
	r, ok := s[connector]
	if !ok {
		return nil, fmt.Errorf("no stub for connector %q", connector)
	}
	return json.RawMessage(r), nil
}

func TestDecideResolvesAndRecordsReachedConnectNode(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "screen", "Screen", flowtest.ConnectGraph())

	// score 80 >= 50 -> high.
	dh := command.NewDecideHandler(log, st, command.WithConnectors(stubConnector{"bureau": `{"score":80}`}))
	res, err := dh.Decide(ctx, id, "screen", "sandbox", nil, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusCompleted || res.Output["tier"] != "high" {
		t.Fatalf("want high, got %+v (%s)", res.Output, res.Error)
	}

	// The base DecisionStarted input does not pretend the connector ran before the
	// graph. Instead, the reached effect carries independent requested/succeeded
	// evidence and the node trace records the same response.
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var requested, succeeded bool
	for _, e := range evs {
		switch e.Type {
		case events.TypeEffectRequested:
			var p events.DecisionEffectRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.NodeID == "c" && p.Kind == "connect" && p.Reference == "bureau" && p.InputHash != "" {
				requested = true
			}
		case events.TypeEffectSucceeded:
			var p events.DecisionEffectSucceeded
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			var output map[string]any
			if err := json.Unmarshal(p.Output, &output); err != nil {
				t.Fatal(err)
			}
			if p.NodeID == "c" && output["score"] == float64(80) {
				succeeded = true
			}
		}
	}
	if !requested || !succeeded {
		t.Fatalf("effect evidence requested=%v succeeded=%v", requested, succeeded)
	}
}

type recordingConnector struct {
	calls  int
	params map[string]any
}

func (r *recordingConnector) Fetch(
	_ context.Context,
	_ identity.Identity,
	_ string,
	params json.RawMessage,
) (json.RawMessage, error) {
	r.calls++
	if err := json.Unmarshal(params, &r.params); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"score":80}`), nil
}

func TestDecideRequestsOnlyReachedEffectWithCurrentRecord(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	graph := events.Graph{
		Nodes: []events.Node{
			{ID: "in", Type: events.NodeInput},
			{ID: "assign", Type: events.NodeAssignment, Config: json.RawMessage(`{"assignments":[{"target":"amount","expr":"requested * 2"}]}`)},
			{ID: "split", Type: events.NodeSplit, Config: json.RawMessage(`{"condition":"use_bureau"}`)},
			{ID: "connect", Type: events.NodeConnect, Config: json.RawMessage(`{"connector":"bureau","output":"report"}`)},
			{ID: "out", Type: events.NodeOutput},
		},
		Edges: []events.Edge{
			{From: "in", To: "assign"},
			{From: "assign", To: "split"},
			{From: "split", To: "connect", Branch: "yes"},
			{From: "split", To: "out", Branch: "no"},
			{From: "connect", To: "out"},
		},
	}
	publishFlow(t, ctx, log, st, id, "lazy-effects", "Lazy effects", graph)
	connector := &recordingConnector{}
	handler := command.NewDecideHandler(log, st, command.WithConnectors(connector))

	skipped, err := handler.Decide(
		ctx, id, "lazy-effects", "sandbox",
		map[string]any{"requested": 21, "use_bureau": false}, command.EntityRef{},
	)
	if err != nil || skipped.Status != domain.StatusCompleted {
		t.Fatalf("skipped result = %+v, err = %v", skipped, err)
	}
	if connector.calls != 0 {
		t.Fatalf("untaken connector calls = %d, want 0", connector.calls)
	}

	reached, err := handler.Decide(
		ctx, id, "lazy-effects", "sandbox",
		map[string]any{"requested": 21, "use_bureau": true}, command.EntityRef{},
	)
	if err != nil || reached.Status != domain.StatusCompleted {
		t.Fatalf("reached result = %+v, err = %v", reached, err)
	}
	if connector.calls != 1 {
		t.Fatalf("reached connector calls = %d, want 1", connector.calls)
	}
	if connector.params["amount"] != float64(42) {
		t.Fatalf("connector amount = %#v, want 42", connector.params["amount"])
	}
}

// decideFailsWithoutProvider publishes a flow whose reached node needs a
// provider, then decides with NO provider configured and asserts admission
// fails before a DecisionStarted event can make an unexecutable run durable.
// Shared by the Connect, AI, and Predict node tests.
func decideFailsWithoutProvider(t *testing.T, slug string, graph events.Graph) {
	t.Helper()
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, slug, slug, graph)

	_, err = command.NewDecideHandler(log, st).Decide(ctx, id, slug, "sandbox", nil, command.EntityRef{})
	if !errors.Is(err, command.ErrBadRequest) {
		t.Fatalf("error = %v, want ErrBadRequest", err)
	}
	envelopes, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, envelope := range envelopes {
		if envelope.Type == events.TypeDecisionStarted {
			t.Fatalf("unexecutable decision was started: %+v", envelope)
		}
	}
}

func TestDecideConnectNodeWithoutProviderFailsLoudly(t *testing.T) {
	decideFailsWithoutProvider(t, "screen", flowtest.ConnectGraph())
}
