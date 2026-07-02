// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// stubAgent is a fixed agent source returning canned JSON, proving the decide path
// pre-resolves AI nodes without depending on the Agent Manager.
type stubAgent string

func (s stubAgent) RunAgent(_ context.Context, _ identity.Identity, _, _ string) (json.RawMessage, error) {
	return json.RawMessage(s), nil
}

func TestDecidePreResolvesAINode(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	publishFlow(t, ctx, log, st, id, "assess", "Assess", flowtest.AIGraph())

	// score 80 >= 50 -> high.
	dh := command.NewDecideHandler(log, st, command.WithAgents(stubAgent(`{"score":80}`)))
	res, err := dh.Decide(ctx, id, "assess", "sandbox", nil, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusCompleted || res.Output["tier"] != "high" {
		t.Fatalf("want high, got %+v (%s)", res.Output, res.Error)
	}
}

func TestDecideAINodeWithoutProviderFailsLoudly(t *testing.T) {
	decideFailsWithoutProvider(t, "assess", flowtest.AIGraph())
}
