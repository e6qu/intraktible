// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/export"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/decision-engine/service"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

// rawGet does an authed GET and returns the body (export endpoints return
// text/xml, not JSON, so api.Request's JSON decode does not fit).
func rawGet(t *testing.T, api *testutil.API, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, api.Server.URL+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", api.Key)
	resp, err := api.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestExportFlowOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "exp", "name": "Exp Flow"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "s", "type": "split", "name": "score"},
				{"id": "out", "type": "output"},
			},
			"edges": []map[string]any{
				{"from": "in", "to": "s"},
				{"from": "s", "to": "out", "branch": "yes"},
			},
		},
	}, http.StatusCreated, nil)

	// Mermaid flowchart (waits for the async flow projection to catch up).
	var mermaid string
	if !testutil.Eventually(t, func() bool {
		code, body := rawGet(t, api, "/v1/flows/"+created.FlowID+"/export?format=mermaid")
		mermaid = body
		return code == http.StatusOK && strings.Contains(body, "s{") // the split node rendered
	}) {
		t.Fatalf("mermaid export never reflected the published graph:\n%s", mermaid)
	}
	if !strings.Contains(mermaid, "flowchart TD") || !strings.Contains(mermaid, "s -->|yes| out") {
		t.Fatalf("mermaid flowchart incomplete:\n%s", mermaid)
	}

	// BPMN with diagram interchange.
	code, bpmn := rawGet(t, api, "/v1/flows/"+created.FlowID+"/export?format=bpmn")
	if code != http.StatusOK || !strings.Contains(bpmn, "<bpmn:definitions") || !strings.Contains(bpmn, "<bpmndi:BPMNDiagram") {
		t.Fatalf("bpmn export incomplete (status %d):\n%s", code, bpmn)
	}

	// Graphviz DOT.
	code, dot := rawGet(t, api, "/v1/flows/"+created.FlowID+"/export?format=dot")
	if code != http.StatusOK || !strings.Contains(dot, "digraph flow {") || !strings.Contains(dot, `"s" -> "out" [label="yes"];`) {
		t.Fatalf("dot export incomplete (status %d):\n%s", code, dot)
	}

	// Round-trippable JSON (the graph re-imports into POST .../versions).
	code, raw := rawGet(t, api, "/v1/flows/"+created.FlowID+"/export?format=json")
	if code != http.StatusOK {
		t.Fatalf("json export status %d:\n%s", code, raw)
	}
	var fx export.FlowExport
	if err := json.Unmarshal([]byte(raw), &fx); err != nil {
		t.Fatalf("json export not valid: %v\n%s", err, raw)
	}
	if fx.Slug == "" || len(fx.Graph.Nodes) == 0 {
		t.Fatalf("json export missing slug/graph:\n%s", raw)
	}

	// Unknown format → 400.
	if code, _ := rawGet(t, api, "/v1/flows/"+created.FlowID+"/export?format=svg"); code != http.StatusBadRequest {
		t.Fatalf("unknown format → %d, want 400", code)
	}
}

func TestExportDecisionTraceOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "trace", "name": "Trace"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "a", "type": "assignment", "config": map[string]any{"assignments": []map[string]any{{"target": "d", "expr": "'OK'"}}}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"d"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
		},
	}, http.StatusCreated, nil)

	// Decide (retries until the flow projection is live), capturing the decision id.
	var dec struct {
		DecisionID string `json:"decision_id"`
		Status     string `json:"status"`
	}
	if !testutil.Eventually(t, func() bool {
		dec = struct {
			DecisionID string `json:"decision_id"`
			Status     string `json:"status"`
		}{}
		api.Request(t, http.MethodPost, "/v1/flows/trace/production/decide", map[string]any{"data": map[string]any{}}, http.StatusOK, &dec)
		return dec.Status == "completed" && dec.DecisionID != ""
	}) {
		t.Fatal("decide never completed")
	}

	// The decision run exports as a Mermaid sequence diagram with its node trace.
	var trace string
	if !testutil.Eventually(t, func() bool {
		code, body := rawGet(t, api, "/v1/decisions/"+dec.DecisionID+"/export")
		trace = body
		return code == http.StatusOK && strings.Contains(body, "Note over E: a (assignment)")
	}) {
		t.Fatalf("decision trace export incomplete:\n%s", trace)
	}
	if !strings.Contains(trace, "sequenceDiagram") || !strings.Contains(trace, "E-->>C: completed") {
		t.Fatalf("sequence diagram incomplete:\n%s", trace)
	}

	// The run also exports as a Graphviz DOT path...
	code, dot := rawGet(t, api, "/v1/decisions/"+dec.DecisionID+"/export?format=dot")
	if code != http.StatusOK || !strings.Contains(dot, "digraph run {") || !strings.Contains(dot, `"a" [label="a (assignment)"`) {
		t.Fatalf("run DOT export incomplete (status %d):\n%s", code, dot)
	}
	// ...and as the full decision record JSON.
	code, raw := rawGet(t, api, "/v1/decisions/"+dec.DecisionID+"/export?format=json")
	if code != http.StatusOK {
		t.Fatalf("run JSON export status %d:\n%s", code, raw)
	}
	var rec history.Record
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatalf("run JSON not valid: %v\n%s", err, raw)
	}
	if rec.DecisionID != dec.DecisionID || rec.Status != "completed" {
		t.Fatalf("run JSON mismatch: %+v", rec)
	}
	// Unknown format → 400.
	if code, _ := rawGet(t, api, "/v1/decisions/"+dec.DecisionID+"/export?format=svg"); code != http.StatusBadRequest {
		t.Fatalf("unknown run format → %d, want 400", code)
	}
}

func TestDecideBatchOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "batch", "name": "Batch"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "a", "type": "assignment", "config": map[string]any{"assignments": []map[string]any{{"target": "d", "expr": "'OK'"}}}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"d"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
		},
		"input_schema": map[string]any{"type": "object", "required": []string{"x"}},
	}, http.StatusCreated, nil)

	// Batch over three rows: two valid, one missing the required field → rejected.
	type result struct {
		DecisionID string `json:"decision_id"`
		Status     string `json:"status"`
	}
	var resp struct {
		Total, Completed, Failed, Rejected int
		Results                            []result `json:"results"`
	}
	if !testutil.Eventually(t, func() bool {
		resp = struct {
			Total, Completed, Failed, Rejected int
			Results                            []result `json:"results"`
		}{}
		api.Request(t, http.MethodPost, "/v1/flows/batch/production/decide/batch", map[string]any{
			"dataset": []map[string]any{{"x": 1}, {"x": 2}, {}},
		}, http.StatusOK, &resp)
		return resp.Total == 3 && resp.Completed == 2
	}) {
		t.Fatalf("batch never completed two rows: %+v", resp)
	}
	if resp.Rejected != 1 {
		t.Fatalf("expected 1 rejected (missing required field), got %+v", resp)
	}
	for _, r := range resp.Results[:2] {
		if r.Status != "completed" || r.DecisionID == "" {
			t.Fatalf("completed rows must carry a decision id: %+v", r)
		}
	}

	// The completed rows were recorded as real decisions in history.
	var hist struct {
		Decisions []json.RawMessage `json:"decisions"`
	}
	api.Request(t, http.MethodGet, "/v1/decisions", nil, http.StatusOK, &hist)
	if len(hist.Decisions) < 2 {
		t.Fatalf("batch decisions not recorded in history: %d", len(hist.Decisions))
	}

	// An empty dataset is a 400.
	api.Request(t, http.MethodPost, "/v1/flows/batch/production/decide/batch", map[string]any{"dataset": []any{}}, http.StatusBadRequest, nil)
}

func TestDecideAppliesPolicyOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "scored", "name": "Scored"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "a", "type": "assignment", "config": map[string]any{"assignments": []map[string]any{{"target": "score", "expr": "score"}}}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"score"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
		},
	}, http.StatusCreated, nil)

	// A policy bound to the flow: high score auto-approves, else refer.
	var pol struct {
		PolicyID string `json:"policy_id"`
	}
	api.Request(t, http.MethodPost, "/v1/policies", map[string]any{"name": "scored-stp", "flow_slug": "scored"}, http.StatusCreated, &pol)
	api.Request(t, http.MethodPost, "/v1/policies/"+pol.PolicyID+"/versions", map[string]any{
		"spec": map[string]any{
			"rules":   []map[string]any{{"when": "score >= 0.85", "disposition": "approve", "code": "P-AUTO"}},
			"default": "refer",
		},
	}, http.StatusCreated, nil)

	type decideResp struct {
		DecisionID  string `json:"decision_id"`
		Status      string `json:"status"`
		Disposition string `json:"disposition"`
	}
	// Decide a high score → the policy auto-approves (retry while both the flow and
	// policy projections catch up).
	var dec decideResp
	if !testutil.Eventually(t, func() bool {
		dec = decideResp{}
		api.Request(t, http.MethodPost, "/v1/flows/scored/production/decide", map[string]any{"data": map[string]any{"score": 0.9}}, http.StatusOK, &dec)
		return dec.Status == "completed" && dec.Disposition == policy.Approve
	}) {
		t.Fatalf("policy never auto-approved: %+v", dec)
	}

	// The disposition is recorded first-class on the decision record.
	var rec history.Record
	api.Request(t, http.MethodGet, "/v1/decisions/"+dec.DecisionID, nil, http.StatusOK, &rec)
	if rec.Disposition != policy.Approve || rec.PolicyID != pol.PolicyID || rec.PolicyVersion != 1 {
		t.Fatalf("decision record missing policy disposition: %+v", rec)
	}

	// A low score refers (the residual that needs a human).
	var low decideResp
	api.Request(t, http.MethodPost, "/v1/flows/scored/production/decide", map[string]any{"data": map[string]any{"score": 0.2}}, http.StatusOK, &low)
	if low.Disposition != policy.Refer {
		t.Fatalf("low score should refer, got %q", low.Disposition)
	}

	// The dispositions roll up into analytics (the automation-rate breakdown).
	type metrics struct {
		ByDisposition map[string]int `json:"by_disposition"`
	}
	var m metrics
	if !testutil.Eventually(t, func() bool {
		m = metrics{}
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID+"/metrics", nil, http.StatusOK, &m)
		return m.ByDisposition["approve"] >= 1 && m.ByDisposition["refer"] >= 1
	}) {
		t.Fatalf("disposition breakdown not in metrics: %+v", m)
	}
}

func startEngine(t *testing.T, opts ...command.DecideOption) *testutil.API {
	t.Helper()
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), command.NewDecideHandler(log, st, opts...), st)
	pol := policy.New(policy.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	routes := func(mux *http.ServeMux) {
		svc.Routes(mux)
		pol.Routes(mux)
	}
	return testutil.StartAPI(t, log, st, "test-key", id, routes,
		flows.Projector{}, history.Projector{}, analytics.Projector{}, policy.Projector{})
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

	// Pin sandbox to v1 (a direct deploy, allowed for non-production environments).
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "sandbox", "version": 1}, http.StatusCreated, nil)
	if !testutil.Eventually(t, func() bool {
		var fv flows.FlowView
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID, nil, http.StatusOK, &fv)
		dep, ok := fv.Deployments["sandbox"]
		return ok && dep.Version == 1
	}) {
		t.Fatal("deployment never reached the registry projection")
	}

	// Decide sandbox -> the pinned v1, not the latest v2.
	var decision struct {
		Status string         `json:"status"`
		Data   map[string]any `json:"data"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/router/sandbox/decide",
		map[string]any{"data": map[string]any{}}, http.StatusOK, &decision)
	if decision.Status != "completed" || decision.Data["decision"] != "v1" {
		t.Fatalf("pinned decide: %+v", decision)
	}

	// Deploying an unpublished version is rejected.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "sandbox", "version": 9}, http.StatusBadRequest, nil)
}

func TestBacktestComparesVersions(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "bt", "name": "BT"}, http.StatusCreated, &created)
	// v1 outputs "A", v2 outputs "B" (the constant marker differs per version).
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("A")}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("B")}, http.StatusCreated, nil)

	// Backtest v1 (baseline) vs v2 (candidate) over two inputs: every record changes.
	var rep struct {
		Summary struct {
			Total              int  `json:"total"`
			Changed            int  `json:"changed"`
			BaselineCompleted  int  `json:"baseline_completed"`
			CandidateCompleted int  `json:"candidate_completed"`
			Compare            bool `json:"compare"`
		} `json:"summary"`
		Records []struct {
			Changed   bool `json:"changed"`
			Candidate struct {
				Output map[string]any `json:"output"`
			} `json:"candidate"`
		} `json:"records"`
	}
	if !testutil.Eventually(t, func() bool {
		rep.Summary.Total = 0
		api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/backtest", map[string]any{
			"version": 1, "compare_version": 2, "dataset": []map[string]any{{}, {}},
		}, http.StatusOK, &rep)
		return rep.Summary.Total == 2 && rep.Summary.CandidateCompleted == 2
	}) {
		t.Fatalf("backtest never reflected both versions: %+v", rep.Summary)
	}
	if !rep.Summary.Compare || rep.Summary.Changed != 2 || rep.Summary.BaselineCompleted != 2 {
		t.Fatalf("summary = %+v", rep.Summary)
	}
	// Only changed records are returned (both here), and the candidate output is "B".
	if len(rep.Records) != 2 || !rep.Records[0].Changed || rep.Records[0].Candidate.Output["decision"] != "B" {
		t.Fatalf("records = %+v", rep.Records)
	}

	// An empty dataset is rejected.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/backtest",
		map[string]any{"version": 1, "dataset": []map[string]any{}}, http.StatusBadRequest, nil)
}

func TestMakerCheckerDeploymentAPI(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "gov", "name": "Gov"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("v1")}, http.StatusCreated, nil)

	// A direct production deploy is refused — it must go through maker-checker.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "production", "version": 1}, http.StatusBadRequest, nil)

	// Propose a production deployment (the maker).
	var req struct {
		RequestID string `json:"request_id"`
		Status    string `json:"status"`
	}
	if !testutil.Eventually(t, func() bool {
		api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployment-requests",
			map[string]any{"environment": "production", "version": 1}, http.StatusCreated, &req)
		return req.RequestID != "" && req.Status == "pending"
	}) {
		t.Fatal("deployment request was not created")
	}

	// Four-eyes: the same caller (the proposer) cannot approve their own request.
	api.Request(t, http.MethodPost,
		"/v1/flows/"+created.FlowID+"/deployment-requests/"+req.RequestID+"/approve", nil, http.StatusBadRequest, nil)

	// The request is visible as pending on the flow (the audit/approval surface).
	if !testutil.Eventually(t, func() bool {
		var fv flows.FlowView
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID, nil, http.StatusOK, &fv)
		return len(fv.DeploymentRequests) == 1 && fv.DeploymentRequests[0].Status == "pending"
	}) {
		t.Fatal("pending deployment request not surfaced on the flow")
	}
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

func TestDecideRecordsReasonCodes(t *testing.T) {
	api := startEngine(t)

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "adverse", "name": "Adverse"}, http.StatusCreated, &created)
	graph := map[string]any{
		"nodes": []map[string]any{
			{"id": "in", "type": "input"},
			{"id": "r", "type": "reason", "config": map[string]any{"reasons": []map[string]any{
				{"when": "fico < 600", "code": "R01", "description": "Insufficient credit score"},
				{"when": "income < 30000", "code": "R02", "description": "Insufficient income"},
			}}},
			{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"decision"}}},
		},
		"edges": []map[string]any{{"from": "in", "to": "r"}, {"from": "r", "to": "out"}},
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": graph}, http.StatusCreated, nil)
	if !testutil.Eventually(t, func() bool {
		var fv flows.FlowView
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID, nil, http.StatusOK, &fv)
		return fv.Latest == 1
	}) {
		t.Fatal("flow registry projection never caught up")
	}

	var decision struct {
		DecisionID string `json:"decision_id"`
		Status     string `json:"status"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/adverse/production/decide",
		map[string]any{"data": map[string]any{"fico": 500, "income": 50000}}, http.StatusOK, &decision)
	if decision.Status != "completed" {
		t.Fatalf("decide status=%s", decision.Status)
	}

	// Only fico<600 matched, and the reason codes are lifted to the first-class
	// field on the decision record (the adverse-action explanation).
	if !testutil.Eventually(t, func() bool {
		var rec history.Record
		api.Request(t, http.MethodGet, "/v1/decisions/"+decision.DecisionID, nil, http.StatusOK, &rec)
		return len(rec.ReasonCodes) == 1 && rec.ReasonCodes[0].Code == "R01"
	}) {
		t.Fatal("reason codes never recorded on the decision")
	}
}
