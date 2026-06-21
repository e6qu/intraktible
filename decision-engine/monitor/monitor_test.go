// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"context"
	"math"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/analytics"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestEvaluate(t *testing.T) {
	snap := Snapshot{Metrics: analytics.FlowMetrics{
		Total: 150, Completed: 90, Failed: 10, AvgDurationMS: 120,
		ByDisposition: map[string]int{"approve": 60, "decline": 10, "refer": 30},
	}}
	cases := []struct {
		name           string
		rule           Rule
		wantActual     float64
		wantComputable bool
		wantFiring     bool
	}{
		{"failure rate breaches", Rule{MetricFailureRate, OpGreaterThan, 0.05}, 0.1, true, true},
		{"failure rate within", Rule{MetricFailureRate, OpGreaterThan, 0.2}, 0.1, true, false},
		{"refer rate breaches", Rule{MetricReferRate, OpGreaterThan, 0.25}, 0.3, true, true},
		{"automation below floor", Rule{MetricAutomationRate, OpLessThan, 0.8}, 0.7, true, true},
		{"approve rate", Rule{MetricApproveRate, OpLessThan, 0.5}, 0.6, true, false},
		{"decline rate", Rule{MetricDeclineRate, OpGreaterThan, 0.05}, 0.1, true, true},
		{"avg latency breaches", Rule{MetricAvgLatencyMS, OpGreaterThan, 50}, 120, true, true},
		{"volume floor", Rule{MetricVolume, OpLessThan, 200}, 150, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Evaluate(snap, c.rule)
			if s.Computable != c.wantComputable || s.Firing != c.wantFiring || s.Actual != c.wantActual {
				t.Fatalf("got %+v, want actual=%v computable=%v firing=%v",
					s, c.wantActual, c.wantComputable, c.wantFiring)
			}
		})
	}
}

func TestEvaluateNoData(t *testing.T) {
	// An unused flow: rates have no denominator and must read as "no data", never firing.
	empty := Snapshot{Metrics: analytics.FlowMetrics{ByDisposition: map[string]int{}}}
	for _, m := range []Metric{MetricFailureRate, MetricReferRate, MetricAutomationRate, MetricAvgLatencyMS, MetricDistributionDrift} {
		s := Evaluate(empty, Rule{Metric: m, Op: OpGreaterThan, Threshold: 0})
		if s.Computable || s.Firing {
			t.Fatalf("%s on empty snapshot: got %+v, want not computable", m, s)
		}
	}
	// Volume is always computable (zero is a real count).
	if s := Evaluate(empty, Rule{Metric: MetricVolume, Op: OpGreaterThan, Threshold: 0}); !s.Computable {
		t.Fatalf("volume should be computable on empty metrics: %+v", s)
	}
}

func TestDistributionDrift(t *testing.T) {
	// Baseline: 70% approve / 10% decline / 20% refer. Current shifts toward refer.
	base := &Baseline{Approve: 0.7, Decline: 0.1, Refer: 0.2, Total: 100}
	snap := Snapshot{
		Metrics:  analytics.FlowMetrics{ByDisposition: map[string]int{"approve": 50, "decline": 10, "refer": 40}},
		Baseline: base,
	}
	// Shares now 0.5/0.1/0.4 → max drift = |0.4-0.2| = |0.5-0.7| = 0.2.
	st := Evaluate(snap, Rule{Metric: MetricDistributionDrift, Op: OpGreaterThan, Threshold: 0.15})
	if !st.Computable || !st.Firing || st.Actual < 0.199 || st.Actual > 0.201 {
		t.Fatalf("drift should fire at ~0.2: %+v", st)
	}
	// Without a baseline, drift is not computable.
	if s := Evaluate(Snapshot{Metrics: snap.Metrics}, Rule{Metric: MetricDistributionDrift, Op: OpGreaterThan, Threshold: 0}); s.Computable {
		t.Fatalf("drift without a baseline must be not computable: %+v", s)
	}

	rep := ComputeDrift(snap)
	if !rep.HasBaseline || !rep.HasCurrent || rep.MaxDrift < 0.199 || len(rep.Buckets) != 3 {
		t.Fatalf("unexpected drift report: %+v", rep)
	}
}

func TestTransition(t *testing.T) {
	cases := []struct {
		firing, alerting bool
		want             transitionAction
	}{
		{true, false, actionAlert},   // ok -> firing: notify
		{true, true, actionNone},     // still firing: no re-notify
		{false, true, actionResolve}, // firing -> ok: reset
		{false, false, actionNone},   // still ok: nothing
	}
	for _, c := range cases {
		if got := transition(c.firing, c.alerting); got != c.want {
			t.Fatalf("transition(%v,%v)=%v want %v", c.firing, c.alerting, got, c.want)
		}
	}
}

func TestValidation(t *testing.T) {
	if ValidMetric("nope") || !MetricFailureRate.Valid() {
		t.Fatal("ValidMetric")
	}
	if ValidOp("ge") || !OpLessThan.Valid() {
		t.Fatal("ValidOp")
	}
}

// A NaN/Inf threshold compares false against every value, so a monitor with one
// would silently never fire. Define must reject it at the write boundary.
func TestDefineRejectsNonFiniteThreshold(t *testing.T) {
	log, _ := testutil.NewLogStore(t)
	h := NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "ops"}
	ctx := context.Background()
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, _, err := h.Define(ctx, id, DefineCmd{
			FlowID: "f", Metric: MetricFailureRate, Op: OpGreaterThan, Threshold: bad,
		})
		if err == nil {
			t.Fatalf("Define accepted a non-finite threshold %v", bad)
		}
	}
	// A finite threshold still works.
	if _, _, err := h.Define(ctx, id, DefineCmd{
		FlowID: "f", Metric: MetricFailureRate, Op: OpGreaterThan, Threshold: 0.5,
	}); err != nil {
		t.Fatalf("Define rejected a valid finite threshold: %v", err)
	}
}
