// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/decision-engine/assertions"
	"github.com/e6qu/intraktible/decision-engine/command"
	"github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/decision-engine/export"
	"github.com/e6qu/intraktible/decision-engine/flows"
	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/decision-engine/internal/flowtest"
	"github.com/e6qu/intraktible/decision-engine/monitor"
	"github.com/e6qu/intraktible/decision-engine/notify"
	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/decision-engine/preapproval"
	"github.com/e6qu/intraktible/decision-engine/service"
	"github.com/e6qu/intraktible/decision-engine/shadow"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/privacy"
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

func TestImportFlowOverHTTP(t *testing.T) {
	api := startEngine(t)

	v1 := map[string]any{
		"slug": "iac",
		"name": "Flow as Code",
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "out", "type": "output"},
			},
			"edges": []map[string]any{{"from": "in", "to": "out"}},
		},
	}

	// First import creates the flow and publishes v1.
	var imp struct {
		FlowID    string `json:"flow_id"`
		Version   int    `json:"version"`
		Created   bool   `json:"created"`
		Published bool   `json:"published"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/import", v1, http.StatusCreated, &imp)
	if !imp.Created || !imp.Published || imp.Version != 1 || imp.FlowID == "" {
		t.Fatalf("first import: %+v", imp)
	}
	flowID := imp.FlowID

	// Re-importing identical content is a no-op (200, nothing published) even
	// back-to-back — the command folds the authoritative log, not the projection.
	imp = struct {
		FlowID    string `json:"flow_id"`
		Version   int    `json:"version"`
		Created   bool   `json:"created"`
		Published bool   `json:"published"`
	}{}
	api.Request(t, http.MethodPost, "/v1/flows/import", v1, http.StatusOK, &imp)
	if imp.Created || imp.Published || imp.Version != 1 || imp.FlowID != flowID {
		t.Fatalf("idempotent re-import: %+v", imp)
	}

	// A changed graph under the same slug publishes v2 onto the same flow.
	v2 := map[string]any{
		"slug": "iac",
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "s", "type": "split", "name": "route"},
				{"id": "out", "type": "output"},
			},
			"edges": []map[string]any{
				{"from": "in", "to": "s"},
				{"from": "s", "to": "out", "branch": "yes"},
			},
		},
	}
	api.Request(t, http.MethodPost, "/v1/flows/import", v2, http.StatusCreated, &imp)
	if imp.Created || !imp.Published || imp.Version != 2 || imp.FlowID != flowID {
		t.Fatalf("update import: %+v", imp)
	}

	// The flow ends up with both versions once the projection catches up.
	if !testutil.Eventually(t, func() bool {
		code, body := rawGet(t, api, "/v1/flows/"+flowID)
		return code == http.StatusOK && strings.Contains(body, `"latest":2`)
	}) {
		t.Fatal("imported flow never reached latest version 2")
	}

	// An exported doc round-trips back through import unchanged (no-op).
	code, raw := rawGet(t, api, "/v1/flows/"+flowID+"/export?format=json")
	if code != http.StatusOK {
		t.Fatalf("export for round-trip: %d\n%s", code, raw)
	}
	var fx export.FlowExport
	if err := json.Unmarshal([]byte(raw), &fx); err != nil {
		t.Fatalf("export not valid: %v", err)
	}
	api.Request(t, http.MethodPost, "/v1/flows/import", fx, http.StatusOK, &imp)
	if imp.Published {
		t.Fatalf("re-importing an export of the latest version should be a no-op: %+v", imp)
	}
}

type fieldSealer struct {
	v      *erasure.Vault
	fields map[string]bool
}

func (f fieldSealer) SealPII(ctx context.Context, id identity.Identity, subject string, doc json.RawMessage) (json.RawMessage, error) {
	return f.v.SealFields(ctx, id, subject, doc, f.fields)
}

func TestDecisionRecordErasure(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	vault := erasure.NewVault(st)
	decide := command.NewDecideHandler(log, st,
		command.WithPIISealer(fieldSealer{v: vault, fields: map[string]bool{"ssn": true}}))
	svc := service.New(command.NewHandler(log), decide, preapproval.NewHandler(log), st)
	svc.UseEraser(vault)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	api := testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, flows.Projector{}, history.Projector{})

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "kyc", "name": "KYC"}, http.StatusCreated, &created)
	// The flow reads only the non-PII "score"; "ssn" rides along in the input.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "a", "type": "assignment", "config": map[string]any{
					"assignments": []map[string]any{{"target": "decision", "expr": `score > 0 ? "OK":"NO"`}},
				}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"decision"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
		},
	}, http.StatusCreated, nil)

	var dec struct {
		DecisionID string `json:"decision_id"`
		Status     string `json:"status"`
	}
	if !testutil.Eventually(t, func() bool {
		api.Request(t, http.MethodPost, "/v1/flows/kyc/sandbox/decide", map[string]any{
			"entity_type": "customer", "entity_id": "ada",
			"data": map[string]any{"score": 1, "ssn": "123-45-6789"},
		}, http.StatusOK, &dec)
		return dec.Status == "completed"
	}) {
		t.Fatal("decide never completed")
	}

	// The recorded decision input has the SSN sealed at rest. Poll: the history
	// projection applies the decision event asynchronously off the bus, so a read
	// immediately after the decide HTTP response can race it (flaky under -race).
	var rec history.Record
	if !testutil.Eventually(t, func() bool {
		var rerr error
		rec, _, rerr = history.Read(context.Background(), st, id, dec.DecisionID)
		return rerr == nil && strings.Contains(string(rec.Data), "$intraktible_erased")
	}) {
		t.Fatalf("decision SSN not sealed at rest: %s", rec.Data)
	}
	if strings.Contains(string(rec.Data), "123-45-6789") {
		t.Fatalf("raw SSN present in recorded decision: %s", rec.Data)
	}

	readSSN := func() string {
		var got struct {
			Data map[string]any `json:"data"`
		}
		api.Request(t, http.MethodGet, "/v1/decisions/"+dec.DecisionID, nil, http.StatusOK, &got)
		s, _ := got.Data["ssn"].(string)
		return s
	}
	// An authorized read unseals it; the non-PII score is untouched.
	if got := readSSN(); got != "123-45-6789" {
		t.Fatalf("unsealed decision ssn = %q", got)
	}

	// Erasing the subject shreds the recorded decision PII.
	if err := vault.Erase(context.Background(), id, "customer/ada"); err != nil {
		t.Fatal(err)
	}
	if got := readSSN(); got != "[erased]" {
		t.Fatalf("decision ssn after erasure = %q, want [erased]", got)
	}
}

// PII that flows through a NODE output (not just the final input/output) is sealed
// at rest and erasable too — otherwise the node trace would defeat crypto-shred.
func TestNodeTraceErasure(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	vault := erasure.NewVault(st)
	decide := command.NewDecideHandler(log, st,
		command.WithPIISealer(fieldSealer{v: vault, fields: map[string]bool{"ssn": true}}))
	svc := service.New(command.NewHandler(log), decide, preapproval.NewHandler(log), st)
	svc.UseEraser(vault)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	api := testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, flows.Projector{}, history.Projector{})

	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "trace", "name": "Trace"}, http.StatusCreated, &created)
	// An assignment node echoes the PII "ssn" into its output, so the node-trace
	// output carries PII that must be sealed.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "a", "type": "assignment", "config": map[string]any{"assignments": []map[string]any{{"target": "ssn", "expr": "ssn"}}}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"ssn"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
		},
	}, http.StatusCreated, nil)

	var dec struct {
		DecisionID string `json:"decision_id"`
		Status     string `json:"status"`
	}
	if !testutil.Eventually(t, func() bool {
		dec = struct {
			DecisionID string `json:"decision_id"`
			Status     string `json:"status"`
		}{}
		api.Request(t, http.MethodPost, "/v1/flows/trace/sandbox/decide", map[string]any{
			"entity_type": "customer", "entity_id": "ada", "data": map[string]any{"ssn": "123-45-6789"},
		}, http.StatusOK, &dec)
		return dec.Status == "completed"
	}) {
		t.Fatal("decide never completed")
	}

	// The 'a' node's recorded output has the SSN sealed at rest.
	var rec history.Record
	if !testutil.Eventually(t, func() bool {
		rec = history.Record{}
		var rerr error
		rec, _, rerr = history.Read(context.Background(), st, id, dec.DecisionID)
		return rerr == nil && len(rec.Nodes) > 0
	}) {
		t.Fatal("decision record never projected")
	}
	var aOut string
	for _, n := range rec.Nodes {
		if n.NodeID == "a" {
			aOut = string(n.Output)
		}
	}
	if aOut == "" || strings.Contains(aOut, "123-45-6789") || !strings.Contains(aOut, "$intraktible_erased") {
		t.Fatalf("node 'a' output not sealed at rest: %s", aOut)
	}

	nodeSSN := func() string {
		var got struct {
			Nodes []struct {
				NodeID string         `json:"node_id"`
				Output map[string]any `json:"output"`
			} `json:"nodes"`
		}
		api.Request(t, http.MethodGet, "/v1/decisions/"+dec.DecisionID, nil, http.StatusOK, &got)
		for _, n := range got.Nodes {
			if n.NodeID == "a" {
				s, _ := n.Output["ssn"].(string)
				return s
			}
		}
		return ""
	}
	if got := nodeSSN(); got != "123-45-6789" {
		t.Fatalf("authorized read should unseal the node-trace ssn, got %q", got)
	}
	if err := vault.Erase(context.Background(), id, "customer/ada"); err != nil {
		t.Fatal(err)
	}
	if got := nodeSSN(); got != "[erased]" {
		t.Fatalf("node-trace ssn after erasure = %q, want [erased]", got)
	}
}

func TestWhatifOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "whatif", "name": "What If"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "a", "type": "assignment", "config": map[string]any{
					"assignments": []map[string]any{{"target": "decision", "expr": `score > 5 ? "A":"B"`}},
				}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"decision"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
		},
	}, http.StatusCreated, nil)

	var rep struct {
		Field       string `json:"field"`
		Transitions int    `json:"transitions"`
		Points      []struct {
			Value   float64        `json:"value"`
			Output  map[string]any `json:"output"`
			Changed bool           `json:"changed"`
		} `json:"points"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/whatif", map[string]any{
		"base": map[string]any{}, "field": "score", "values": []any{1, 3, 7, 9},
	}, http.StatusOK, &rep)
	if rep.Field != "score" || len(rep.Points) != 4 || rep.Transitions != 1 {
		t.Fatalf("whatif report = %+v", rep)
	}
	if rep.Points[0].Output["decision"] != "B" || rep.Points[3].Output["decision"] != "A" {
		t.Fatalf("whatif outcomes = %+v", rep.Points)
	}

	// A missing field is a 400.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/whatif",
		map[string]any{"values": []any{1}}, http.StatusBadRequest, nil)
}

func TestShadowEvaluationOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "shadowflow", "name": "Shadow"}, http.StatusCreated, &created)

	publish := func(decision string) {
		api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
			"graph": map[string]any{
				"nodes": []map[string]any{
					{"id": "in", "type": "input"},
					{"id": "a", "type": "assignment", "config": map[string]any{
						"assignments": []map[string]any{{"target": "decision", "expr": "'" + decision + "'"}},
					}},
					{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"decision"}}},
				},
				"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
			},
		}, http.StatusCreated, nil)
	}
	publish("A") // v1
	publish("B") // v2 — diverges
	publish("A") // v3 — agrees with v1

	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "sandbox", "version": 1}, http.StatusCreated, nil)
	api.Request(t, http.MethodPut, "/v1/flows/"+created.FlowID+"/shadow",
		map[string]any{"environment": "sandbox", "version": 2}, http.StatusOK, nil)

	// Wait until v1 is the live champion (the deploy projection caught up), so the
	// shadow (v2) actually runs alongside it rather than v2 being the latest.
	decide := func() string {
		var dec struct {
			Status string         `json:"status"`
			Data   map[string]any `json:"data"`
		}
		api.Request(t, http.MethodPost, "/v1/flows/shadowflow/sandbox/decide",
			map[string]any{"data": map[string]any{}}, http.StatusOK, &dec)
		if dec.Status != "completed" {
			return ""
		}
		s, _ := dec.Data["decision"].(string)
		return s
	}
	if !testutil.Eventually(t, func() bool { return decide() == "A" }) {
		t.Fatal("v1 never became the live champion")
	}

	type envShadow struct {
		ShadowVersion int `json:"shadow_version"`
		Total         int `json:"total"`
		Matched       int `json:"matched"`
		Diverged      int `json:"diverged"`
	}
	readReport := func() (map[string]int, envShadow) {
		var got struct {
			Shadows map[string]int       `json:"shadows"`
			Report  map[string]envShadow `json:"report"`
		}
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID+"/shadow", nil, http.StatusOK, &got)
		return got.Shadows, got.Report["sandbox"]
	}

	// The shadow (v2 → "B") diverges from the live champion (v1 → "A").
	if !testutil.Eventually(t, func() bool {
		shadows, env := readReport()
		return shadows["sandbox"] == 2 && env.ShadowVersion == 2 && env.Diverged >= 1 && env.Matched == 0
	}) {
		_, env := readReport()
		t.Fatalf("expected divergence under shadow v2, got %+v", env)
	}

	// Switching the shadow to v3 (which agrees with v1) resets the comparison and
	// records matches instead.
	api.Request(t, http.MethodPut, "/v1/flows/"+created.FlowID+"/shadow",
		map[string]any{"environment": "sandbox", "version": 3}, http.StatusOK, nil)
	if !testutil.Eventually(t, func() bool {
		decide()
		_, env := readReport()
		return env.ShadowVersion == 3 && env.Matched >= 1 && env.Diverged == 0
	}) {
		_, env := readReport()
		t.Fatalf("expected matches under shadow v3, got %+v", env)
	}

	// Clearing the shadow removes the assignment.
	api.Request(t, http.MethodPut, "/v1/flows/"+created.FlowID+"/shadow",
		map[string]any{"environment": "sandbox", "version": 0}, http.StatusOK, nil)
	if !testutil.Eventually(t, func() bool {
		shadows, _ := readReport()
		return shadows["sandbox"] == 0
	}) {
		t.Fatal("clearing the shadow did not remove the assignment")
	}
}

func TestImportBundleOverHTTP(t *testing.T) {
	api := startEngine(t)

	okGraph := map[string]any{
		"nodes": []map[string]any{
			{"id": "in", "type": "input"},
			{"id": "out", "type": "output"},
		},
		"edges": []map[string]any{{"from": "in", "to": "out"}},
	}

	// A bundle with two good flows and one invalid (bad slug) flow.
	bundle := map[string]any{
		"flows": []map[string]any{
			{"slug": "iac-a", "name": "A", "graph": okGraph},
			{"slug": "iac-b", "name": "B", "graph": okGraph},
			{"slug": "Bad Slug", "name": "nope", "graph": okGraph},
		},
	}
	var out struct {
		Results []struct {
			Slug      string `json:"slug"`
			Version   int    `json:"version"`
			Created   bool   `json:"created"`
			Published bool   `json:"published"`
			Error     string `json:"error"`
		} `json:"results"`
		Published int `json:"published"`
		Failed    int `json:"failed"`
		Unchanged int `json:"unchanged"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/import-bundle", bundle, http.StatusOK, &out)
	if out.Published != 2 || out.Failed != 1 || len(out.Results) != 3 {
		t.Fatalf("bundle summary: %+v", out)
	}
	// The good flows are created; the invalid one carries an error and no version.
	for _, r := range out.Results {
		switch r.Slug {
		case "iac-a", "iac-b":
			if !r.Created || !r.Published || r.Version != 1 || r.Error != "" {
				t.Fatalf("good flow %q: %+v", r.Slug, r)
			}
		case "Bad Slug":
			if r.Error == "" || r.Published {
				t.Fatalf("invalid flow should report an error: %+v", r)
			}
		}
	}

	// Re-importing the same bundle is a no-op for the valid flows (idempotent).
	out.Published, out.Failed, out.Unchanged = 0, 0, 0
	api.Request(t, http.MethodPost, "/v1/flows/import-bundle", bundle, http.StatusOK, &out)
	if out.Published != 0 || out.Failed != 1 || out.Unchanged != 2 {
		t.Fatalf("idempotent re-import summary: %+v", out)
	}

	// An empty bundle is a 400.
	api.Request(t, http.MethodPost, "/v1/flows/import-bundle", map[string]any{"flows": []any{}}, http.StatusBadRequest, nil)
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
	// Poll until the FULL trace is projected: the history projection applies the
	// node-evaluated events before the terminal DecisionCompleted, so waiting only
	// for the node line can observe a partial trace missing "completed" (flaky).
	var trace string
	if !testutil.Eventually(t, func() bool {
		code, body := rawGet(t, api, "/v1/decisions/"+dec.DecisionID+"/export")
		trace = body
		return code == http.StatusOK &&
			strings.Contains(body, "Note over E: a (assignment)") &&
			strings.Contains(body, "E-->>C: completed")
	}) {
		t.Fatalf("decision trace export incomplete:\n%s", trace)
	}
	if !strings.Contains(trace, "sequenceDiagram") {
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

type fakeCompleter struct{ graph string }

func (fakeCompleter) Complete(_ context.Context, _, prompt string) (string, error) {
	return "FAKE-COMPLETION for: " + prompt, nil
}

func (f fakeCompleter) CompleteJSON(_ context.Context, _, _ string, _ json.RawMessage) (json.RawMessage, error) {
	if f.graph == "" {
		return json.RawMessage(`{}`), nil // an unusable graph (no input/output) → 422
	}
	return json.RawMessage(f.graph), nil
}

func TestCopilotGenerate(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), command.NewDecideHandler(log, st), preapproval.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}

	// A completer returning a valid graph → 200 with an applyable graph.
	good := fakeCompleter{graph: `{"nodes":[{"id":"in","type":"input"},{"id":"out","type":"output","config":{"fields":["x"]}}],"edges":[{"from":"in","to":"out"}]}`}
	svc.UseCopilot(good)
	api := testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, flows.Projector{})
	var gen struct {
		Graph struct {
			Nodes []map[string]any `json:"nodes"`
		} `json:"graph"`
	}
	api.Request(t, http.MethodPost, "/v1/copilot/generate", map[string]any{"prompt": "approve everyone"}, http.StatusOK, &gen)
	if len(gen.Graph.Nodes) != 2 {
		t.Fatalf("generated graph nodes = %d, want 2", len(gen.Graph.Nodes))
	}

	// A completer returning an unusable graph → 422 (validated server-side, never applied).
	log2, st2 := testutil.NewLogStore(t)
	svc2 := service.New(command.NewHandler(log2), command.NewDecideHandler(log2, st2), preapproval.NewHandler(log2), st2)
	svc2.UseCopilot(fakeCompleter{})
	api2 := testutil.StartAPI(t, log2, st2, "test-key", id, svc2.Routes, flows.Projector{})
	api2.Request(t, http.MethodPost, "/v1/copilot/generate", map[string]any{"prompt": "x"}, http.StatusUnprocessableEntity, nil)
}

func TestCopilotOverHTTP(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), command.NewDecideHandler(log, st), preapproval.NewHandler(log), st)
	svc.UseCopilot(fakeCompleter{})
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	api := testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, flows.Projector{}, history.Projector{})

	var explain struct {
		Text string `json:"text"`
	}
	api.Request(t, http.MethodPost, "/v1/copilot/explain", map[string]any{
		"graph": map[string]any{"nodes": []any{map[string]any{"id": "in", "type": "input"}}, "edges": []any{}},
	}, http.StatusOK, &explain)
	if !strings.Contains(explain.Text, "FAKE-COMPLETION") {
		t.Fatalf("explain text = %q", explain.Text)
	}

	var suggest struct {
		Text string `json:"text"`
	}
	api.Request(t, http.MethodPost, "/v1/copilot/suggest", map[string]any{"prompt": "approve when fico >= 700"}, http.StatusOK, &suggest)
	if !strings.Contains(suggest.Text, "FAKE-COMPLETION") {
		t.Fatalf("suggest text = %q", suggest.Text)
	}

	// An empty suggest prompt is a 400.
	api.Request(t, http.MethodPost, "/v1/copilot/suggest", map[string]any{"prompt": ""}, http.StatusBadRequest, nil)
}

func TestCopilotUnconfiguredReturns503(t *testing.T) {
	// startEngine wires no copilot.
	api := startEngine(t)
	api.Request(t, http.MethodPost, "/v1/copilot/suggest", map[string]any{"prompt": "x"}, http.StatusServiceUnavailable, nil)
}

func TestDecideStreamOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "stream", "name": "Stream"}, http.StatusCreated, &created)
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

	// Stream three NDJSON rows: two valid, one missing the required field → rejected.
	ndjson := "{\"x\":1}\n{\"x\":2}\n{}\n"
	var body string
	if !testutil.Eventually(t, func() bool {
		req, err := http.NewRequest(http.MethodPost, api.Server.URL+"/v1/flows/stream/production/decide/stream", strings.NewReader(ndjson))
		if err != nil {
			return false
		}
		req.Header.Set("X-Api-Key", api.Key)
		resp, err := api.Server.Client().Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
		return strings.Count(body, `"status":"completed"`) == 2
	}) {
		t.Fatalf("stream never completed two rows:\n%s", body)
	}
	if !strings.Contains(body, `"status":"rejected"`) {
		t.Fatalf("expected one rejected row (missing required field):\n%s", body)
	}
	// One NDJSON result line per input row.
	if lines := strings.Split(strings.TrimSpace(body), "\n"); len(lines) != 3 {
		t.Fatalf("expected 3 NDJSON result lines, got %d:\n%s", len(lines), body)
	}
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

	// The disposition is recorded first-class on the decision record (poll: the
	// history projection applies the decision event asynchronously, so the record
	// can 404 or read stale immediately after the decide response).
	var rec history.Record
	if !testutil.Eventually(t, func() bool {
		rec = history.Record{}
		if api.RequestStatus(t, http.MethodGet, "/v1/decisions/"+dec.DecisionID, nil, &rec) != http.StatusOK {
			return false
		}
		return rec.Disposition == policy.Approve && rec.PolicyID == pol.PolicyID && rec.PolicyVersion == 1
	}) {
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

func TestPreApprovalOverHTTP(t *testing.T) {
	api := startEngine(t)
	var granted struct {
		PreApprovalID string `json:"preapproval_id"`
	}
	api.Request(t, http.MethodPost, "/v1/preapprovals", map[string]any{
		"entity_type": "applicant", "entity_id": "acme", "disposition": "approve",
		"terms": map[string]any{"limit": 15000}, "valid_days": 90, "policy_id": "p1",
	}, http.StatusCreated, &granted)
	if granted.PreApprovalID == "" {
		t.Fatal("grant returned no id")
	}

	// It is readable by entity (retry while the projection catches up).
	type view struct {
		Disposition string `json:"disposition"`
		Status      string `json:"status"`
	}
	var v view
	if !testutil.Eventually(t, func() bool {
		v = view{}
		api.Request(t, http.MethodGet, "/v1/preapprovals/applicant/acme", nil, http.StatusOK, &v)
		return v.Status == "active" && v.Disposition == "approve"
	}) {
		t.Fatalf("pre-approval not active: %+v", v)
	}

	// Revoking flips it to revoked.
	api.Request(t, http.MethodPost, "/v1/preapprovals/applicant/acme/revoke", map[string]any{"reason": "fraud"}, http.StatusOK, nil)
	if !testutil.Eventually(t, func() bool {
		v = view{}
		api.Request(t, http.MethodGet, "/v1/preapprovals/applicant/acme", nil, http.StatusOK, &v)
		return v.Status == "revoked"
	}) {
		t.Fatalf("pre-approval not revoked: %+v", v)
	}
}

func TestDecideHonorsPreApprovalOverHTTP(t *testing.T) {
	api := startEngine(t)
	var flow struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "honored", "name": "Honored"}, http.StatusCreated, &flow)
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{{"id": "in", "type": "input"}, {"id": "out", "type": "output"}},
			"edges": []map[string]any{{"from": "in", "to": "out"}},
		},
	}, http.StatusCreated, nil)

	// Pre-approve the entity with an offer.
	api.Request(t, http.MethodPost, "/v1/preapprovals", map[string]any{
		"entity_type": "applicant", "entity_id": "acme", "disposition": "approve",
		"terms": map[string]any{"limit": 5000}, "valid_days": 30,
	}, http.StatusCreated, nil)

	// Deciding for that entity is served instantly from the pre-approval: approved,
	// with the stored terms as the output, and no flow run.
	type decResp struct {
		DecisionID  string         `json:"decision_id"`
		Status      string         `json:"status"`
		Disposition string         `json:"disposition"`
		Data        map[string]any `json:"data"`
	}
	var d decResp
	if !testutil.Eventually(t, func() bool {
		d = decResp{}
		api.Request(t, http.MethodPost, "/v1/flows/honored/production/decide", map[string]any{
			"data": map[string]any{}, "entity_type": "applicant", "entity_id": "acme",
		}, http.StatusOK, &d)
		return d.Status == "completed" && d.Disposition == "approve"
	}) {
		t.Fatalf("pre-approval never honored: %+v", d)
	}
	if d.Data["limit"] != float64(5000) {
		t.Fatalf("honored decision should carry the pre-approval terms: %+v", d.Data)
	}

	// The decision record links the pre-approval and has no node trace (flow
	// skipped). Poll: the history projection applies asynchronously.
	var rec history.Record
	if !testutil.Eventually(t, func() bool {
		rec = history.Record{}
		if api.RequestStatus(t, http.MethodGet, "/v1/decisions/"+d.DecisionID, nil, &rec) != http.StatusOK {
			return false
		}
		return rec.PreApprovalID != "" && len(rec.Nodes) == 0
	}) {
		t.Fatalf("expected a honored record with no node trace: %+v", rec)
	}
}

func TestPreApproveBatchOverHTTP(t *testing.T) {
	api := startEngine(t)
	var flow struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "bulk", "name": "Bulk"}, http.StatusCreated, &flow)
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "a", "type": "assignment", "config": map[string]any{"assignments": []map[string]any{{"target": "score", "expr": "score"}}}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"score"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
		},
	}, http.StatusCreated, nil)

	// Bind a policy: score >= 0.8 -> approve, else refer.
	var pol struct {
		PolicyID string `json:"policy_id"`
	}
	api.Request(t, http.MethodPost, "/v1/policies", map[string]any{"name": "stp", "flow_slug": "bulk"}, http.StatusCreated, &pol)
	api.Request(t, http.MethodPost, "/v1/policies/"+pol.PolicyID+"/versions", map[string]any{
		"spec": map[string]any{"rules": []map[string]any{{"when": "score >= 0.8", "disposition": "approve"}}, "default": "refer"},
	}, http.StatusCreated, nil)

	// Promote a population: two approve (score>=0.8), one refer, one missing id.
	type result struct {
		Index         int    `json:"index"`
		EntityID      string `json:"entity_id"`
		Status        string `json:"status"`
		Disposition   string `json:"disposition"`
		Granted       bool   `json:"granted"`
		PreApprovalID string `json:"preapproval_id"`
		Reason        string `json:"reason"`
	}
	var resp struct {
		Total    int      `json:"total"`
		Granted  int      `json:"granted"`
		Skipped  int      `json:"skipped"`
		Rejected int      `json:"rejected"`
		Results  []result `json:"results"`
	}
	dataset := []map[string]any{
		{"applicant_id": "a1", "score": 0.95},
		{"applicant_id": "a2", "score": 0.5},
		{"applicant_id": "a3", "score": 0.85},
		{"score": 0.99}, // no applicant_id -> rejected
	}
	if !testutil.Eventually(t, func() bool {
		resp.Granted, resp.Skipped, resp.Rejected = 0, 0, 0
		api.Request(t, http.MethodPost, "/v1/flows/bulk/production/preapprove/batch", map[string]any{
			"dataset": dataset, "entity_type": "applicant", "entity_key": "applicant_id", "valid_days": 30,
		}, http.StatusOK, &resp)
		return resp.Granted == 2 // wait until the policy projection resolves
	}) {
		t.Fatalf("expected 2 grants once the policy resolves: %+v", resp)
	}
	if resp.Total != 4 || resp.Skipped != 1 || resp.Rejected != 1 {
		t.Fatalf("unexpected batch tally: %+v", resp)
	}

	// The approved entities are now honored instantly at decide — terms carry the score.
	var d struct {
		Status      string         `json:"status"`
		Disposition string         `json:"disposition"`
		Data        map[string]any `json:"data"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/bulk/production/decide", map[string]any{
		"data": map[string]any{}, "entity_type": "applicant", "entity_id": "a1",
	}, http.StatusOK, &d)
	if d.Status != "completed" || d.Disposition != "approve" || d.Data["score"] != 0.95 {
		t.Fatalf("expected a1 honored from the batch grant: %+v", d)
	}
}

func TestMonitorsOverHTTP(t *testing.T) {
	api := startEngine(t)
	var flow struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "watched", "name": "Watched"}, http.StatusCreated, &flow)
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{{"id": "in", "type": "input"}, {"id": "out", "type": "output"}},
			"edges": []map[string]any{{"from": "in", "to": "out"}},
		},
	}, http.StatusCreated, nil)

	// Define a volume monitor: fire when more than 2 decisions have run.
	var def struct {
		MonitorID string `json:"monitor_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/monitors", map[string]any{
		"metric": "volume", "op": "gt", "threshold": 2, "description": "traffic",
	}, http.StatusCreated, &def)

	type status struct {
		Actual     float64 `json:"actual"`
		Computable bool    `json:"computable"`
		Firing     bool    `json:"firing"`
	}
	type mon struct {
		MonitorID string `json:"monitor_id"`
		Metric    string `json:"metric"`
		Status    status `json:"status"`
	}
	var listed struct {
		Monitors []mon `json:"monitors"`
	}

	// With no decisions yet, the rule is computable (volume 0) but not firing.
	api.Request(t, http.MethodGet, "/v1/flows/"+flow.FlowID+"/monitors", nil, http.StatusOK, &listed)
	if len(listed.Monitors) != 1 || listed.Monitors[0].Status.Firing {
		t.Fatalf("expected one non-firing monitor before traffic: %+v", listed.Monitors)
	}

	// Run three decisions; the volume monitor then fires (3 > 2).
	for range 3 {
		api.Request(t, http.MethodPost, "/v1/flows/watched/production/decide", map[string]any{"data": map[string]any{}}, http.StatusOK, nil)
	}
	if !testutil.Eventually(t, func() bool {
		listed.Monitors = nil
		api.Request(t, http.MethodGet, "/v1/flows/"+flow.FlowID+"/monitors", nil, http.StatusOK, &listed)
		return len(listed.Monitors) == 1 && listed.Monitors[0].Status.Firing
	}) {
		t.Fatalf("volume monitor never fired: %+v", listed.Monitors)
	}
	if listed.Monitors[0].Status.Actual != 3 {
		t.Fatalf("expected actual volume 3: %+v", listed.Monitors[0].Status)
	}

	// Deleting it removes it from the list.
	api.Request(t, http.MethodDelete, "/v1/flows/"+flow.FlowID+"/monitors/"+def.MonitorID, nil, http.StatusOK, nil)
	if !testutil.Eventually(t, func() bool {
		listed.Monitors = nil
		api.Request(t, http.MethodGet, "/v1/flows/"+flow.FlowID+"/monitors", nil, http.StatusOK, &listed)
		return len(listed.Monitors) == 0
	}) {
		t.Fatalf("monitor not deleted: %+v", listed.Monitors)
	}
}

func TestMonitorCheckDeliversToWebhookOverHTTP(t *testing.T) {
	api := startEngine(t)

	// A webhook target that records the payload it receives.
	var mu sync.Mutex
	var received []map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p map[string]any
		_ = json.Unmarshal(b, &p)
		mu.Lock()
		received = append(received, p)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	api.Request(t, http.MethodPost, "/v1/webhooks", map[string]any{"url": hook.URL, "note": "ops"}, http.StatusCreated, nil)

	var flow struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "alerted", "name": "Alerted"}, http.StatusCreated, &flow)
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{{"id": "in", "type": "input"}, {"id": "out", "type": "output"}},
			"edges": []map[string]any{{"from": "in", "to": "out"}},
		},
	}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/monitors", map[string]any{
		"metric": "volume", "op": "gt", "threshold": 0, "description": "any traffic",
	}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/alerted/production/decide", map[string]any{"data": map[string]any{}}, http.StatusOK, nil)

	// Checking the flow fires the monitor and delivers it to the webhook.
	type delivery struct {
		OK     bool `json:"ok"`
		Status int  `json:"status"`
	}
	var checkResp struct {
		Fired      []map[string]any `json:"fired"`
		Deliveries []delivery       `json:"deliveries"`
	}
	if !testutil.Eventually(t, func() bool {
		checkResp.Fired, checkResp.Deliveries = nil, nil
		api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/monitors/check", nil, http.StatusOK, &checkResp)
		return len(checkResp.Fired) == 1 && len(checkResp.Deliveries) == 1 && checkResp.Deliveries[0].OK
	}) {
		t.Fatalf("monitor check never delivered: %+v", checkResp)
	}

	mu.Lock()
	gotPayload := len(received) > 0 && received[0]["flow_id"] == flow.FlowID
	mu.Unlock()
	if !gotPayload {
		t.Fatalf("webhook did not receive the firing payload: %+v", received)
	}

	// The delivery is recorded on the webhook's read model.
	type webhook struct {
		DeliveryCount int  `json:"delivery_count"`
		LastOK        bool `json:"last_ok"`
	}
	var listed struct {
		Webhooks []webhook `json:"webhooks"`
	}
	if !testutil.Eventually(t, func() bool {
		listed.Webhooks = nil
		api.Request(t, http.MethodGet, "/v1/webhooks", nil, http.StatusOK, &listed)
		return len(listed.Webhooks) == 1 && listed.Webhooks[0].DeliveryCount >= 1 && listed.Webhooks[0].LastOK
	}) {
		t.Fatalf("delivery not recorded on the webhook: %+v", listed.Webhooks)
	}
}

func TestPrivacyMaskingOverHTTP(t *testing.T) {
	api := startEngine(t)
	var flow struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "pii", "name": "PII"}, http.StatusCreated, &flow)
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "a", "type": "assignment", "config": map[string]any{"assignments": []map[string]any{
					{"target": "ssn", "expr": "ssn"}, {"target": "amount", "expr": "amount"},
				}}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"ssn", "amount"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
		},
	}, http.StatusCreated, nil)

	// Classify "ssn" as sensitive.
	api.Request(t, http.MethodPut, "/v1/privacy", map[string]any{"fields": []string{"SSN"}}, http.StatusOK, nil)

	var dec struct {
		DecisionID string `json:"decision_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/pii/production/decide",
		map[string]any{"data": map[string]any{"ssn": "123-45-6789", "amount": 100}}, http.StatusOK, &dec)

	// The decision detail masks ssn but leaves amount intact, in both input and output.
	var rec struct {
		Data   map[string]any `json:"data"`
		Output map[string]any `json:"output"`
		Status string         `json:"status"`
	}
	if !testutil.Eventually(t, func() bool {
		rec = struct {
			Data   map[string]any `json:"data"`
			Output map[string]any `json:"output"`
			Status string         `json:"status"`
		}{}
		api.Request(t, http.MethodGet, "/v1/decisions/"+dec.DecisionID, nil, http.StatusOK, &rec)
		return rec.Status == "completed"
	}) {
		t.Fatalf("decision never recorded: %+v", rec)
	}
	if rec.Data["ssn"] != "[redacted]" || rec.Data["amount"] != float64(100) {
		t.Fatalf("input masking wrong: %+v", rec.Data)
	}
	if rec.Output["ssn"] != "[redacted]" || rec.Output["amount"] != float64(100) {
		t.Fatalf("output masking wrong: %+v", rec.Output)
	}

	// The JSON export is masked too.
	_, body := rawGet(t, api, "/v1/decisions/"+dec.DecisionID+"/export?format=json")
	if !strings.Contains(body, "[redacted]") || strings.Contains(body, "123-45-6789") {
		t.Fatalf("export not masked: %s", body)
	}
}

func TestDistributionDriftOverHTTP(t *testing.T) {
	api := startEngine(t)
	var flow struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "drifty", "name": "Drifty"}, http.StatusCreated, &flow)
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "a", "type": "assignment", "config": map[string]any{"assignments": []map[string]any{{"target": "score", "expr": "score"}}}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"score"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
		},
	}, http.StatusCreated, nil)
	// Policy: score >= 0.5 approve, else refer.
	var pol struct {
		PolicyID string `json:"policy_id"`
	}
	api.Request(t, http.MethodPost, "/v1/policies", map[string]any{"name": "d", "flow_slug": "drifty"}, http.StatusCreated, &pol)
	api.Request(t, http.MethodPost, "/v1/policies/"+pol.PolicyID+"/versions", map[string]any{
		"spec": map[string]any{"rules": []map[string]any{{"when": "score >= 0.5", "disposition": "approve"}}, "default": "refer"},
	}, http.StatusCreated, nil)

	decide := func(score float64) {
		api.Request(t, http.MethodPost, "/v1/flows/drifty/production/decide",
			map[string]any{"data": map[string]any{"score": score}}, http.StatusOK, nil)
	}
	// Baseline mix: one approve, one refer → 50/50.
	decide(0.9)
	decide(0.1)
	type metrics struct {
		ByDisposition map[string]int `json:"by_disposition"`
	}
	var m metrics
	if !testutil.Eventually(t, func() bool {
		m = metrics{}
		api.Request(t, http.MethodGet, "/v1/flows/"+flow.FlowID+"/metrics", nil, http.StatusOK, &m)
		return m.ByDisposition["approve"] == 1 && m.ByDisposition["refer"] == 1
	}) {
		t.Fatalf("baseline metrics never settled: %+v", m.ByDisposition)
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/baseline", nil, http.StatusCreated, nil)

	// Shift the mix hard toward approve: eight more approvals → 9 approve / 1 refer.
	for range 8 {
		decide(0.9)
	}
	type drift struct {
		HasBaseline bool    `json:"has_baseline"`
		MaxDrift    float64 `json:"max_drift"`
	}
	var d drift
	if !testutil.Eventually(t, func() bool {
		d = drift{}
		api.Request(t, http.MethodGet, "/v1/flows/"+flow.FlowID+"/drift", nil, http.StatusOK, &d)
		return d.HasBaseline && d.MaxDrift > 0.35 // approve 0.5 -> 0.9 ≈ 0.4
	}) {
		t.Fatalf("drift never registered the shift: %+v", d)
	}

	// A distribution_drift monitor fires on that shift.
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/monitors",
		map[string]any{"metric": "distribution_drift", "op": "gt", "threshold": 0.2}, http.StatusCreated, nil)
	var listed struct {
		Monitors []struct {
			Status struct {
				Firing bool `json:"firing"`
			} `json:"status"`
		} `json:"monitors"`
	}
	if !testutil.Eventually(t, func() bool {
		listed.Monitors = nil
		api.Request(t, http.MethodGet, "/v1/flows/"+flow.FlowID+"/monitors", nil, http.StatusOK, &listed)
		return len(listed.Monitors) == 1 && listed.Monitors[0].Status.Firing
	}) {
		t.Fatalf("distribution_drift monitor never fired: %+v", listed.Monitors)
	}
}

func TestPolicyBacktestOverHTTP(t *testing.T) {
	api := startEngine(t)
	var flow struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "btscore", "name": "BTScore"}, http.StatusCreated, &flow)
	api.Request(t, http.MethodPost, "/v1/flows/"+flow.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "a", "type": "assignment", "config": map[string]any{"assignments": []map[string]any{{"target": "score", "expr": "score"}}}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"score"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "a"}, {"from": "a", "to": "out"}},
		},
	}, http.StatusCreated, nil)

	var pol struct {
		PolicyID string `json:"policy_id"`
	}
	api.Request(t, http.MethodPost, "/v1/policies", map[string]any{"name": "bt", "flow_slug": "btscore"}, http.StatusCreated, &pol)
	// Published v1: strict cutoff 0.85.
	api.Request(t, http.MethodPost, "/v1/policies/"+pol.PolicyID+"/versions", map[string]any{
		"spec": map[string]any{"rules": []map[string]any{{"when": "score >= 0.85", "disposition": "approve"}}, "default": "refer"},
	}, http.StatusCreated, nil)

	// Backtest a looser draft (cutoff 0.4) against the published v1; the 0.5 row flips.
	type dist struct{ Approve, Decline, Refer, Failed int }
	var rep struct {
		Summary struct {
			Total     int
			Evaluated dist
			Compare   *dist
			Flipped   int
		} `json:"summary"`
	}
	if !testutil.Eventually(t, func() bool {
		rep.Summary.Compare = nil
		api.Request(t, http.MethodPost, "/v1/policies/"+pol.PolicyID+"/backtest", map[string]any{
			"spec":            map[string]any{"rules": []map[string]any{{"when": "score >= 0.4", "disposition": "approve"}}, "default": "refer"},
			"compare_version": 1,
			"dataset":         []map[string]any{{"score": 0.9}, {"score": 0.5}},
		}, http.StatusOK, &rep)
		return rep.Summary.Total == 2 && rep.Summary.Evaluated.Approve == 2
	}) {
		t.Fatalf("policy backtest never resolved the flow+policy: %+v", rep.Summary)
	}
	if rep.Summary.Compare == nil || rep.Summary.Compare.Approve != 1 || rep.Summary.Flipped != 1 {
		t.Fatalf("unexpected backtest diff: %+v", rep.Summary)
	}
}

// TestDecideEnvironmentScopeEnforced proves a managed key scoped to one environment
// is rejected (403) on another, while it still passes the scope gate for its own.
func TestDecideEnvironmentScopeEnforced(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	svc := service.New(command.NewHandler(log), command.NewDecideHandler(log, st), preapproval.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	api := testutil.StartAPIScoped(t, log, st, "sandbox-key", auth.Sandbox, id, svc.Routes,
		flows.Projector{}, history.Projector{})

	// A sandbox-scoped key may not decide against production.
	api.Request(t, http.MethodPost, "/v1/flows/x/production/decide",
		map[string]any{"data": map[string]any{}}, http.StatusForbidden, nil)
	// Against sandbox the scope gate passes (400 for the unknown flow, not 403).
	api.Request(t, http.MethodPost, "/v1/flows/x/sandbox/decide",
		map[string]any{"data": map[string]any{}}, http.StatusBadRequest, nil)
}

// TestFlowOpenAPIEndpoint proves the per-flow generated contract carries the flow's
// decide endpoints and embeds its published input schema.
func TestFlowOpenAPIEndpoint(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": "scoring", "name": "Scoring"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"fico"}}},
			},
			"edges": []map[string]any{{"from": "in", "to": "out"}},
		},
		"input_schema": map[string]any{
			"type": "object", "required": []string{"fico"},
			"properties": map[string]any{"fico": map[string]any{"type": "integer"}},
		},
	}, http.StatusCreated, nil)

	var doc map[string]any
	api.Request(t, http.MethodGet, "/v1/flows/scoring/openapi.json", nil, http.StatusOK, &doc)
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi version = %v, want 3.1.0", doc["openapi"])
	}
	paths, _ := doc["paths"].(map[string]any)
	if _, ok := paths["/v1/flows/scoring/{env}/decide"]; !ok {
		t.Fatalf("generated contract missing the flow's decide path; got %v", paths)
	}
	// The published input schema must be embedded as the request data schema.
	raw, _ := json.Marshal(doc)
	if !strings.Contains(string(raw), `"fico"`) {
		t.Fatalf("input schema not embedded in the per-flow contract:\n%s", raw)
	}
}

func startEngine(t *testing.T, opts ...command.DecideOption) *testutil.API {
	t.Helper()
	log, st := testutil.NewLogStore(t)
	paCmd := preapproval.NewHandler(log)
	svc := service.New(command.NewHandler(log), command.NewDecideHandler(log, st, opts...), paCmd, st)
	pol := policy.New(policy.NewHandler(log), st)
	pa := preapproval.New(paCmd, st)
	hooks := notify.New(notify.NewHandler(log), st)
	// Tests deliver to httptest (loopback), so use a plain client (no egress guard).
	mon := monitor.New(monitor.NewHandler(log), st, notify.NewNotifier(log, st, http.DefaultClient))
	priv := privacy.New(privacy.NewHandler(log), st)
	asserts := assertions.New(assertions.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	routes := func(mux *http.ServeMux) {
		svc.Routes(mux)
		pol.Routes(mux)
		pa.Routes(mux)
		hooks.Routes(mux)
		mon.Routes(mux)
		priv.Routes(mux)
		asserts.Routes(mux)
	}
	return testutil.StartAPI(t, log, st, "test-key", id, routes,
		flows.Projector{}, history.Projector{}, analytics.Projector{}, policy.Projector{}, preapproval.Projector{}, monitor.Projector{}, notify.Projector{}, privacy.Projector{}, assertions.Projector{}, shadow.Projector{})
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

	// Invalid environment -> 400 (sandbox/staging/production are the valid ones).
	api.Request(t, http.MethodPost, "/v1/flows/scoring/qa/decide",
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

func TestPromoteOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "promo", "name": "Promo"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("v1")}, http.StatusCreated, nil)

	// Pin sandbox to v1.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "sandbox", "version": 1}, http.StatusCreated, nil)
	if !testutil.Eventually(t, func() bool {
		var fv flows.FlowView
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID, nil, http.StatusOK, &fv)
		return fv.Deployments["sandbox"].Version == 1
	}) {
		t.Fatal("sandbox deploy never landed")
	}

	// Promote sandbox -> staging: a non-production target deploys directly.
	var promo struct {
		Promoted bool `json:"promoted"`
		Version  int  `json:"version"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/promote",
		map[string]any{"from": "sandbox", "to": "staging"}, http.StatusCreated, &promo)
	if !promo.Promoted || promo.Version != 1 {
		t.Fatalf("staging promote should deploy directly: %+v", promo)
	}
	if !testutil.Eventually(t, func() bool {
		var fv flows.FlowView
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID, nil, http.StatusOK, &fv)
		return fv.Deployments["staging"].Version == 1
	}) {
		t.Fatal("staging deployment never landed")
	}

	// Promote staging -> production: opens a maker-checker request, not a direct deploy.
	var prod struct {
		Promoted  bool   `json:"promoted"`
		Pending   bool   `json:"pending"`
		RequestID string `json:"request_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/promote",
		map[string]any{"from": "staging", "to": "production"}, http.StatusCreated, &prod)
	if prod.Promoted || !prod.Pending || prod.RequestID == "" {
		t.Fatalf("production promote should open a pending request: %+v", prod)
	}
	if !testutil.Eventually(t, func() bool {
		var fv flows.FlowView
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID, nil, http.StatusOK, &fv)
		// Production is not deployed yet (awaits a second approver); the request is pending.
		_, deployed := fv.Deployments["production"]
		return !deployed && len(fv.DeploymentRequests) == 1 && fv.DeploymentRequests[0].Status == "pending"
	}) {
		t.Fatal("production promote did not open a pending request")
	}

	// Promoting from an env with nothing deployed is rejected.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/promote",
		map[string]any{"from": "production", "to": "sandbox"}, http.StatusBadRequest, nil)
}

func TestAssertionsAndPromoteGateOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "asserted", "name": "Asserted"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("approve")}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "sandbox", "version": 1}, http.StatusCreated, nil)

	// Define two cases: one matches the constant output, one does not.
	api.Request(t, http.MethodPut, "/v1/flows/"+created.FlowID+"/assertions", map[string]any{
		"cases": []map[string]any{
			{"name": "ok", "input": map[string]any{}, "expect": map[string]any{"decision": "approve"}},
			{"name": "bad", "input": map[string]any{}, "expect": map[string]any{"decision": "decline"}},
		},
	}, http.StatusOK, nil)

	type runReport struct {
		Total, Passed, Failed int
	}
	var rep runReport
	if !testutil.Eventually(t, func() bool {
		rep = runReport{}
		api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/assertions/run", nil, http.StatusOK, &rep)
		return rep.Total == 2
	}) {
		t.Fatalf("assertions never ran against the published version: %+v", rep)
	}
	if rep.Passed != 1 || rep.Failed != 1 {
		t.Fatalf("expected 1 pass / 1 fail: %+v", rep)
	}

	// A failing assertion blocks promotion (409); force overrides.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/promote",
		map[string]any{"from": "sandbox", "to": "staging"}, http.StatusConflict, nil)

	// Fix the cases so all pass; wait for the projection, then the gate lets it through.
	api.Request(t, http.MethodPut, "/v1/flows/"+created.FlowID+"/assertions", map[string]any{
		"cases": []map[string]any{
			{"name": "ok", "input": map[string]any{}, "expect": map[string]any{"decision": "approve"}},
		},
	}, http.StatusOK, nil)
	if !testutil.Eventually(t, func() bool {
		rep = runReport{}
		api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/assertions/run", nil, http.StatusOK, &rep)
		return rep.Total == 1 && rep.Failed == 0
	}) {
		t.Fatalf("assertions never went green: %+v", rep)
	}
	var promo struct {
		Promoted bool `json:"promoted"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/promote",
		map[string]any{"from": "sandbox", "to": "staging"}, http.StatusCreated, &promo)
	if !promo.Promoted {
		t.Fatalf("promote should succeed once assertions pass: %+v", promo)
	}
}

func TestPromoteGateOnFiringMonitorOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "gated", "name": "Gated"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("v1")}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "sandbox", "version": 1}, http.StatusCreated, nil)
	// A monitor that fires on any traffic, then traffic to trip it.
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/monitors",
		map[string]any{"metric": "volume", "op": "gt", "threshold": 0}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/gated/sandbox/decide",
		map[string]any{"data": map[string]any{}}, http.StatusOK, nil)

	// Once the monitor is firing, an un-forced promote is blocked (409).
	type listed struct {
		Monitors []struct {
			Status struct {
				Firing bool `json:"firing"`
			} `json:"status"`
		} `json:"monitors"`
	}
	var ls listed
	if !testutil.Eventually(t, func() bool {
		ls.Monitors = nil
		api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID+"/monitors", nil, http.StatusOK, &ls)
		return len(ls.Monitors) == 1 && ls.Monitors[0].Status.Firing
	}) {
		t.Fatalf("monitor never fired: %+v", ls.Monitors)
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/promote",
		map[string]any{"from": "sandbox", "to": "staging"}, http.StatusConflict, nil)

	// Forcing the promote overrides the gate.
	var promo struct {
		Promoted bool `json:"promoted"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/promote",
		map[string]any{"from": "sandbox", "to": "staging", "force": true}, http.StatusCreated, &promo)
	if !promo.Promoted {
		t.Fatalf("forced promote should deploy: %+v", promo)
	}
}

func TestPromotionPolicyOverHTTP(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "stage-policy", "name": "Stage Policy"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("approve")}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "sandbox", "version": 1}, http.StatusCreated, nil)

	// Failing assertions would normally block sandbox -> staging.
	api.Request(t, http.MethodPut, "/v1/flows/"+created.FlowID+"/assertions", map[string]any{
		"cases": []map[string]any{{"name": "bad", "input": map[string]any{}, "expect": map[string]any{"decision": "decline"}}},
	}, http.StatusOK, nil)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/promote",
		map[string]any{"from": "sandbox", "to": "staging"}, http.StatusConflict, nil)

	// Configure staging: skip assertions, but require review.
	api.Request(t, http.MethodPut, "/v1/flows/"+created.FlowID+"/promotion-policy", map[string]any{
		"policy": map[string]any{
			"staging": map[string]any{"require_assertions": false, "require_review": true},
		},
	}, http.StatusOK, nil)
	var got struct {
		Policy map[string]events.PromotionStagePolicy `json:"policy"`
	}
	api.Request(t, http.MethodGet, "/v1/flows/"+created.FlowID+"/promotion-policy", nil, http.StatusOK, &got)
	if !got.Policy["staging"].RequireReview || got.Policy["staging"].RequireAssertions {
		t.Fatalf("promotion policy did not apply: %+v", got.Policy["staging"])
	}

	var promo struct {
		Promoted  bool   `json:"promoted"`
		Pending   bool   `json:"pending"`
		RequestID string `json:"request_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/promote",
		map[string]any{"from": "sandbox", "to": "staging"}, http.StatusCreated, &promo)
	if promo.Promoted || !promo.Pending || promo.RequestID == "" {
		t.Fatalf("staging policy should open a request: %+v", promo)
	}
}

func TestPromotionPolicyCanDisableForce(t *testing.T) {
	api := startEngine(t)
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows",
		map[string]string{"slug": "noforce", "name": "No Force"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions",
		map[string]any{"graph": flowtest.ConstGraph("approve")}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/deployments",
		map[string]any{"environment": "sandbox", "version": 1}, http.StatusCreated, nil)
	api.Request(t, http.MethodPut, "/v1/flows/"+created.FlowID+"/assertions", map[string]any{
		"cases": []map[string]any{{"name": "bad", "input": map[string]any{}, "expect": map[string]any{"decision": "decline"}}},
	}, http.StatusOK, nil)
	api.Request(t, http.MethodPut, "/v1/flows/"+created.FlowID+"/promotion-policy", map[string]any{
		"policy": map[string]any{
			"staging": map[string]any{"allow_force": false},
		},
	}, http.StatusOK, nil)

	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/promote",
		map[string]any{"from": "sandbox", "to": "staging", "force": true}, http.StatusConflict, nil)
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
