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

// decisionTableConfig is the config of a Decision Table node: ordered rows, each
// a condition with its output assignments. Mode "first" (default) applies the
// first matching row; "all" applies every matching row in order.
type decisionTableConfig struct {
	Mode string `json:"mode"`
	Rows []struct {
		When    string   `json:"when"`
		Outputs []assign `json:"outputs"`
	} `json:"rows"`
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
