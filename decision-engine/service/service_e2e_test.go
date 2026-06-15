// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/service"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func startEngine(t *testing.T, opts ...command.DecideOption) *testutil.API {
	t.Helper()
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), command.NewDecideHandler(log, st, opts...), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	return testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, flows.Projector{}, history.Projector{}, analytics.Projector{})
}

// stubFeatures is a fixed feature source for the decide HTTP test.
type stubFeatures map[string]float64

func (s stubFeatures) Features(_ context.Context, _ identity.Identity, _, _ string) (map[string]float64, error) {
	return s, nil
}

func TestDecideValidatesInputSchema(t *testing.T) {
	api := startEngine(t)

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "scored", "name": "Scored"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": flowtest.LinearGraph(),
		"input_schema": map[string]any{
			"type":       "object",
			"required":   []string{"customer"},
			"properties": map[string]any{"customer": map[string]any{"type": "string"}},
		},
	}, http.StatusCreated, nil)

	// Valid input runs once the version is visible.
	if !testutil.Eventually(t, func() bool {
		var res struct {
			Status string `json:"status"`
		}
		api.Request(t, http.MethodPost, "/v1/flows/scored/production/decide",
			map[string]any{"data": map[string]any{"customer": "acme"}}, http.StatusOK, &res)
		return res.Status == "completed"
	}) {
		t.Fatal("valid input never decided")
	}

	// Missing the required field is a bad request (not a recorded decision).
	api.Request(t, http.MethodPost, "/v1/flows/scored/production/decide",
		map[string]any{"data": map[string]any{}}, http.StatusBadRequest, nil)
	// Wrong type is also rejected.
	api.Request(t, http.MethodPost, "/v1/flows/scored/production/decide",
		map[string]any{"data": map[string]any{"customer": 42}}, http.StatusBadRequest, nil)
}

func TestDecideWithEntityFeaturesOverHTTP(t *testing.T) {
	api := startEngine(t, command.WithFeatures(stubFeatures{"txn_count_24h": 5}))

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "risk", "name": "Risk"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.FeatureGraph()}, http.StatusCreated, nil)

	// The flow registry read model is eventually consistent; retry the decide
	// until the published version is visible, then assert the feature drove it.
	if !testutil.Eventually(t, func() bool {
		var res struct {
			Status string         `json:"status"`
			Data   map[string]any `json:"data"`
		}
		api.Request(t, http.MethodPost, "/v1/flows/risk/production/decide",
			map[string]any{"entity_type": "customer", "entity_id": "c1"}, http.StatusOK, &res)
		return res.Status == "completed" && res.Data["tier"] == "high"
	}) {
		t.Fatal("decide never reflected the injected feature (tier=high)")
	}
}

// stubConnector is a fixed connector source for the decide HTTP test.
type stubConnector map[string]string

func (s stubConnector) Fetch(_ context.Context, _ identity.Identity, connector string, _ json.RawMessage) (json.RawMessage, error) {
	r, ok := s[connector]
	if !ok {
		return nil, fmt.Errorf("no stub for connector %q", connector)
	}
	return json.RawMessage(r), nil
}

func TestDecideWithConnectorOverHTTP(t *testing.T) {
	api := startEngine(t, command.WithConnectors(stubConnector{"bureau": `{"score":80}`}))

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "screen", "name": "Screen"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConnectGraph()}, http.StatusCreated, nil)

	if !testutil.Eventually(t, func() bool {
		var res struct {
			Status string         `json:"status"`
			Data   map[string]any `json:"data"`
		}
		api.Request(t, http.MethodPost, "/v1/flows/screen/production/decide", map[string]any{}, http.StatusOK, &res)
		return res.Status == "completed" && res.Data["tier"] == "high"
	}) {
		t.Fatal("decide never reflected the connector response (tier=high)")
	}
}

// stubAgent is a fixed agent source for the decide HTTP test.
type stubAgent string

func (s stubAgent) RunAgent(_ context.Context, _ identity.Identity, _, _ string) (json.RawMessage, error) {
	return json.RawMessage(s), nil
}

func TestDecideWithAINodeOverHTTP(t *testing.T) {
	api := startEngine(t, command.WithAgents(stubAgent(`{"score":80}`)))

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "assess", "name": "Assess"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.AIGraph()}, http.StatusCreated, nil)

	if !testutil.Eventually(t, func() bool {
		var res struct {
			Status string         `json:"status"`
			Data   map[string]any `json:"data"`
		}
		api.Request(t, http.MethodPost, "/v1/flows/assess/production/decide", map[string]any{}, http.StatusOK, &res)
		return res.Status == "completed" && res.Data["tier"] == "high"
	}) {
		t.Fatal("decide never reflected the AI node output (tier=high)")
	}
}

func TestEngineAPIEndToEnd(t *testing.T) {
	api := startEngine(t)

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "onboarding", "name": "Onboarding"}, http.StatusCreated, &created)
	if created.FlowID == "" {
		t.Fatal("create did not return a flow id")
	}

	var published struct {
		Version int    `json:"version"`
		Etag    string `json:"etag"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.LinearGraph()}, http.StatusCreated, &published)
	if published.Version != 1 || published.Etag == "" {
		t.Fatalf("publish returned %+v", published)
	}

	// Read model is async; poll the GET endpoint until the version appears.
	if !testutil.Eventually(t, func() bool {
		var fv flows.FlowView
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID, nil, http.StatusOK, &fv)
		return fv.Latest == 1 && len(fv.Versions) == 1 && fv.Slug == "onboarding"
	}) {
		t.Fatal("GET flow never reflected the published version")
	}

	var list struct {
		Flows []flows.FlowView `json:"flows"`
	}
	api.Request(t, http.MethodGet, "/v1/flows", nil, http.StatusOK, &list)
	if len(list.Flows) != 1 {
		t.Fatalf("list returned %d flows, want 1", len(list.Flows))
	}
}

func TestEngineAPIValidationErrors(t *testing.T) {
	api := startEngine(t)

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "dup", "name": "A"}, http.StatusCreated, &created)

	// Duplicate slug -> 400.
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "dup", "name": "B"}, http.StatusBadRequest, nil)

	// Invalid (cyclic) graph -> 400.
	cyclic := flowtest.LinearGraph()
	cyclic.Edges = append(cyclic.Edges, events.Edge{From: "out", To: "r"})
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": cyclic}, http.StatusBadRequest, nil)

	// Publishing to an unknown flow -> 400.
	api.Request(t, http.MethodPost, "/v1/flows/ghost/versions",
		map[string]any{"graph": flowtest.LinearGraph()}, http.StatusBadRequest, nil)

	// Unknown flow GET -> 404.
	api.Request(t, http.MethodGet, "/v1/flows/ghost", nil, http.StatusNotFound, nil)
}

func TestDecideAPIEndToEnd(t *testing.T) {
	api := startEngine(t)

	// Publish an executable flow over HTTP.
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "scoring", "name": "Scoring"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.DecisionGraph()}, http.StatusCreated, nil)

	// decide reads the (async) flow registry projection; wait for it to catch up
	// via the GET endpoint (which is always 200) before deciding.
	if !testutil.Eventually(t, func() bool {
		var fv flows.FlowView
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID, nil, http.StatusOK, &fv)
		return fv.Latest == 1
	}) {
		t.Fatal("flow registry projection never caught up")
	}

	var decision struct {
		DecisionID string         `json:"decision_id"`
		Status     string         `json:"status"`
		Data       map[string]any `json:"data"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/scoring/production/decide",
		map[string]any{"data": map[string]any{"fico": 680, "bonus": 40}}, http.StatusOK, &decision)
	if decision.Status != "completed" || decision.Data["decision"] != "APPROVE" {
		t.Fatalf("decide result: %+v", decision)
	}

	// The decision shows up in history with its full node trace.
	if !testutil.Eventually(t, func() bool {
		var rec history.Record
		api.Request(t, http.MethodGet, "/v1/decisions/"+decision.DecisionID, nil, http.StatusOK, &rec)
		return rec.Status == "completed" && len(rec.TimeOrdered) == 5
	}) {
		t.Fatal("decision history never reflected the run")
	}
	var list struct {
		Decisions []history.Record `json:"decisions"`
	}
	api.Request(t, http.MethodGet, "/v1/decisions", nil, http.StatusOK, &list)
	if len(list.Decisions) == 0 {
		t.Fatal("decision list is empty")
	}

	// The decision is counted in the analytics metrics (async projection).
	if !testutil.Eventually(t, func() bool {
		var m analytics.FlowMetrics
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID+"/metrics", nil, http.StatusOK, &m)
		return m.Total >= 1 && m.Completed >= 1 && m.ByVariant["champion"].Completed >= 1
	}) {
		t.Fatal("metrics never reflected the decision")
	}

	// Invalid environment -> 400.
	api.Request(t, http.MethodPost, "/v1/flows/scoring/staging/decide",
		map[string]any{"data": map[string]any{}}, http.StatusBadRequest, nil)
}

func TestDeployAPIPinsVersion(t *testing.T) {
	api := startEngine(t)

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "router", "name": "Router"}, http.StatusCreated, &created)
	// Publish v1 and v2 (each outputs a marker).
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("v1")}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("v2")}, http.StatusCreated, nil)

	// Pin production to v1; wait for the registry projection to show it.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "production", "version": 1}, http.StatusCreated, nil)
	if !testutil.Eventually(t, func() bool {
		var fv flows.FlowView
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID, nil, http.StatusOK, &fv)
		dep, ok := fv.Deployments["production"]
		return ok && dep.Version == 1
	}) {
		t.Fatal("deployment never reached the registry projection")
	}

	// Decide production -> the pinned v1, not the latest v2.
	var decision struct {
		Status string         `json:"status"`
		Data   map[string]any `json:"data"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/router/production/decide",
		map[string]any{"data": map[string]any{}}, http.StatusOK, &decision)
	if decision.Status != "completed" || decision.Data["decision"] != "v1" {
		t.Fatalf("pinned decide: %+v", decision)
	}

	// Deploying an unpublished version is rejected.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "production", "version": 9}, http.StatusBadRequest, nil)
}

func TestEngineAPIRequiresAuth(t *testing.T) {
	api := startEngine(t)
	resp, err := http.Get(api.Server.URL + "/v1/flows")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request -> %d, want 401", resp.StatusCode)
	}
}
