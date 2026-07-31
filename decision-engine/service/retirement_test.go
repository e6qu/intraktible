// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// TestModelDependentsBlocksRetirementWhileADeployedFlowReferencesTheModel proves
// the dependent-aware model retirement gate: a flow deployed with a Predict node
// on the model is reported, and an unrelated deployed flow is not.
func TestModelDependentsBlocksRetirementWhileADeployedFlowReferencesTheModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "risk-owner"}

	predictGraph := events.Graph{Nodes: []events.Node{{
		ID:     "predict",
		Type:   events.NodePredict,
		Config: json.RawMessage(`{"model":"default-risk-candidate","output":"risk"}`),
	}}}
	otherGraph := events.Graph{Nodes: []events.Node{{
		ID:     "predict",
		Type:   events.NodePredict,
		Config: json.RawMessage(`{"model":"other-model","output":"risk"}`),
	}}}

	if err := store.PutDoc(
		ctx, st, flows.Collection,
		store.Key(id.Org, id.Workspace, "flow-using-risk"),
		flows.FlowView{
			Org: id.Org, Workspace: id.Workspace, FlowID: "flow-using-risk",
			Latest:      1,
			Versions:    []flows.VersionView{{Version: 1, Graph: predictGraph}},
			Deployments: map[string]flows.DeploymentView{"production": {Version: 1}},
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.PutDoc(
		ctx, st, flows.Collection,
		store.Key(id.Org, id.Workspace, "flow-using-other"),
		flows.FlowView{
			Org: id.Org, Workspace: id.Workspace, FlowID: "flow-using-other",
			Latest:      1,
			Versions:    []flows.VersionView{{Version: 1, Graph: otherGraph}},
			Deployments: map[string]flows.DeploymentView{"sandbox": {Version: 1}},
		},
	); err != nil {
		t.Fatal(err)
	}

	svc := &Service{store: st}
	dependants, err := svc.modelDependents(ctx, id, "default-risk-candidate")
	if err != nil {
		t.Fatal(err)
	}
	if len(dependants) != 1 || dependants[0].FlowID != "flow-using-risk" ||
		dependants[0].Env != "production" {
		t.Fatalf("dependants = %+v, want flow-using-risk/production", dependants)
	}

	none, err := svc.modelDependents(ctx, id, "unused-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("unused model dependants = %+v, want none", none)
	}
}
