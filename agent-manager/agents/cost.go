// SPDX-License-Identifier: AGPL-3.0-or-later

package agents

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ModelPrice is one model's billing rate in USD per million tokens, split by
// input (prompt) and output (completion) — the standard way providers quote LLM
// pricing.
type ModelPrice struct {
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
}

// Pricing maps a model name to its rate. Operators supply it (model prices change
// and vary by contract, so shipping built-in defaults would mislead); without a
// price for a model, usage is still reported but cost is omitted rather than shown
// as a misleading zero.
type Pricing map[string]ModelPrice

// CostUSD computes the cost of a token usage at the named model's rate. priced is
// false when the model has no configured price — the caller then omits cost.
func (p Pricing) CostUSD(model string, promptTokens, completionTokens int) (cost float64, priced bool) {
	pr, ok := p[model]
	if !ok {
		return 0, false
	}
	cost = float64(promptTokens)/1e6*pr.InputPerMTok + float64(completionTokens)/1e6*pr.OutputPerMTok
	return cost, true
}

// ParsePricing reads the INTRAKTIBLE_AI_PRICES format: comma-separated
// "model=input/output" entries, each rate in USD per million tokens — e.g.
// "gpt-4o=2.5/10,claude-opus-4=15/75". An empty string yields nil (no pricing).
// A malformed entry is a configuration error returned loudly.
func ParsePricing(s string) (Pricing, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	p := Pricing{}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		model, rates, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("ai pricing: entry %q must be model=input/output", entry)
		}
		in, out, ok := strings.Cut(rates, "/")
		if !ok {
			return nil, fmt.Errorf("ai pricing: rates %q must be input/output", rates)
		}
		inRate, err := strconv.ParseFloat(strings.TrimSpace(in), 64)
		if err != nil || !finiteNonNegative(inRate) {
			return nil, fmt.Errorf("ai pricing: input rate %q for %q: want a non-negative number", in, model)
		}
		outRate, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
		if err != nil || !finiteNonNegative(outRate) {
			return nil, fmt.Errorf("ai pricing: output rate %q for %q: want a non-negative number", out, model)
		}
		p[strings.TrimSpace(model)] = ModelPrice{InputPerMTok: inRate, OutputPerMTok: outRate}
	}
	return p, nil
}

// finiteNonNegative rejects NaN and ±Inf in addition to negatives: strconv.ParseFloat
// accepts "NaN"/"Inf" as valid floats, and a non-finite rate poisons cost arithmetic
// (any product is NaN/Inf) and fails JSON marshaling of the cost report downstream.
func finiteNonNegative(r float64) bool {
	return r >= 0 && !math.IsInf(r, 0)
}

// CostReport is a run summary augmented with computed cost. It embeds RunSummary
// (token + count roll-ups) and adds per-model and total USD cost for the models
// that have a configured price. Priced is false when no pricing is configured at
// all, so the UI can explain why cost is absent.
type CostReport struct {
	RunSummary
	Priced       bool               `json:"priced"`
	TotalCostUSD float64            `json:"total_cost_usd"`
	CostByModel  map[string]float64 `json:"cost_by_model,omitempty"`
}

// Cost builds a CostReport from a summary at the given pricing. With nil/empty
// pricing it reports Priced=false and no cost (tokens still present in the
// embedded summary).
func (p Pricing) Cost(summary RunSummary) CostReport {
	rep := CostReport{RunSummary: summary}
	if len(p) == 0 {
		return rep
	}
	rep.Priced = true
	rep.CostByModel = map[string]float64{}
	for model, mu := range summary.ByModel {
		if cost, ok := p.CostUSD(model, mu.PromptTokens, mu.CompletionTokens); ok {
			rep.CostByModel[model] = cost
			rep.TotalCostUSD += cost
		}
	}
	return rep
}
