// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/policy"
	"github.com/e6qu/intraktible/platform/testutil"
)

// scorecardFlow publishes a monotone scorecard-style flow (score = income/1000,
// approve when score >= 50, else decline) plus the bound policy, and returns the
// flow id. The disposition is driven entirely by the income input, so a single-field
// change can flip a decline to an approve.
func scorecardFlow(t *testing.T, api *testutil.API, slug string) string {
	t.Helper()
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": slug, "name": "Scorecard"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "score", "type": "assignment", "config": map[string]any{
					"assignments": []map[string]any{{"target": "score", "expr": "income / 1000"}},
				}},
				{"id": "gate", "type": "split", "config": map[string]any{"condition": "score >= 50"}},
				{"id": "approve", "type": "assignment", "config": map[string]any{
					"assignments": []map[string]any{{"target": "decision", "expr": "'APPROVE'"}},
				}},
				{"id": "decline", "type": "assignment", "config": map[string]any{
					"assignments": []map[string]any{{"target": "decision", "expr": "'DECLINE'"}},
				}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"decision", "score"}}},
			},
			"edges": []map[string]any{
				{"from": "in", "to": "score"},
				{"from": "score", "to": "gate"},
				{"from": "gate", "to": "approve", "branch": "yes"},
				{"from": "gate", "to": "decline", "branch": "no"},
				{"from": "approve", "to": "out"},
				{"from": "decline", "to": "out"},
			},
		},
	}, http.StatusCreated, nil)

	var pol struct {
		PolicyID string `json:"policy_id"`
	}
	api.Request(t, http.MethodPost, "/v1/policies", map[string]any{"name": slug + "-pol", "flow_slug": slug}, http.StatusCreated, &pol)
	api.Request(t, http.MethodPost, "/v1/policies/"+pol.PolicyID+"/versions", map[string]any{
		"spec": map[string]any{
			"rules":   []map[string]any{{"when": "score >= 50", "disposition": "approve"}},
			"default": "decline",
		},
	}, http.StatusCreated, nil)
	return created.FlowID
}

// decideOnce decides one input and returns the decision id once the flow+policy
// projections have caught up and assigned the expected disposition.
func decideOnce(t *testing.T, api *testutil.API, slug string, data map[string]any, wantDisp string) string {
	t.Helper()
	var dec struct {
		DecisionID  string `json:"decision_id"`
		Status      string `json:"status"`
		Disposition string `json:"disposition"`
	}
	if !testutil.Eventually(t, func() bool {
		dec = struct {
			DecisionID  string `json:"decision_id"`
			Status      string `json:"status"`
			Disposition string `json:"disposition"`
		}{}
		api.Request(t, http.MethodPost, "/v1/flows/"+slug+"/production/decide",
			map[string]any{"data": data}, http.StatusOK, &dec)
		return dec.Status == "completed" && dec.Disposition == wantDisp
	}) {
		t.Fatalf("flow %q never decided %q for %v: %+v", slug, wantDisp, data, dec)
	}
	return dec.DecisionID
}

type nodeStat struct {
	NodeID string  `json:"node_id"`
	Type   string  `json:"type"`
	Count  int     `json:"count"`
	Pct    float64 `json:"pct"`
}

type nodeStatsResp struct {
	Total        int            `json:"total"`
	Dispositions map[string]int `json:"dispositions"`
	Nodes        []nodeStat     `json:"nodes"`
}

func TestNodeStatsAggregatesTrace(t *testing.T) {
	api := startEngine(t)
	flowID := scorecardFlow(t, api, "ns")

	// Two approves (income 80k) and one decline (income 10k): the approve branch is
	// hit twice, the decline branch once, and the input/score/gate nodes every time.
	decideOnce(t, api, "ns", map[string]any{"income": 80000.0}, string(policy.Approve))
	decideOnce(t, api, "ns", map[string]any{"income": 80000.0}, string(policy.Approve))
	decideOnce(t, api, "ns", map[string]any{"income": 10000.0}, string(policy.Decline))

	var stats nodeStatsResp
	if !testutil.Eventually(t, func() bool {
		stats = nodeStatsResp{}
		api.Request(t, http.MethodGet, "/v1/flows/"+flowID+"/node-stats", nil, http.StatusOK, &stats)
		return stats.Total == 3
	}) {
		t.Fatalf("node-stats never saw 3 completed decisions: %+v", stats)
	}

	byID := map[string]nodeStat{}
	for _, n := range stats.Nodes {
		byID[n.NodeID] = n
	}
	// Every published node must appear, including the never-traversed branch's nodes.
	for _, want := range []string{"in", "score", "gate", "approve", "decline", "out"} {
		if _, ok := byID[want]; !ok {
			t.Fatalf("node %q missing from stats: %+v", want, stats.Nodes)
		}
	}
	if got := byID["gate"].Count; got != 3 {
		t.Fatalf("gate hit count = %d, want 3", got)
	}
	if got := byID["approve"].Count; got != 2 {
		t.Fatalf("approve hit count = %d, want 2", got)
	}
	if got := byID["decline"].Count; got != 1 {
		t.Fatalf("decline hit count = %d, want 1", got)
	}
	if byID["in"].Pct != 1.0 {
		t.Fatalf("input node pct = %v, want 1.0", byID["in"].Pct)
	}
	if stats.Dispositions["approve"] != 2 || stats.Dispositions["decline"] != 1 {
		t.Fatalf("disposition tally = %+v, want approve:2 decline:1", stats.Dispositions)
	}
}

// zeroHitNodeFlow publishes a flow with a dead branch: the gate's condition is a
// constant `false`, so the "yes" branch (node "never") is never traversed.
func zeroHitNodeFlow(t *testing.T, api *testutil.API, slug string) string {
	t.Helper()
	var created struct {
		FlowID string `json:"flow_id"`
	}
	api.Request(t, http.MethodPost, "/v1/flows", map[string]any{"slug": slug, "name": "Dead"}, http.StatusCreated, &created)
	api.Request(t, http.MethodPost, "/v1/flows/"+created.FlowID+"/versions", map[string]any{
		"graph": map[string]any{
			"nodes": []map[string]any{
				{"id": "in", "type": "input"},
				{"id": "gate", "type": "split", "config": map[string]any{"condition": "false"}},
				{"id": "never", "type": "assignment", "config": map[string]any{
					"assignments": []map[string]any{{"target": "decision", "expr": "'NEVER'"}},
				}},
				{"id": "always", "type": "assignment", "config": map[string]any{
					"assignments": []map[string]any{{"target": "decision", "expr": "'ALWAYS'"}},
				}},
				{"id": "out", "type": "output", "config": map[string]any{"fields": []string{"decision"}}},
			},
			"edges": []map[string]any{
				{"from": "in", "to": "gate"},
				{"from": "gate", "to": "never", "branch": "yes"},
				{"from": "gate", "to": "always", "branch": "no"},
				{"from": "never", "to": "out"},
				{"from": "always", "to": "out"},
			},
		},
	}, http.StatusCreated, nil)
	return created.FlowID
}

func TestNodeStatsIncludesZeroHitNode(t *testing.T) {
	api := startEngine(t)
	flowID := zeroHitNodeFlow(t, api, "deadns")

	decideOnce(t, api, "deadns", map[string]any{}, "") // no policy bound → empty disposition

	var stats nodeStatsResp
	if !testutil.Eventually(t, func() bool {
		stats = nodeStatsResp{}
		api.Request(t, http.MethodGet, "/v1/flows/"+flowID+"/node-stats", nil, http.StatusOK, &stats)
		return stats.Total == 1
	}) {
		t.Fatalf("node-stats never saw the decision: %+v", stats)
	}
	byID := map[string]nodeStat{}
	for _, n := range stats.Nodes {
		byID[n.NodeID] = n
	}
	if n, ok := byID["never"]; !ok || n.Count != 0 || n.Pct != 0 {
		t.Fatalf("zero-hit node 'never' = %+v, want present with count 0", n)
	}
	if byID["always"].Count != 1 {
		t.Fatalf("'always' node count = %d, want 1", byID["always"].Count)
	}
}

type flipResp struct {
	Field       string  `json:"field"`
	From        float64 `json:"from"`
	To          float64 `json:"to"`
	Direction   string  `json:"direction"`
	Disposition string  `json:"disposition"`
}

type counterfactualResp struct {
	Disposition string     `json:"disposition"`
	Flips       []flipResp `json:"flips"`
	Searched    int        `json:"searched"`
}

func TestCounterfactualFindsFlip(t *testing.T) {
	api := startEngine(t)
	scorecardFlow(t, api, "cf")

	// A declined decision: income 10k → score 10 → below the 50 threshold.
	decID := decideOnce(t, api, "cf", map[string]any{"income": 10000.0}, string(policy.Decline))

	var cf counterfactualResp
	if !testutil.Eventually(t, func() bool {
		cf = counterfactualResp{}
		if api.RequestStatus(t, http.MethodPost, "/v1/decisions/"+decID+"/counterfactual", map[string]any{}, &cf) != http.StatusOK {
			return false
		}
		return len(cf.Flips) > 0
	}) {
		t.Fatalf("counterfactual found no flip: %+v", cf)
	}
	if cf.Disposition != string(policy.Decline) {
		t.Fatalf("counterfactual original disposition = %q, want decline", cf.Disposition)
	}
	f := cf.Flips[0]
	if f.Field != "income" {
		t.Fatalf("flip field = %q, want income", f.Field)
	}
	if f.Direction != "increase" {
		t.Fatalf("flip direction = %q, want increase", f.Direction)
	}
	if f.Disposition != string(policy.Approve) {
		t.Fatalf("flip disposition = %q, want approve", f.Disposition)
	}
	// score = income/1000 >= 50 ⇒ income >= 50000, so the flip lands at/above 50k.
	if f.To < 50000 {
		t.Fatalf("flip target income = %v, want >= 50000 to clear the threshold", f.To)
	}
	if cf.Searched <= 0 {
		t.Fatalf("counterfactual reported 0 evals")
	}
}

func TestCounterfactualEmptyForApproved(t *testing.T) {
	api := startEngine(t)
	scorecardFlow(t, api, "cfok")

	decID := decideOnce(t, api, "cfok", map[string]any{"income": 90000.0}, string(policy.Approve))

	var cf counterfactualResp
	if !testutil.Eventually(t, func() bool {
		cf = counterfactualResp{}
		return api.RequestStatus(t, http.MethodPost, "/v1/decisions/"+decID+"/counterfactual", map[string]any{}, &cf) == http.StatusOK &&
			cf.Disposition == string(policy.Approve)
	}) {
		t.Fatalf("counterfactual for approved decision: %+v", cf)
	}
	if len(cf.Flips) != 0 {
		t.Fatalf("approved decision should have no flips, got %+v", cf.Flips)
	}
	if cf.Searched != 0 {
		t.Fatalf("approved decision should search 0 evals, got %d", cf.Searched)
	}
}

type coverageBranchResp struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Branch string `json:"branch"`
	Hits   int    `json:"hits"`
}

type coverageResp struct {
	Runs         int                  `json:"runs"`
	Fields       []string             `json:"fields"`
	Nodes        []coverageNodeResp   `json:"nodes"`
	Branches     []coverageBranchResp `json:"branches"`
	Dispositions map[string]int       `json:"dispositions"`
	DeadNodes    []string             `json:"dead_nodes"`
	DeadBranches []coverageBranchResp `json:"dead_branches"`
}

type coverageNodeResp struct {
	NodeID string `json:"node_id"`
	Type   string `json:"type"`
	Hits   int    `json:"hits"`
}

func TestCoverageReportsDeadBranch(t *testing.T) {
	api := startEngine(t)
	flowID := zeroHitNodeFlow(t, api, "covdead")

	var cov coverageResp
	if !testutil.Eventually(t, func() bool {
		cov = coverageResp{}
		return api.RequestStatus(t, http.MethodPost, "/v1/flows/"+flowID+"/coverage",
			map[string]any{"runs": 50}, &cov) == http.StatusOK && cov.Runs == 50
	}) {
		t.Fatalf("coverage never ran: %+v", cov)
	}

	// The gate's condition is constant false, so the "yes" branch to 'never' is dead.
	foundDeadBranch := false
	for _, b := range cov.DeadBranches {
		if b.From == "gate" && b.To == "never" && b.Branch == "yes" {
			foundDeadBranch = true
		}
	}
	if !foundDeadBranch {
		t.Fatalf("expected dead branch gate->never (yes), got dead_branches=%+v branches=%+v", cov.DeadBranches, cov.Branches)
	}
	// 'never' is unreachable, so it is a dead node too.
	foundDeadNode := false
	for _, n := range cov.DeadNodes {
		if n == "never" {
			foundDeadNode = true
		}
	}
	if !foundDeadNode {
		t.Fatalf("expected dead node 'never', got %+v", cov.DeadNodes)
	}
	// The 'always' branch is taken on every run, so it must NOT be dead.
	for _, b := range cov.DeadBranches {
		if b.From == "gate" && b.To == "always" {
			t.Fatalf("gate->always wrongly reported dead: %+v", cov.DeadBranches)
		}
	}
}

func TestCoverageReproducible(t *testing.T) {
	api := startEngine(t)
	flowID := scorecardFlow(t, api, "covrepro")

	var a, b coverageResp
	if !testutil.Eventually(t, func() bool {
		a = coverageResp{}
		return api.RequestStatus(t, http.MethodPost, "/v1/flows/"+flowID+"/coverage",
			map[string]any{"runs": 100}, &a) == http.StatusOK && a.Runs == 100 && len(a.Fields) > 0
	}) {
		t.Fatalf("coverage never ran with discovered fields: %+v", a)
	}
	api.Request(t, http.MethodPost, "/v1/flows/"+flowID+"/coverage", map[string]any{"runs": 100}, http.StatusOK, &b)

	// Deterministic seed ⇒ identical disposition distribution across two calls.
	if a.Dispositions["approve"] != b.Dispositions["approve"] || a.Dispositions["decline"] != b.Dispositions["decline"] {
		t.Fatalf("coverage not reproducible: %+v vs %+v", a.Dispositions, b.Dispositions)
	}
	// income is the only numeric lever the graph references.
	hasIncome := false
	for _, f := range a.Fields {
		if f == "income" {
			hasIncome = true
		}
	}
	if !hasIncome {
		t.Fatalf("coverage did not discover 'income' field: %+v", a.Fields)
	}
}

func TestNodeStatsUnknownFlow(t *testing.T) {
	api := startEngine(t)
	if code := api.RequestStatus(t, http.MethodGet, "/v1/flows/nope/node-stats", nil, nil); code != http.StatusNotFound {
		t.Fatalf("node-stats unknown flow status = %d, want 404", code)
	}
	if code := api.RequestStatus(t, http.MethodPost, "/v1/flows/nope/coverage", map[string]any{}, nil); code != http.StatusNotFound {
		t.Fatalf("coverage unknown flow status = %d, want 404", code)
	}
	if code := api.RequestStatus(t, http.MethodPost, "/v1/decisions/nope/counterfactual", map[string]any{}, nil); code != http.StatusNotFound {
		t.Fatalf("counterfactual unknown decision status = %d, want 404", code)
	}
}
