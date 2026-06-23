// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
	"github.com/e6qu/intraktible/decision-engine/events"
)

func cfgNode(id string, t events.NodeType, config string) events.Node {
	n := events.Node{ID: id, Type: t}
	if config != "" {
		n.Config = json.RawMessage(config)
	}
	return n
}

// linear builds an input -> mid -> out flow.
func linear(mid, out events.Node) events.Graph {
	return events.Graph{
		Nodes: []events.Node{cfgNode("in", events.NodeInput, ""), mid, out},
		Edges: []events.Edge{{From: "in", To: mid.ID}, {From: mid.ID, To: out.ID}},
	}
}

func outputJSON(t *testing.T, run domain.Run) string {
	t.Helper()
	if run.Status != domain.StatusCompleted {
		t.Fatalf("status=%s err=%s", run.Status, run.Err)
	}
	b, err := json.Marshal(run.Output)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExecuteLinear(t *testing.T) {
	const (
		ruleCfg   = `{"rules":[{"when":"fico < 600","then":[{"target":"tier","expr":"'low'"}]}]}`
		twoCfg    = `{"assignments":[{"target":"x","expr":"a + b"},{"target":"y","expr":"x * 2"}]}`
		scoreCfg  = `{"output":"score","factors":[{"when":"fico < 700","weight":10},{"when":"defaults > 0","weight":25}]}`
		tableCfg  = `{"rows":[{"when":"score >= 80","outputs":[{"target":"grade","expr":"'A'"}]},{"when":"score >= 60","outputs":[{"target":"grade","expr":"'B'"}]}]}`
		matrixCfg = `{"output":"tier","rows":[{"when":"income >= 50000"},{"when":"true"}],"cols":[{"when":"score >= 700"},{"when":"true"}],"cells":[["PRIME","NEAR"],["SUB","DECLINE"]]}`
		codeArith = `{"code":"score = data[\"fico\"] + 10"}`
		codeIf    = `{"code":"if data[\"amount\"] > 1000:\n    decision = \"APPROVE\"\nelse:\n    decision = \"DECLINE\""}`
		codeFunc  = `{"code":"def f():\n    return 7\nresult = f()"}`
	)
	rule := cfgNode("m", events.NodeRule, ruleCfg)
	gradeOut := cfgNode("out", events.NodeOutput, `{"fields":["grade"]}`)
	cases := []struct {
		name  string
		graph events.Graph
		input map[string]any
		want  string
	}{
		{
			"assignment",
			linear(cfgNode("m", events.NodeAssignment, `{"assignments":[{"target":"score","expr":"fico + 10"}]}`), cfgNode("out", events.NodeOutput, `{"fields":["score"]}`)),
			map[string]any{"fico": 700}, `{"score":710}`,
		},
		{"rule fires", linear(rule, cfgNode("out", events.NodeOutput, `{"fields":["tier"]}`)), map[string]any{"fico": 550}, `{"tier":"low"}`},
		{"rule skips", linear(rule, cfgNode("out", events.NodeOutput, `{"fields":["tier"]}`)), map[string]any{"fico": 800}, `{"tier":null}`},
		{"chained deterministically", linear(cfgNode("m", events.NodeAssignment, twoCfg), cfgNode("out", events.NodeOutput, "")), map[string]any{"a": 3, "b": 4}, `{"a":3,"b":4,"x":7,"y":14}`},
		{"scorecard", linear(cfgNode("m", events.NodeScorecard, scoreCfg), cfgNode("out", events.NodeOutput, `{"fields":["score"]}`)), map[string]any{"fico": 650, "defaults": 1}, `{"score":35}`},
		{"decision table first row", linear(cfgNode("m", events.NodeDecisionTable, tableCfg), gradeOut), map[string]any{"score": 85}, `{"grade":"A"}`},
		{"decision table second row", linear(cfgNode("m", events.NodeDecisionTable, tableCfg), gradeOut), map[string]any{"score": 70}, `{"grade":"B"}`},
		{"2d matrix", linear(cfgNode("m", events.NodeMatrix2D, matrixCfg), cfgNode("out", events.NodeOutput, `{"fields":["tier"]}`)), map[string]any{"income": 60000, "score": 720}, `{"tier":"PRIME"}`},
		{"code arithmetic", linear(cfgNode("m", events.NodeCode, codeArith), cfgNode("out", events.NodeOutput, `{"fields":["score"]}`)), map[string]any{"fico": 700}, `{"score":710}`},
		{"code top-level if", linear(cfgNode("m", events.NodeCode, codeIf), cfgNode("out", events.NodeOutput, `{"fields":["decision"]}`)), map[string]any{"amount": 5000}, `{"decision":"APPROVE"}`},
		{"code skips functions", linear(cfgNode("m", events.NodeCode, codeFunc), cfgNode("out", events.NodeOutput, "")), map[string]any{}, `{"result":7}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := outputJSON(t, domain.Execute(c.graph, c.input))
			if got != c.want {
				t.Fatalf("output=%s, want %s", got, c.want)
			}
			// Same inputs must reproduce the same output (replay prerequisite).
			if again := outputJSON(t, domain.Execute(c.graph, c.input)); again != got {
				t.Fatalf("non-deterministic: %s != %s", again, got)
			}
		})
	}
}

// TestDecisionTableHitPolicies covers the DMN hit-policy set over a table whose
// two rows both match when score >= 80 (band/pts differ between them).
func TestDecisionTableHitPolicies(t *testing.T) {
	rows := `"rows":[{"when":"score >= 50","outputs":[{"target":"band","expr":"'mid'"},{"target":"pts","expr":"1"}]},` +
		`{"when":"score >= 80","outputs":[{"target":"band","expr":"'high'"},{"target":"pts","expr":"2"}]}]`
	tbl := func(prefix string) events.Node { return cfgNode("m", events.NodeDecisionTable, "{"+prefix+rows+"}") }
	out := cfgNode("out", events.NodeOutput, `{"fields":["band","pts"]}`)

	ok := []struct {
		name, cfg string
		input     map[string]any
		want      string
	}{
		{"first picks the first match", `"hit":"first",`, map[string]any{"score": 85}, `{"band":"mid","pts":1}`},
		{"unique single match", `"hit":"unique",`, map[string]any{"score": 60}, `{"band":"mid","pts":1}`},
		{"rule_order collects per target in order", `"hit":"rule_order",`, map[string]any{"score": 85}, `{"band":["mid","high"],"pts":[1,2]}`},
		{"collect list", `"hit":"collect",`, map[string]any{"score": 85}, `{"band":["mid","high"],"pts":[1,2]}`},
		{"collect count", `"hit":"collect","aggregate":"count",`, map[string]any{"score": 85}, `{"band":2,"pts":2}`},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			if got := outputJSON(t, domain.Execute(linear(tbl(c.cfg), out), c.input)); got != c.want {
				t.Fatalf("output=%s, want %s", got, c.want)
			}
		})
	}

	// COLLECT sum reduces a numeric output across matching rows (1 + 2 = 3).
	sumTbl := cfgNode("m", events.NodeDecisionTable,
		`{"hit":"collect","aggregate":"sum","rows":[{"when":"score >= 50","outputs":[{"target":"pts","expr":"1"}]},{"when":"score >= 80","outputs":[{"target":"pts","expr":"2"}]}]}`)
	if got := outputJSON(t, domain.Execute(linear(sumTbl, cfgNode("out", events.NodeOutput, `{"fields":["pts"]}`)), map[string]any{"score": 85})); got != `{"pts":3}` {
		t.Fatalf("collect sum = %s, want {\"pts\":3}", got)
	}

	// ANY succeeds when every matching row agrees on its outputs.
	anyTbl := cfgNode("m", events.NodeDecisionTable,
		`{"hit":"any","rows":[{"when":"score >= 50","outputs":[{"target":"band","expr":"'ok'"}]},{"when":"score >= 80","outputs":[{"target":"band","expr":"'ok'"}]}]}`)
	if got := outputJSON(t, domain.Execute(linear(anyTbl, cfgNode("out", events.NodeOutput, `{"fields":["band"]}`)), map[string]any{"score": 85})); got != `{"band":"ok"}` {
		t.Fatalf("any agree = %s, want {\"band\":\"ok\"}", got)
	}

	// ANY with ZERO matching rows must NOT panic (matched[1:] on an empty slice) —
	// the pure core is contractually panic-free. It completes, applying no outputs.
	noMatch := domain.Execute(linear(anyTbl, cfgNode("out", events.NodeOutput, `{"fields":["band"]}`)), map[string]any{"score": 10})
	if noMatch.Status != domain.StatusCompleted {
		t.Fatalf("ANY zero-match: status=%s err=%q, want completed (no panic)", noMatch.Status, noMatch.Err)
	}

	// Conflict policies fail loudly when more than one row matches with differing output.
	bad := []struct {
		name, cfg, wantErr string
	}{
		{"unique conflict fails", `"hit":"unique",`, "UNIQUE"},
		{"any conflict fails", `"hit":"any",`, "ANY"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			run := domain.Execute(linear(tbl(c.cfg), out), map[string]any{"score": 85})
			if run.Status != domain.StatusFailed {
				t.Fatalf("status=%s, want failed", run.Status)
			}
			if !strings.Contains(run.Err, c.wantErr) {
				t.Fatalf("err=%q, want containing %q", run.Err, c.wantErr)
			}
		})
	}
}

func splitGraph() events.Graph {
	return events.Graph{
		Nodes: []events.Node{
			cfgNode("in", events.NodeInput, ""),
			cfgNode("s", events.NodeSplit, `{"condition":"amount > 1000"}`),
			cfgNode("yes", events.NodeAssignment, `{"assignments":[{"target":"decision","expr":"'APPROVE'"}]}`),
			cfgNode("no", events.NodeAssignment, `{"assignments":[{"target":"decision","expr":"'DECLINE'"}]}`),
			cfgNode("out", events.NodeOutput, `{"fields":["decision"]}`),
		},
		Edges: []events.Edge{
			{From: "in", To: "s"},
			{From: "s", To: "yes", Branch: "yes"},
			{From: "s", To: "no", Branch: "no"},
			{From: "yes", To: "out"},
			{From: "no", To: "out"},
		},
	}
}

func TestExecuteSplit(t *testing.T) {
	g := splitGraph()
	approve := domain.Execute(g, map[string]any{"amount": 5000})
	if got := outputJSON(t, approve); got != `{"decision":"APPROVE"}` {
		t.Fatalf("yes branch: %s", got)
	}
	for _, r := range approve.Results {
		if r.NodeID == "no" {
			t.Fatal("the not-taken branch must not be evaluated")
		}
	}
	if got := outputJSON(t, domain.Execute(g, map[string]any{"amount": 500})); got != `{"decision":"DECLINE"}` {
		t.Fatalf("no branch: %s", got)
	}
}

func TestExecuteManualReview(t *testing.T) {
	g := linear(
		cfgNode("mr", events.NodeManualReview, `{"company_name":"company","case_type":"'aml'","sla_days":7}`),
		cfgNode("out", events.NodeOutput, ""),
	)
	run := domain.Execute(g, map[string]any{"company": "Acme"})
	if run.Status != domain.StatusCompleted {
		t.Fatalf("status=%s err=%s", run.Status, run.Err)
	}
	// manual_review flows through, but it also contributes a MANUAL_REVIEW reason
	// code so an escalated decision is explainable without an explicit Reason node;
	// the Output node always surfaces the reserved reason_codes field.
	if got := outputJSON(t, run); got != `{"company":"Acme","reason_codes":[{"code":"MANUAL_REVIEW","description":"Escalated to manual review"}]}` {
		t.Fatalf("final output=%s", got)
	}
	// The node records the escalation fields the decide shell emits, plus the code.
	var mrOut string
	for _, r := range run.Results {
		if r.NodeID == "mr" {
			mrOut = string(r.Output)
		}
	}
	if mrOut != `{"case_type":"aml","company_name":"Acme","reason_codes":[{"code":"MANUAL_REVIEW","description":"Escalated to manual review"}],"sla_days":7}` {
		t.Fatalf("manual_review output=%s", mrOut)
	}
}

func TestExecuteReasonCodes(t *testing.T) {
	g := linear(
		cfgNode("r", events.NodeReason, `{"reasons":[`+
			`{"when":"fico < 600","code":"R01","description":"Insufficient credit score"},`+
			`{"when":"income < 30000","code":"R02","description":"Insufficient income"}]}`),
		cfgNode("out", events.NodeOutput, `{"fields":["decision"]}`),
	)
	run := domain.Execute(g, map[string]any{"fico": 500.0, "income": 50000.0, "decision": "DECLINE"})
	if run.Status != domain.StatusCompleted {
		t.Fatalf("status=%s err=%s", run.Status, run.Err)
	}
	// Only the matching condition (fico<600) emits a code, and reason_codes is
	// surfaced even though the output node selected only "decision".
	rc, ok := run.Output["reason_codes"].([]any)
	if !ok || len(rc) != 1 {
		t.Fatalf("want 1 surfaced reason code, got %#v", run.Output["reason_codes"])
	}
	first, _ := rc[0].(map[string]any)
	if first["code"] != "R01" || first["description"] != "Insufficient credit score" {
		t.Fatalf("wrong reason code: %#v", first)
	}
	if run.Output["decision"] != "DECLINE" {
		t.Fatalf("selected field lost: %v", run.Output["decision"])
	}
}

func TestExecuteFailsLoudly(t *testing.T) {
	cases := []struct {
		name       string
		graph      events.Graph
		failedNode string
	}{
		{
			"bad expression",
			linear(cfgNode("a", events.NodeAssignment, `{"assignments":[{"target":"x","expr":"fico +"}]}`), cfgNode("out", events.NodeOutput, "")),
			"a",
		},
		{
			"unsupported node type",
			linear(cfgNode("ai", events.NodeAI, ""), cfgNode("out", events.NodeOutput, "")),
			"ai",
		},
		{
			"matrix with no covering bucket",
			linear(cfgNode("m", events.NodeMatrix2D, `{"rows":[{"when":"false"}],"cols":[{"when":"true"}],"cells":[["X"]]}`), cfgNode("out", events.NodeOutput, "")),
			"m",
		},
		{
			"code syntax error",
			linear(cfgNode("m", events.NodeCode, `{"code":"x = "}`), cfgNode("out", events.NodeOutput, "")),
			"m",
		},
		{
			"code exceeds the step bound",
			linear(cfgNode("m", events.NodeCode, `{"code":"for i in range(100000000):\n    pass"}`), cfgNode("out", events.NodeOutput, "")),
			"m",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := domain.Execute(c.graph, map[string]any{"fico": 1})
			if run.Status != domain.StatusFailed || run.FailedNode != c.failedNode || run.Err == "" {
				t.Fatalf("expected loud failure at %q, got %+v", c.failedNode, run)
			}
		})
	}
}
