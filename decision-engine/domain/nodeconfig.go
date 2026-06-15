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
