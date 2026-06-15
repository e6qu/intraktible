// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// Decision run status values.
const (
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// NodeResult is one node's evaluation output, captured in execution order.
type NodeResult struct {
	NodeID string          `json:"node_id"`
	Type   events.NodeType `json:"type"`
	Output json.RawMessage `json:"output,omitempty"`
}

// Run is the result of executing a flow against an input. It always carries the
// per-node trace; on failure Status is StatusFailed and Err/FailedNode are set
// (the failure is reported, never swallowed).
type Run struct {
	Status     string         `json:"status"`
	Output     map[string]any `json:"output,omitempty"`
	Results    []NodeResult   `json:"results"`
	FailedNode string         `json:"failed_node,omitempty"`
	Err        string         `json:"error,omitempty"`
}

// Execute runs a (validated, acyclic) flow graph against input and returns the
// ordered node trace plus the final output. It is pure and deterministic: the
// same graph and input always yield the same Run, which is the prerequisite for
// replay. Expression evaluation (expr-lang) is side-effect free.
//
// The MVP executes Input, Assignment, Rule, Split, and Output nodes; any other
// node type fails loudly until its engine lands.
func Execute(g events.Graph, input map[string]any) Run {
	nodes := make(map[string]events.Node, len(g.Nodes))
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	outgoing := make(map[string][]events.Edge)
	for _, e := range g.Edges {
		outgoing[e.From] = append(outgoing[e.From], e)
	}

	cur := inputNode(g)
	if cur == "" {
		return Run{Status: StatusFailed, Err: "decision-engine: graph has no input node"}
	}
	ctx := cloneContext(input)
	run := Run{Status: StatusCompleted}

	// The graph is acyclic (enforced at publish time); the step bound is a
	// defensive backstop, not a correctness mechanism.
	for step := 0; step <= len(g.Nodes); step++ {
		n, ok := nodes[cur]
		if !ok {
			return fail(run, cur, fmt.Sprintf("decision-engine: edge to unknown node %q", cur))
		}
		output, next, err := evalNode(n, ctx, outgoing[n.ID])
		run.Results = append(run.Results, NodeResult{NodeID: n.ID, Type: n.Type, Output: toJSON(output)})
		if err != nil {
			return fail(run, n.ID, err.Error())
		}
		if n.Type == events.NodeOutput {
			run.Output = asMap(output)
			return run
		}
		if next == "" {
			run.Output = ctx
			return run
		}
		cur = next
	}
	return fail(run, cur, "decision-engine: execution exceeded the node bound")
}

func evalNode(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	switch n.Type {
	case events.NodeInput:
		return map[string]any{}, firstEdge(edges), nil
	case events.NodeAssignment:
		return evalAssignment(n, ctx, edges)
	case events.NodeRule:
		return evalRule(n, ctx, edges)
	case events.NodeSplit:
		return evalSplit(n, ctx, edges)
	case events.NodeScorecard:
		return evalScorecard(n, ctx, edges)
	case events.NodeDecisionTable:
		return evalDecisionTable(n, ctx, edges)
	case events.NodeMatrix2D:
		return evalMatrix(n, ctx, edges)
	case events.NodeCode:
		return evalCode(n, ctx, edges)
	case events.NodeManualReview:
		return evalManualReview(n, ctx, edges)
	case events.NodeOutput:
		return evalOutput(n, ctx)
	default:
		return nil, "", fmt.Errorf("decision-engine: node %q has no execution engine for type %q", n.ID, n.Type)
	}
}

func evalAssignment(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg assignmentConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	applied := make(map[string]any, len(cfg.Assignments))
	for _, a := range cfg.Assignments {
		v, err := evalAny(a.Expr, ctx)
		if err != nil {
			return nil, "", fmt.Errorf("decision-engine: node %q assignment %q: %w", n.ID, a.Target, err)
		}
		ctx[a.Target] = v
		applied[a.Target] = v
	}
	return applied, firstEdge(edges), nil
}

func evalRule(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg ruleConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	applied := make(map[string]any)
	for i, r := range cfg.Rules {
		match, err := evalBool(r.When, ctx)
		if err != nil {
			return nil, "", fmt.Errorf("decision-engine: node %q rule %d condition: %w", n.ID, i, err)
		}
		if !match {
			continue
		}
		for _, a := range r.Then {
			v, err := evalAny(a.Expr, ctx)
			if err != nil {
				return nil, "", fmt.Errorf("decision-engine: node %q rule %d assignment %q: %w", n.ID, i, a.Target, err)
			}
			ctx[a.Target] = v
			applied[a.Target] = v
		}
	}
	return applied, firstEdge(edges), nil
}

func evalSplit(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg splitConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	match, err := evalBool(cfg.Condition, ctx)
	if err != nil {
		return nil, "", fmt.Errorf("decision-engine: node %q split condition: %w", n.ID, err)
	}
	branch := "no"
	if match {
		branch = "yes"
	}
	next := edgeForBranch(edges, branch)
	if next == "" {
		return nil, "", fmt.Errorf("decision-engine: node %q split has no %q branch edge", n.ID, branch)
	}
	return map[string]any{"branch": branch}, next, nil
}

func evalScorecard(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg scorecardConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	output := cfg.Output
	if output == "" {
		output = "score"
	}
	var score float64
	for i, f := range cfg.Factors {
		match, err := evalBool(f.When, ctx)
		if err != nil {
			return nil, "", fmt.Errorf("decision-engine: node %q factor %d: %w", n.ID, i, err)
		}
		if match {
			score += f.Weight
		}
	}
	ctx[output] = score
	return map[string]any{output: score}, firstEdge(edges), nil
}

func evalDecisionTable(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg decisionTableConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	applied := make(map[string]any)
	for i, row := range cfg.Rows {
		match, err := evalBool(row.When, ctx)
		if err != nil {
			return nil, "", fmt.Errorf("decision-engine: node %q row %d condition: %w", n.ID, i, err)
		}
		if !match {
			continue
		}
		for _, a := range row.Outputs {
			v, err := evalAny(a.Expr, ctx)
			if err != nil {
				return nil, "", fmt.Errorf("decision-engine: node %q row %d output %q: %w", n.ID, i, a.Target, err)
			}
			ctx[a.Target] = v
			applied[a.Target] = v
		}
		if cfg.Mode != "all" {
			break
		}
	}
	return applied, firstEdge(edges), nil
}

func evalMatrix(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg matrixConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	output := cfg.Output
	if output == "" {
		output = "result"
	}
	row, err := matchAxis(n, "row", cfg.Rows, ctx)
	if err != nil {
		return nil, "", err
	}
	col, err := matchAxis(n, "col", cfg.Cols, ctx)
	if err != nil {
		return nil, "", err
	}
	if row >= len(cfg.Cells) || col >= len(cfg.Cells[row]) {
		return nil, "", fmt.Errorf("decision-engine: node %q matrix cell [%d][%d] out of range", n.ID, row, col)
	}
	var v any
	if err := json.Unmarshal(cfg.Cells[row][col], &v); err != nil {
		return nil, "", fmt.Errorf("decision-engine: node %q matrix cell [%d][%d]: %w", n.ID, row, col, err)
	}
	ctx[output] = v
	return map[string]any{output: v}, firstEdge(edges), nil
}

// matchAxis returns the index of the first axis condition that holds, failing
// loudly when none match (a 2D matrix must cover the input).
func matchAxis(n events.Node, axis string, conds []axisCond, ctx map[string]any) (int, error) {
	for i, c := range conds {
		match, err := evalBool(c.When, ctx)
		if err != nil {
			return 0, fmt.Errorf("decision-engine: node %q %s %d: %w", n.ID, axis, i, err)
		}
		if match {
			return i, nil
		}
	}
	return 0, fmt.Errorf("decision-engine: node %q matrix has no matching %s", n.ID, axis)
}

// evalManualReview evaluates the case fields for an escalation. It is pass-through
// (the flow continues); the decide shell turns the recorded output into a
// ManualReviewRequested event.
func evalManualReview(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg manualReviewConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	company, err := evalString(cfg.CompanyName, ctx)
	if err != nil {
		return nil, "", fmt.Errorf("decision-engine: node %q company_name: %w", n.ID, err)
	}
	caseType, err := evalString(cfg.CaseType, ctx)
	if err != nil {
		return nil, "", fmt.Errorf("decision-engine: node %q case_type: %w", n.ID, err)
	}
	return map[string]any{
		"company_name": company,
		"case_type":    caseType,
		"sla_days":     cfg.SLADays,
	}, firstEdge(edges), nil
}

func evalString(code string, env map[string]any) (string, error) {
	v, err := evalAny(code, env)
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("expression %q did not evaluate to a string", code)
	}
	return s, nil
}

func evalOutput(n events.Node, ctx map[string]any) (any, string, error) {
	var cfg outputConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	if len(cfg.Fields) == 0 {
		return cloneContext(ctx), "", nil
	}
	resp := make(map[string]any, len(cfg.Fields))
	for _, f := range cfg.Fields {
		resp[f] = ctx[f]
	}
	return resp, "", nil
}

func evalAny(code string, env map[string]any) (any, error) {
	program, err := compile(code, env)
	if err != nil {
		return nil, err
	}
	return expr.Run(program, env)
}

func evalBool(code string, env map[string]any) (bool, error) {
	program, err := compile(code, env)
	if err != nil {
		return false, err
	}
	out, err := expr.Run(program, env)
	if err != nil {
		return false, err
	}
	b, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("condition %q did not evaluate to a boolean", code)
	}
	return b, nil
}

func compile(code string, env map[string]any) (*vm.Program, error) {
	if code == "" {
		return nil, fmt.Errorf("expression is empty")
	}
	return expr.Compile(code, expr.Env(env))
}

func inputNode(g events.Graph) string {
	for _, n := range g.Nodes {
		if n.Type == events.NodeInput {
			return n.ID
		}
	}
	return ""
}

func firstEdge(edges []events.Edge) string {
	if len(edges) == 0 {
		return ""
	}
	return edges[0].To
}

func edgeForBranch(edges []events.Edge, branch string) string {
	for _, e := range edges {
		if e.Branch == branch {
			return e.To
		}
	}
	return ""
}

func fail(run Run, nodeID, msg string) Run {
	run.Status = StatusFailed
	run.FailedNode = nodeID
	run.Err = msg
	return run
}

func cloneContext(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func toJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return b
}
