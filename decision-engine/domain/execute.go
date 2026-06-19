// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

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
	case events.NodeConnect:
		return evalConnect(n, ctx, edges)
	case events.NodeAI:
		return evalAI(n, ctx, edges)
	case events.NodePredict:
		return evalPredict(n, ctx, edges)
	case events.NodeManualReview:
		return evalManualReview(n, ctx, edges)
	case events.NodeReason:
		return evalReason(n, ctx, edges)
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

// reasonCodesField is the reserved context/output key that accumulates structured
// adverse-action reason codes. The Output node always surfaces it.
const reasonCodesField = "reason_codes"

// evalReason appends a {code, description} entry for every reason whose condition
// holds to the reserved reason_codes list, accumulating across the flow.
func evalReason(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg reasonConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	codes := existingReasonCodes(ctx)
	added := make([]any, 0, len(cfg.Reasons))
	for i, r := range cfg.Reasons {
		match, err := evalBool(r.When, ctx)
		if err != nil {
			return nil, "", fmt.Errorf("decision-engine: node %q reason %d condition: %w", n.ID, i, err)
		}
		if !match {
			continue
		}
		code := map[string]any{"code": r.Code, "description": r.Description}
		codes = append(codes, code)
		added = append(added, code)
	}
	ctx[reasonCodesField] = codes
	return map[string]any{reasonCodesField: added}, firstEdge(edges), nil
}

// existingReasonCodes returns a copy of the accumulated reason_codes list (empty
// when absent or the wrong shape) so Reason nodes append without aliasing ctx.
func existingReasonCodes(ctx map[string]any) []any {
	if v, ok := ctx[reasonCodesField].([]any); ok {
		return append([]any{}, v...)
	}
	return []any{}
}

// DMN-style hit policies for the decision table.
const (
	hitFirst     = "first"
	hitUnique    = "unique"
	hitAny       = "any"
	hitRuleOrder = "rule_order"
	hitCollect   = "collect"
)

func evalDecisionTable(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg decisionTableConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	hit := strings.ToLower(strings.TrimSpace(cfg.Hit))
	if hit == "" {
		// Back-compat with the predecessor "mode" field: "all" applied every
		// matching row in order (last write wins per target); anything else was FIRST.
		if strings.EqualFold(strings.TrimSpace(cfg.Mode), "all") {
			return evalTableApplyAll(n, cfg, ctx, edges)
		}
		hit = hitFirst
	}

	// Evaluate matching rows against the input context (rules are independent — a
	// row's outputs are not visible to other rows). FIRST stops at the first match.
	type rowOutput struct {
		idx     int
		outputs map[string]any
	}
	var matched []rowOutput
	for i, row := range cfg.Rows {
		// Conditions read the input context; outputs write to a per-row scratch env
		// (a clone) so a later output in the SAME row can read an earlier one without
		// leaking across rows.
		out, ok, err := evalTableRow(n.ID, i, row, ctx, cloneEnv(ctx))
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		matched = append(matched, rowOutput{i, out})
		if hit == hitFirst {
			break
		}
	}

	applied := make(map[string]any)
	switch hit {
	case hitFirst, hitUnique, hitAny:
		if hit == hitUnique && len(matched) > 1 {
			return nil, "", fmt.Errorf("decision-engine: node %q UNIQUE hit policy: %d rows matched", n.ID, len(matched))
		}
		if hit == hitAny {
			for _, m := range matched[1:] {
				if !reflect.DeepEqual(m.outputs, matched[0].outputs) {
					return nil, "", fmt.Errorf("decision-engine: node %q ANY hit policy: matching rows produce conflicting outputs", n.ID)
				}
			}
		}
		if len(matched) > 0 {
			for k, v := range matched[0].outputs {
				ctx[k] = v
				applied[k] = v
			}
		}
	case hitRuleOrder, hitCollect:
		agg := ""
		if hit == hitCollect {
			agg = strings.ToLower(strings.TrimSpace(cfg.Aggregate))
		}
		// Collect each target's values across matching rows, in rule (then output-
		// declaration) order, then reduce by the aggregator.
		lists := map[string][]any{}
		var order []string
		for _, m := range matched {
			for _, a := range cfg.Rows[m.idx].Outputs {
				if _, seen := lists[a.Target]; !seen {
					order = append(order, a.Target)
				}
				lists[a.Target] = append(lists[a.Target], m.outputs[a.Target])
			}
		}
		for _, target := range order {
			v, err := aggregateValues(agg, lists[target])
			if err != nil {
				return nil, "", fmt.Errorf("decision-engine: node %q COLLECT %q of %q: %w", n.ID, agg, target, err)
			}
			ctx[target] = v
			applied[target] = v
		}
	default:
		return nil, "", fmt.Errorf("decision-engine: node %q unknown hit policy %q", n.ID, hit)
	}
	return applied, firstEdge(edges), nil
}

// evalTableApplyAll is the deprecated mode:"all" path: apply every matching row's
// outputs in order, last write winning per target (rows see earlier rows' writes,
// so condition and outputs share the live context).
func evalTableApplyAll(n events.Node, cfg decisionTableConfig, ctx map[string]any, edges []events.Edge) (any, string, error) {
	applied := make(map[string]any)
	for i, row := range cfg.Rows {
		out, ok, err := evalTableRow(n.ID, i, row, ctx, ctx)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		for k, v := range out {
			applied[k] = v
		}
	}
	return applied, firstEdge(edges), nil
}

// evalTableRow evaluates one row: its condition against condEnv and, on a match, its
// outputs against outEnv (each output visible to later outputs in the same row, since
// they are written back into outEnv). It returns the row's output map and whether it
// matched. Callers pass outEnv == condEnv to apply outputs to the live context, or a
// clone to keep rows independent.
func evalTableRow(nodeID string, i int, row decisionRow, condEnv, outEnv map[string]any) (map[string]any, bool, error) {
	ok, err := evalBool(row.When, condEnv)
	if err != nil {
		return nil, false, fmt.Errorf("decision-engine: node %q row %d condition: %w", nodeID, i, err)
	}
	if !ok {
		return nil, false, nil
	}
	out := make(map[string]any, len(row.Outputs))
	for _, a := range row.Outputs {
		v, err := evalAny(a.Expr, outEnv)
		if err != nil {
			return nil, false, fmt.Errorf("decision-engine: node %q row %d output %q: %w", nodeID, i, a.Target, err)
		}
		outEnv[a.Target] = v
		out[a.Target] = v
	}
	return out, true, nil
}

// cloneEnv shallow-copies an evaluation context so per-row output writes don't
// mutate the shared context (which would leak one row's outputs into another's).
func cloneEnv(ctx map[string]any) map[string]any {
	c := make(map[string]any, len(ctx)+2)
	for k, v := range ctx {
		c[k] = v
	}
	return c
}

// aggregateValues reduces a COLLECT target's values by aggregator: "" or "list"
// keeps the list, "count" yields the length, and sum/min/max reduce numerically.
func aggregateValues(agg string, vals []any) (any, error) {
	switch agg {
	case "", "list":
		return vals, nil
	case "count":
		return len(vals), nil
	case "sum", "min", "max":
		nums := make([]float64, len(vals))
		for i, v := range vals {
			f, ok := toFloat(v)
			if !ok {
				return nil, fmt.Errorf("non-numeric value %v", v)
			}
			nums[i] = f
		}
		if len(nums) == 0 {
			if agg == "sum" {
				return float64(0), nil
			}
			return nil, nil
		}
		acc := nums[0]
		for _, f := range nums[1:] {
			switch agg {
			case "sum":
				acc += f
			case "min":
				if f < acc {
					acc = f
				}
			case "max":
				if f > acc {
					acc = f
				}
			}
		}
		return acc, nil
	default:
		return nil, fmt.Errorf("unknown aggregator %q", agg)
	}
}

// toFloat coerces the numeric types expr-lang yields (and JSON's float64) to float64.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	default:
		return 0, false
	}
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

// ConnectSpec names a Connect node's connector + the key its response lands under.
type ConnectSpec struct {
	NodeID    string
	Connector string
	Output    string
}

// ConnectSpecs extracts the Connect nodes from a graph so the shell can pre-resolve
// their connector calls before execution (keeping Execute pure). It fails loudly on
// a Connect node missing its connector or output.
func ConnectSpecs(g events.Graph) ([]ConnectSpec, error) {
	var out []ConnectSpec
	for _, n := range g.Nodes {
		if n.Type != events.NodeConnect {
			continue
		}
		var cfg connectConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return nil, err
		}
		if cfg.Connector == "" || cfg.Output == "" {
			return nil, fmt.Errorf("decision-engine: connect node %q needs a connector and an output", n.ID)
		}
		out = append(out, ConnectSpec{NodeID: n.ID, Connector: cfg.Connector, Output: cfg.Output})
	}
	return out, nil
}

// evalConnect is pass-through: the shell pre-resolves the connector call and
// injects the response under connect.<output>; the node echoes that into its
// recorded output and fails loudly if it was not resolved.
func evalConnect(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg connectConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	return preResolved(n, ctx, edges, "connect", cfg.Output, "connector")
}

// AISpec names an AI node's agent + the key its output lands under (and the literal
// prompt, empty meaning "send the current input").
type AISpec struct {
	NodeID string
	Agent  string
	Output string
	Prompt string
}

// AISpecs extracts the AI nodes from a graph so the shell can pre-resolve their
// agent runs before execution (keeping Execute pure). It fails loudly on an AI
// node missing its agent or output.
func AISpecs(g events.Graph) ([]AISpec, error) {
	var out []AISpec
	for _, n := range g.Nodes {
		if n.Type != events.NodeAI {
			continue
		}
		var cfg aiConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return nil, err
		}
		if cfg.Agent == "" || cfg.Output == "" {
			return nil, fmt.Errorf("decision-engine: ai node %q needs an agent and an output", n.ID)
		}
		out = append(out, AISpec{NodeID: n.ID, Agent: cfg.Agent, Output: cfg.Output, Prompt: cfg.Prompt})
	}
	return out, nil
}

// evalAI is pass-through: the shell pre-resolves the agent run and injects the
// output under ai.<output>; the node echoes that into its recorded output.
func evalAI(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg aiConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	return preResolved(n, ctx, edges, "ai", cfg.Output, "agent")
}

// PredictSpec names a Predict node's model + the key its prediction lands under.
type PredictSpec struct {
	NodeID string
	Model  string
	Output string
}

// PredictSpecs extracts the Predict nodes from a graph so the shell can pre-resolve
// their model evaluations before execution (keeping Execute pure). It fails loudly
// on a Predict node missing its model or output.
func PredictSpecs(g events.Graph) ([]PredictSpec, error) {
	var out []PredictSpec
	for _, n := range g.Nodes {
		if n.Type != events.NodePredict {
			continue
		}
		var cfg predictConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return nil, err
		}
		if cfg.Model == "" || cfg.Output == "" {
			return nil, fmt.Errorf("decision-engine: predict node %q needs a model and an output", n.ID)
		}
		out = append(out, PredictSpec{NodeID: n.ID, Model: cfg.Model, Output: cfg.Output})
	}
	return out, nil
}

// evalPredict is pass-through: the shell pre-resolves the model evaluation and
// injects the prediction under predict.<output>; the node echoes that into its
// recorded output.
func evalPredict(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg predictConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	return preResolved(n, ctx, edges, "predict", cfg.Output, "model")
}

// preResolved echoes a shell-injected value at ctx[bucket][output] as the node's
// output, failing loudly when the bucket or key is absent (no provider wired).
func preResolved(n events.Node, ctx map[string]any, edges []events.Edge, bucket, output, kind string) (any, string, error) {
	b, ok := ctx[bucket].(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("decision-engine: %s node %q has no resolved data (no %s provider configured?)", bucket, n.ID, kind)
	}
	v, ok := b[output]
	if !ok {
		return nil, "", fmt.Errorf("decision-engine: %s node %q output %q was not resolved", bucket, n.ID, output)
	}
	return map[string]any{output: v}, firstEdge(edges), nil
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
	// reason_codes is a reserved compliance field — always surface it so an
	// adverse-action explanation is never dropped by output field selection.
	if rc, ok := ctx[reasonCodesField]; ok {
		if _, selected := resp[reasonCodesField]; !selected {
			resp[reasonCodesField] = rc
		}
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
