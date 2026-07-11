// SPDX-License-Identifier: AGPL-3.0-or-later

package analytics_test

import (
	"math"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/analytics"
)

func TestAttainmentWindowIsolatesRecentBreach(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	// A long-lived flow: a flawless run 10 days ago, then a bad day today.
	m := analytics.FlowMetrics{
		Completed: 108, Failed: 2, AvgDurationMS: 30,
		Daily: []analytics.DailyBucket{
			{Date: "2026-07-01", Completed: 100, Failed: 0, TotalDurationMS: 2000},
			{Date: "2026-07-11", Completed: 8, Failed: 2, TotalDurationMS: 400},
		},
	}
	// All-time (window 0) dilutes the breach: 108/110 = 98.2% ≥ 95% target → met.
	all := analytics.AttainmentWindow(m, 0.95, 0, now, 0)
	if !all.SuccessMet || all.WindowDays != 0 {
		t.Fatalf("all-time should meet the target: %+v", all)
	}
	// A 3-day window sees only today: 8/10 = 80% < 95% → breached, which is the point.
	win := analytics.AttainmentWindow(m, 0.95, 0, now, 3)
	if win.SuccessMet {
		t.Fatalf("3-day window should surface the recent breach: %+v", win)
	}
	if win.Decisions != 10 || win.WindowDays != 3 {
		t.Fatalf("window should count only in-range days: %+v", win)
	}
	if math.Abs(win.SuccessRate-0.8) > 1e-9 {
		t.Fatalf("window success rate = %v, want 0.8", win.SuccessRate)
	}
}

func TestAttainmentWindowCapsAtRetention(t *testing.T) {
	// A window past the retained history is clamped, not silently empty.
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	m := analytics.FlowMetrics{Daily: []analytics.DailyBucket{{Date: "2026-07-11", Completed: 1}}}
	a := analytics.AttainmentWindow(m, 0.99, 0, now, 100000)
	if a.WindowDays != 90 {
		t.Fatalf("window should clamp to the retention bound (90), got %d", a.WindowDays)
	}
	if a.Decisions != 1 {
		t.Fatalf("clamped window still counts the retained day: %+v", a)
	}
}

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
