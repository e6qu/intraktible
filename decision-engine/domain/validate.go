// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"fmt"
	"strings"

	"github.com/expr-lang/expr"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// validateHitAggregate rejects an unknown decision-table hit policy or aggregate at
// publish time, so a typo (e.g. hit:"anyy") fails when the flow is published rather
// than surfacing as a runtime "unknown hit policy" error on the first production
// decision. Mirrors the normalization the executor applies (lower/trim; empty hit
// defaults to first / the deprecated mode path).
func validateHitAggregate(n events.Node, cfg decisionTableConfig) error {
	switch hitPolicy(strings.ToLower(strings.TrimSpace(cfg.Hit))) {
	case "", hitFirst, hitUnique, hitAny, hitRuleOrder, hitCollect:
	default:
		return fmt.Errorf("decision-engine: node %q: unknown hit policy %q (first|unique|any|rule_order|collect)", n.ID, cfg.Hit)
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Hit), string(hitCollect)) {
		switch strings.ToLower(strings.TrimSpace(cfg.Aggregate)) {
		case "", "count", "sum", "min", "max":
		default:
			return fmt.Errorf("decision-engine: node %q: unknown aggregate %q (count|sum|min|max)", n.ID, cfg.Aggregate)
		}
	}
	return nil
}

// ValidateFlow checks that a graph is publishable: structurally valid
// (ValidateGraph) AND every node's config decodes for its type and its
// expressions compile. It is a pure, side-effect-free "dry compile" — it never
// runs the flow, never resolves a connector/agent/model reference (those may be
// defined after the flow is published), and touches no I/O. It gates PublishVersion
// so a semantically-broken flow (a node with malformed config, a rule with an
// uncompilable condition, a Code node that won't parse) is rejected at the write
// boundary instead of failing on the first live decision in production.
func ValidateFlow(g events.Graph) error {
	if err := ValidateGraph(g); err != nil {
		return err
	}
	for _, n := range g.Nodes {
		if err := validateNodeConfig(n); err != nil {
			return err
		}
	}
	return validateUniqueOutputs(g)
}

// validateUniqueOutputs rejects two Connect (or two AI, or two Predict) nodes that
// write the same output name. Resolved results are collected into a map keyed by
// output within each namespace (connect/ai/predict), so a duplicate silently
// discards one node's result while the connector/model is still called — an
// order-dependent, invisible data loss. Catching it at publish makes the collision
// impossible to ship.
func validateUniqueOutputs(g events.Graph) error {
	connects, err := ConnectSpecs(g)
	if err != nil {
		return err
	}
	ais, err := AISpecs(g)
	if err != nil {
		return err
	}
	predicts, err := PredictSpecs(g)
	if err != nil {
		return err
	}
	checkDup := func(kind string, outputs []string) error {
		seen := make(map[string]bool, len(outputs))
		for _, o := range outputs {
			if seen[o] {
				return fmt.Errorf("decision-engine: duplicate %s output %q — each %s node must write a distinct output", kind, o, kind)
			}
			seen[o] = true
		}
		return nil
	}
	connectOut := make([]string, len(connects))
	for i, s := range connects {
		connectOut[i] = s.Output
	}
	aiOut := make([]string, len(ais))
	for i, s := range ais {
		aiOut[i] = s.Output
	}
	predictOut := make([]string, len(predicts))
	for i, s := range predicts {
		predictOut[i] = s.Output
	}
	if err := checkDup("connect", connectOut); err != nil {
		return err
	}
	if err := checkDup("ai", aiOut); err != nil {
		return err
	}
	return checkDup("predict", predictOut)
}

// validateNodeConfig decodes a node's config (the same strict decode the executor
// uses, so it rejects exactly what runtime decode would) and compiles every
// expression it carries. It deliberately does not resolve references or evaluate
// anything — only shape + syntax.
func validateNodeConfig(n events.Node) error {
	switch n.Type {
	case events.NodeAssignment:
		var cfg assignmentConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return err
		}
		for _, a := range cfg.Assignments {
			if err := checkExpr(n, "assignment "+a.Target, a.Expr); err != nil {
				return err
			}
		}
	case events.NodeRule:
		var cfg ruleConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return err
		}
		for i, r := range cfg.Rules {
			if err := checkExpr(n, fmt.Sprintf("rule %d condition", i), r.When); err != nil {
				return err
			}
			for _, a := range r.Then {
				if err := checkExpr(n, fmt.Sprintf("rule %d assignment %q", i, a.Target), a.Expr); err != nil {
					return err
				}
			}
		}
	case events.NodeSplit:
		var cfg splitConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return err
		}
		// checkExpr passes an empty expression, which is right for the nodes where
		// omitting one means "no rule". A split with no condition has nothing to
		// route on, so it would publish and then fail on every decision.
		if strings.TrimSpace(cfg.Condition) == "" {
			return fmt.Errorf("decision-engine: node %q split has no condition", n.ID)
		}
		if err := checkExpr(n, "split condition", cfg.Condition); err != nil {
			return err
		}
	case events.NodeScorecard:
		var cfg scorecardConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return err
		}
		for i, f := range cfg.Factors {
			if err := checkExpr(n, fmt.Sprintf("factor %d", i), f.When); err != nil {
				return err
			}
		}
	case events.NodeReason:
		var cfg reasonConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return err
		}
		for i, r := range cfg.Reasons {
			if err := checkExpr(n, fmt.Sprintf("reason %d condition", i), r.When); err != nil {
				return err
			}
		}
	case events.NodeDecisionTable:
		var cfg decisionTableConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return err
		}
		if err := validateHitAggregate(n, cfg); err != nil {
			return err
		}
		for i, row := range cfg.Rows {
			if err := checkExpr(n, fmt.Sprintf("row %d condition", i), row.When); err != nil {
				return err
			}
			for _, a := range row.Outputs {
				if err := checkExpr(n, fmt.Sprintf("row %d output %q", i, a.Target), a.Expr); err != nil {
					return err
				}
			}
		}
	case events.NodeMatrix2D:
		var cfg matrixConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return err
		}
		for i, r := range cfg.Rows {
			if err := checkExpr(n, fmt.Sprintf("row axis %d", i), r.When); err != nil {
				return err
			}
		}
		for i, c := range cfg.Cols {
			if err := checkExpr(n, fmt.Sprintf("col axis %d", i), c.When); err != nil {
				return err
			}
		}
	case events.NodeManualReview:
		var cfg manualReviewConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return err
		}
		if err := checkExpr(n, "company_name", cfg.CompanyName); err != nil {
			return err
		}
		if err := checkExpr(n, "case_type", cfg.CaseType); err != nil {
			return err
		}
		// A negative SLA would emit a ManualReviewRequested already-overdue on
		// arrival; an absurd one overflows the case manager's date arithmetic. Bound
		// it at publish (10000 days ≈ 27 years, matching case-manager's MaxSLADays).
		if cfg.SLADays < 0 || cfg.SLADays > 10000 {
			return fmt.Errorf("decision-engine: node %q manual_review sla_days must be between 0 and 10000, got %d", n.ID, cfg.SLADays)
		}
	case events.NodeCode:
		var cfg codeConfig
		if err := decodeConfig(n, &cfg); err != nil {
			return err
		}
		if strings.TrimSpace(cfg.Code) != "" {
			if _, err := codeOpts.Parse(n.ID+".star", []byte(cfg.Code), 0); err != nil {
				return fmt.Errorf("decision-engine: node %q code: %w", n.ID, err)
			}
		}
	case events.NodeConnect:
		return decodeConfig(n, &connectConfig{})
	case events.NodeAI:
		return decodeConfig(n, &aiConfig{})
	case events.NodePredict:
		return decodeConfig(n, &predictConfig{})
	case events.NodeOutput:
		return decodeConfig(n, &outputConfig{})
	case events.NodeInput:
		// Input carries no executable config (its schema is the version's contract).
	default:
		// Unknown types are already rejected by ValidateGraph.
	}
	return nil
}

// checkExpr compiles a single expr-lang expression for syntax, with no
// environment so unknown identifiers (resolved from the decision context at run
// time) are permitted — this is a parse/syntax check, never an evaluation. An
// empty optional expression is left to the executor (skipped here, not rejected).
func checkExpr(n events.Node, where, code string) error {
	if strings.TrimSpace(code) == "" {
		return nil
	}
	if _, err := expr.Compile(code); err != nil {
		return fmt.Errorf("decision-engine: node %q %s: %w", n.ID, where, err)
	}
	return nil
}
