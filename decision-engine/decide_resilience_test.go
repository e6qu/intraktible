// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// resilienceSetup publishes a flow and returns a handler configured with opts (used to
// inject a failing dependency or a tight evaluation budget). It exercises the real
// durable WAL, so the fail-loud behavior is measured on the production path.
func resilienceSetup(t *testing.T, graph events.Graph, opts ...command.DecideOption) (context.Context, *command.DecideHandler, identity.Identity) {
	t.Helper()
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "risk", "Risk", graph)
	return ctx, command.NewDecideHandler(log, st, opts...), id
}

// failedLoud reports whether a decide outcome is a loud failure: an error, or a decision
// that did not complete. The one thing forbidden is a silent "completed" that dropped or
// skipped the failed step.
func failedLoud(res command.DecideResult, err error) bool {
	return err != nil || res.Status != "completed"
}

func riskInput() map[string]any { return map[string]any{"fico": 700, "bonus": 20} }

// TestDecideEvalBudgetFailsLoud verifies the per-decide evaluation budget is enforced end
// to end: with a one-nanosecond budget the deadline fires on the first node, so the
// decision fails loud (never a silent "completed") and returns quickly — a CPU-heavy
// expression a flow author ships cannot hang the synchronous decide.
func TestDecideEvalBudgetFailsLoud(t *testing.T) {
	ctx, dh, id := resilienceSetup(t, flowtest.DecisionGraph(), command.WithEvalTimeout(time.Nanosecond))

	start := time.Now()
	res, err := dh.Decide(ctx, id, "risk", "sandbox", riskInput(), command.EntityRef{})
	elapsed := time.Since(start)

	if !failedLoud(res, err) {
		t.Fatal("a 1ns evaluation budget must not produce a completed decision")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("decide took %s under a 1ns budget — the deadline is not enforced (it hung)", elapsed)
	}
}

// TestDecideGenerousBudgetCompletes is the positive control: the same flow with a normal
// budget completes, proving the budget test above fails specifically because of the tight
// deadline and not because the flow is broken.
func TestDecideGenerousBudgetCompletes(t *testing.T) {
	ctx, dh, id := resilienceSetup(t, flowtest.DecisionGraph(), command.WithEvalTimeout(5*time.Second))

	res, err := dh.Decide(ctx, id, "risk", "sandbox", riskInput(), command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "completed" {
		t.Fatalf("a healthy flow with a generous budget should complete, got %s", res.Status)
	}
}

// Each external-dependency port has a fake that always fails, recording that it was
// reached so a test can prove the dependency was attempted (not silently skipped).
type erroringConnector struct{ called bool }

func (c *erroringConnector) Fetch(context.Context, identity.Identity, string, json.RawMessage) (json.RawMessage, error) {
	c.called = true
	return nil, errors.New("bureau: upstream unavailable")
}

type erroringAgent struct{ called bool }

func (a *erroringAgent) RunAgent(context.Context, identity.Identity, string, string) (json.RawMessage, error) {
	a.called = true
	return nil, errors.New("assess: provider error")
}

type erroringModel struct{ called bool }

func (m *erroringModel) Predict(context.Context, identity.Identity, string, map[string]any) (json.RawMessage, error) {
	m.called = true
	return nil, errors.New("risk: model unavailable")
}

func (m *erroringModel) ApprovedForServing(context.Context, identity.Identity, string) (bool, error) {
	return true, nil
}

// TestDecideFailsLoudOnDependencyError checks that a failing Connect, AI, or Predict node
// makes the decision fail loud — the dependency is attempted and its failure surfaces,
// rather than the decision completing with that step's data silently missing.
func TestDecideFailsLoudOnDependencyError(t *testing.T) {
	input := map[string]any{"amount": 100}

	t.Run("connector", func(t *testing.T) {
		conn := &erroringConnector{}
		ctx, dh, id := resilienceSetup(t, flowtest.ConnectGraph(), command.WithConnectors(conn))
		res, err := dh.Decide(ctx, id, "risk", "sandbox", input, command.EntityRef{})
		if !conn.called {
			t.Fatal("the connector was never attempted")
		}
		if !failedLoud(res, err) {
			t.Fatal("a failing connector must not yield a completed decision")
		}
		if err != nil && !strings.Contains(err.Error(), "bureau") {
			t.Fatalf("the connector failure should surface, got %v", err)
		}
	})

	t.Run("agent", func(t *testing.T) {
		agent := &erroringAgent{}
		ctx, dh, id := resilienceSetup(t, flowtest.AIGraph(), command.WithAgents(agent))
		res, err := dh.Decide(ctx, id, "risk", "sandbox", input, command.EntityRef{})
		if !agent.called {
			t.Fatal("the agent was never attempted")
		}
		if !failedLoud(res, err) {
			t.Fatal("a failing agent must not yield a completed decision")
		}
	})

	t.Run("model", func(t *testing.T) {
		model := &erroringModel{}
		ctx, dh, id := resilienceSetup(t, flowtest.PredictGraph(), command.WithModels(model))
		res, err := dh.Decide(ctx, id, "risk", "sandbox", input, command.EntityRef{})
		if !model.called {
			t.Fatal("the model was never attempted")
		}
		if !failedLoud(res, err) {
			t.Fatal("a failing model must not yield a completed decision")
		}
	})
}
