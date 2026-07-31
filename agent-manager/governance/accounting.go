// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"fmt"
	"time"
)

// invocationCostUSD derives cost only from the immutable rates reviewed with
// the release. Runtime price tables may inform a new release, but cannot change
// the meaning of already-recorded usage or its budget admission.
func invocationCostUSD(budget Budget, promptTokens, outputTokens int) float64 {
	return float64(promptTokens)/1_000_000*budget.InputCostPerMTok +
		float64(outputTokens)/1_000_000*budget.OutputCostPerMTok
}

func enforceInvocationBudget(
	budget Budget,
	promptTokens, outputTokens, toolCalls int,
) (float64, error) {
	cost := invocationCostUSD(budget, promptTokens, outputTokens)
	switch {
	case promptTokens > budget.MaxPromptTokens:
		return cost, fmt.Errorf(
			"agent governance: prompt tokens %d exceed reviewed per-run limit %d",
			promptTokens, budget.MaxPromptTokens,
		)
	case outputTokens > budget.MaxCompletionTokens:
		return cost, fmt.Errorf(
			"agent governance: completion tokens %d exceed reviewed per-run limit %d",
			outputTokens, budget.MaxCompletionTokens,
		)
	case toolCalls > budget.MaxToolCalls:
		return cost, fmt.Errorf(
			"agent governance: tool calls %d exceed reviewed per-run limit %d",
			toolCalls, budget.MaxToolCalls,
		)
	case cost > budget.MaxCostUSD:
		return cost, fmt.Errorf(
			"agent governance: cost %.8f USD exceeds reviewed per-run limit %.8f USD",
			cost, budget.MaxCostUSD,
		)
	default:
		return cost, nil
	}
}

func budgetPeriodStart(now time.Time, period string) time.Time {
	now = now.UTC()
	switch period {
	case "day":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Time{}
	}
}
