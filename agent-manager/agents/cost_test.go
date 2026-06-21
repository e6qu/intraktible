// SPDX-License-Identifier: AGPL-3.0-or-later

package agents_test

import (
	"math"
	"testing"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/agent-manager/domain"
)

func TestParsePricing(t *testing.T) {
	p, err := agents.ParsePricing("gpt-4o=2.5/10, claude-opus=15/75")
	if err != nil {
		t.Fatal(err)
	}
	if p["gpt-4o"].InputPerMTok != 2.5 || p["gpt-4o"].OutputPerMTok != 10 {
		t.Fatalf("gpt-4o = %+v", p["gpt-4o"])
	}
	if p["claude-opus"].OutputPerMTok != 75 {
		t.Fatalf("claude-opus = %+v", p["claude-opus"])
	}
}

func TestParsePricingEmpty(t *testing.T) {
	p, err := agents.ParsePricing("")
	if err != nil || p != nil {
		t.Fatalf("empty pricing = %v, %v", p, err)
	}
}

func TestParsePricingMalformed(t *testing.T) {
	for _, bad := range []string{"gpt-4o", "gpt-4o=2.5", "gpt-4o=x/10", "gpt-4o=2.5/-1"} {
		if _, err := agents.ParsePricing(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestPricingCostUSD(t *testing.T) {
	p := agents.Pricing{"m": {InputPerMTok: 3, OutputPerMTok: 15}}
	// 1M prompt @ $3 + 1M completion @ $15 = $18.
	cost, ok := p.CostUSD("m", 1_000_000, 1_000_000)
	if !ok || math.Abs(cost-18) > 1e-9 {
		t.Fatalf("cost = %v, ok=%v", cost, ok)
	}
	if _, ok := p.CostUSD("other", 100, 100); ok {
		t.Fatal("unpriced model must report priced=false")
	}
}

// SummarizeRuns aggregates token usage total and per model, attributing an
// empty-model run to "unknown" rather than dropping its tokens.
func TestSummarizeRunsUsage(t *testing.T) {
	runs := []agents.RunView{
		{Agent: "a", Model: "m1", Status: domain.RunCompleted, PromptTokens: 100, CompletionTokens: 20},
		{Agent: "a", Model: "m1", Status: domain.RunCompleted, PromptTokens: 50, CompletionTokens: 10},
		{Agent: "b", Model: "", Status: domain.RunFailed, PromptTokens: 5, CompletionTokens: 0},
	}
	s := agents.SummarizeRuns(runs)
	if s.PromptTokens != 155 || s.CompletionTokens != 30 {
		t.Fatalf("totals = %d/%d", s.PromptTokens, s.CompletionTokens)
	}
	if s.ByModel["m1"].Runs != 2 || s.ByModel["m1"].PromptTokens != 150 {
		t.Fatalf("m1 = %+v", s.ByModel["m1"])
	}
	if s.ByModel["unknown"].PromptTokens != 5 {
		t.Fatalf("unknown bucket = %+v", s.ByModel["unknown"])
	}
}

// Cost derives per-model and total USD only for priced models, and reports
// Priced=false when no pricing is configured.
func TestPricingCostReport(t *testing.T) {
	summary := agents.RunSummary{ByModel: map[string]agents.ModelUsage{
		"m1": {Runs: 1, PromptTokens: 1_000_000, CompletionTokens: 0},
		"m2": {Runs: 1, PromptTokens: 1_000_000, CompletionTokens: 0},
	}}
	none := agents.Pricing(nil).Cost(summary)
	if none.Priced {
		t.Fatal("nil pricing must report Priced=false")
	}
	rep := agents.Pricing{"m1": {InputPerMTok: 2}}.Cost(summary)
	if !rep.Priced || math.Abs(rep.TotalCostUSD-2) > 1e-9 {
		t.Fatalf("report = %+v", rep)
	}
	if _, ok := rep.CostByModel["m2"]; ok {
		t.Fatal("unpriced m2 must be omitted from cost_by_model")
	}
}
