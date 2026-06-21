// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
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
// decide path pre-resolves Connect nodes without depending on the Context Layer.
type stubConnector map[string]string

func (s stubConnector) Fetch(_ context.Context, _ identity.Identity, connector string, _ json.RawMessage) (json.RawMessage, error) {
	r, ok := s[connector]
	if !ok {
		return nil, fmt.Errorf("no stub for connector %q", connector)
	}
	return json.RawMessage(r), nil
}

func TestDecidePreResolvesConnectNode(t *testing.T) {
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
	res, err := dh.Decide(ctx, id, "screen", "production", nil, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(domain.StatusCompleted) || res.Output["tier"] != "high" {
		t.Fatalf("want high, got %+v (%s)", res.Output, res.Error)
	}

	// The connector response is recorded in the decision's input.
	evs, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawConnect bool
	for _, e := range evs {
		if e.Type != events.TypeDecisionStarted {
			continue
		}
		var p events.DecisionStarted
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if err := json.Unmarshal(p.Data, &data); err != nil {
			t.Fatal(err)
		}
		conn, ok := data["connect"].(map[string]any)
		if !ok {
			t.Fatalf("connect data not recorded: %v", data)
		}
		if _, ok := conn["bureau"]; !ok {
			t.Fatalf("bureau output not recorded: %v", conn)
		}
		sawConnect = true
	}
	if !sawConnect {
		t.Fatal("no DecisionStarted recorded")
	}
}

// decideFailsWithoutProvider publishes a flow whose node depends on a pre-resolved
// provider, then decides with NO provider configured and asserts the run fails
// loudly (the pure core cannot perform the I/O itself). Shared by the Connect and
// AI node tests.
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

	res, err := command.NewDecideHandler(log, st).Decide(ctx, id, slug, "production", nil, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != string(domain.StatusFailed) {
		t.Fatalf("expected a failed decision without a provider, got %+v", res)
	}
}

func TestDecideConnectNodeWithoutProviderFailsLoudly(t *testing.T) {
	decideFailsWithoutProvider(t, "screen", flowtest.ConnectGraph())
}
