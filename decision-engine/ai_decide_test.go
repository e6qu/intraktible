// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

// stubAgent is a fixed agent source returning canned JSON, proving the decide
// shell resolves a yielded AI effect without depending on the Agent Manager.
type stubAgent string

func (s stubAgent) RunAgent(_ context.Context, _ identity.Identity, _, _ string, _ int) (json.RawMessage, error) {
	return json.RawMessage(s), nil
}

type recordingAgent struct {
	versions []int
}

func (s *recordingAgent) RunAgent(_ context.Context, _ identity.Identity, _, _ string, version int) (json.RawMessage, error) {
	s.versions = append(s.versions, version)
	return json.RawMessage(`{"score":80}`), nil
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

func TestGovernedDecideRequiresAndPassesPinnedAgentVersion(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "caller"}
	st := store.NewMemory()
	h := command.NewHandler(log)
	flowID, _, err := h.CreateFlow(ctx, id, domain.CreateFlow{Slug: "assess", Name: "Assess"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{
		FlowID: flowID, Graph: flowtest.AIGraph(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{
		FlowID: flowID, Environment: "staging", Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, flows.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	agent := &recordingAgent{}
	dh := command.NewDecideHandler(log, st, command.WithAgents(agent))
	if _, err := dh.Decide(ctx, id, "assess", "staging", nil, command.EntityRef{}); !errors.Is(err, command.ErrBadRequest) {
		t.Fatalf("unversioned governed AI node error = %v, want bad request", err)
	}
	if len(agent.versions) != 0 {
		t.Fatalf("provider was called before version governance passed: %v", agent.versions)
	}
	if _, err := dh.Preview(ctx, id, "assess", "staging", nil, command.EntityRef{}); !errors.Is(err, command.ErrBadRequest) {
		t.Fatalf("unversioned governed preview error = %v, want bad request", err)
	}
	if len(agent.versions) != 0 {
		t.Fatalf("preview called provider before version governance passed: %v", agent.versions)
	}

	versioned := flowtest.AIGraph()
	for i := range versioned.Nodes {
		if versioned.Nodes[i].Type == events.NodeAI {
			versioned.Nodes[i].Config = json.RawMessage(`{"agent":"assess","version":1,"output":"assess"}`)
		}
	}
	if _, _, _, err := h.PublishVersion(ctx, id, domain.PublishVersion{
		FlowID: flowID, Graph: versioned,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Deploy(ctx, id, domain.DeployVersion{
		FlowID: flowID, Environment: "staging", Version: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := projection.New(log, st, flows.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	res, err := dh.Decide(ctx, id, "assess", "staging", nil, command.EntityRef{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StatusCompleted || len(agent.versions) != 1 || agent.versions[0] != 1 {
		t.Fatalf("governed result=%+v agent versions=%v, want completed with pinned v1", res, agent.versions)
	}
}
