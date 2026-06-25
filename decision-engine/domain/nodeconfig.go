// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// assign sets a context field to the result of an expression.
type assign struct {
	Target string `json:"target"`
	Expr   string `json:"expr"`
}

// assignmentConfig is the config of an Assignment node.
type assignmentConfig struct {
	Assignments []assign `json:"assignments"`
}

// ruleConfig is the config of a Rule node: ordered when/then clauses.
type ruleConfig struct {
	Rules []struct {
		When string   `json:"when"`
		Then []assign `json:"then"`
	} `json:"rules"`
}

// splitConfig is the config of a Split node: a boolean condition selecting the
// "yes"/"no" branch edge.
type splitConfig struct {
	Condition string `json:"condition"`
}

// outputConfig is the config of an Output node. Empty Fields returns the whole
// context; otherwise only the named fields form the response.
type outputConfig struct {
	Fields []string `json:"fields"`
}

// manualReviewConfig is the config of a manual_review node: the case fields to
// open. CompanyName and CaseType are expressions evaluated against the context
// (use quotes for a literal, e.g. "'aml'"); SLADays is a literal.
//
// Suspend makes the node a durable human task: instead of passing through (the
// default, backward-compatible behaviour), the decision PAUSES here and resumes
// only when a reviewer acts. OutputKey is where the reviewer's outcome is injected
// into the record on resume (default "review"), so downstream nodes can branch on it.
type manualReviewConfig struct {
	CompanyName string `json:"company_name"`
	CaseType    string `json:"case_type"`
	SLADays     int    `json:"sla_days"`
	Suspend     bool   `json:"suspend"`
	OutputKey   string `json:"output_key"`
}

// connectConfig is the config of a Connect node: it names a Context Layer
// connector to call and the key its response is injected under (read downstream
// as connect.<output>).
type connectConfig struct {
	Connector string `json:"connector"`
	Output    string `json:"output"`
}

// predictConfig is the config of a Predict node: it names a registered model to
// evaluate and the key its prediction is injected under (read downstream as
// predict.<output>, e.g. predict.risk.probability).
type predictConfig struct {
	Model  string `json:"model"`
	Output string `json:"output"`
}

// aiConfig is the config of an AI node: it names an Agent Manager agent to run and
// the key its output is injected under (read downstream as ai.<output>). Prompt is
// the literal prompt; when empty the node sends the current input as the prompt.
type aiConfig struct {
	Agent  string `json:"agent"`
	Output string `json:"output"`
	Prompt string `json:"prompt,omitempty"`
}

// codeConfig is the config of a Code node: a Starlark script with the decision
// context predeclared as the `data` dict; its top-level assignments merge back
// into the context.
type codeConfig struct {
	Code string `json:"code"`
}

// scorecardConfig is the config of a Scorecard node: a score is the sum of the
// weights of the factors whose condition holds, written to Output (default
// "score").
type scorecardConfig struct {
	Output  string `json:"output"`
	Factors []struct {
		When   string  `json:"when"`
		Weight float64 `json:"weight"`
	} `json:"factors"`
}

// reasonConfig is the config of a Reason node: an ordered list of reason codes,
// each appended to the reserved "reason_codes" list in the context when its When
// condition holds. Code and Description are literal, human-readable text — the
// adverse-action explainability (ECOA/Reg B, insurance) raw material; When is an
// expression evaluated against the context.
type reasonConfig struct {
	Reasons []struct {
		When        string `json:"when"`
		Code        string `json:"code"`
		Description string `json:"description"`
	} `json:"reasons"`
}

// decisionTableConfig is the config of a Decision Table node: ordered rows, each
// a condition with its output assignments, resolved under a DMN-style hit policy.
//
// Hit (default "first"):
//   - first      — the first matching row wins.
//   - unique     — exactly one row may match; >1 is a conflict (error).
//   - any        — multiple rows may match but must all produce identical outputs.
//   - rule_order — every matching row's outputs, collected per target as a list in
//     rule order.
//   - collect    — like rule_order, but each target may be reduced by Aggregate.
//
// Aggregate (collect only): sum | min | max | count (empty = the list itself).
//
// Mode is the deprecated predecessor of Hit: "all" applies every matching row in
// order (last write wins per target); it is honoured when Hit is unset.
type decisionTableConfig struct {
	Hit       string        `json:"hit"`
	Aggregate string        `json:"aggregate"`
	Mode      string        `json:"mode"`
	Rows      []decisionRow `json:"rows"`
}

// decisionRow is one row of a decision table: a condition and its output assignments.
type decisionRow struct {
	When    string   `json:"when"`
	Outputs []assign `json:"outputs"`
}

// axisCond is one bucket of a matrix axis, selected when its condition holds.
type axisCond struct {
	When string `json:"when"`
}

// matrixConfig is the config of a 2D Matrix node: the first matching Rows
// condition and first matching Cols condition select Cells[row][col], a literal
// value written to Output (default "result").
type matrixConfig struct {
	Output string              `json:"output"`
	Rows   []axisCond          `json:"rows"`
	Cols   []axisCond          `json:"cols"`
	Cells  [][]json.RawMessage `json:"cells"`
}

// decodeConfig strictly decodes a node's Config into v (unknown fields rejected
// — fail loudly on a misconfigured node). An empty Config decodes to the zero
// value, which each evaluator treats as "no-op / defaults".
func decodeConfig(n events.Node, v any) error {
	if len(n.Config) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(n.Config))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decision-engine: node %q config: %w", n.ID, err)
	}
	return nil
}
