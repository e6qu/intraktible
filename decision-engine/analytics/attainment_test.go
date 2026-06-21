// SPDX-License-Identifier: AGPL-3.0-or-later

package analytics_test

import (
	"math"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/analytics"
)

func TestAttainmentMet(t *testing.T) {
	// 99 completed, 1 failed = 99% success, target 95% → met, budget mostly intact.
	m := analytics.FlowMetrics{Completed: 99, Failed: 1, AvgDurationMS: 40}
	a := analytics.Attainment(m, 0.95, 100)
	if !a.SuccessMet || !a.LatencyMet {
		t.Fatalf("expected met: %+v", a)
	}
	if math.Abs(a.SuccessRate-0.99) > 1e-9 {
		t.Fatalf("success rate = %v", a.SuccessRate)
	}
	// Error budget = 5%; consumed 1% → 80% of the budget remains.
	if math.Abs(a.BudgetRemaining-0.8) > 1e-9 {
		t.Fatalf("budget remaining = %v", a.BudgetRemaining)
	}
}

func TestAttainmentBreached(t *testing.T) {
	// 90% success vs a 95% target, and latency over the 50ms target.
	m := analytics.FlowMetrics{Completed: 90, Failed: 10, AvgDurationMS: 120}
	a := analytics.Attainment(m, 0.95, 50)
	if a.SuccessMet {
		t.Fatal("success should be breached")
	}
	if a.LatencyMet {
		t.Fatal("latency should be breached")
	}
	if a.BudgetRemaining >= 0 {
		t.Fatalf("budget should be exhausted (negative), got %v", a.BudgetRemaining)
	}
}

// With no decisions yet, objectives are reported met (nothing has breached them)
// and the full error budget remains.
func TestAttainmentNoData(t *testing.T) {
	a := analytics.Attainment(analytics.FlowMetrics{}, 0.99, 100)
	if !a.SuccessMet || !a.LatencyMet || a.Decisions != 0 || a.BudgetRemaining != 1 {
		t.Fatalf("no-data attainment = %+v", a)
	}
}

// A 100%-success target leaves no error budget: any failure is immediately over.
func TestAttainmentZeroBudget(t *testing.T) {
	a := analytics.Attainment(analytics.FlowMetrics{Completed: 9, Failed: 1}, 1.0, 0)
	if a.ErrorBudget != 0 || a.BudgetRemaining != 0 || a.SuccessMet {
		t.Fatalf("zero-budget attainment = %+v", a)
	}
	// No latency objective (target 0) → latency trivially met.
	if !a.LatencyMet {
		t.Fatal("no latency target should be trivially met")
	}
}
