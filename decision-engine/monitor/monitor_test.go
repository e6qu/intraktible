// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"testing"

	"github.com/e6qu/intraktible/decision-engine/analytics"
)

func TestEvaluate(t *testing.T) {
	snap := analytics.FlowMetrics{
		Total: 150, Completed: 90, Failed: 10, AvgDurationMS: 120,
		ByDisposition: map[string]int{"approve": 60, "decline": 10, "refer": 30},
	}
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
	empty := analytics.FlowMetrics{ByDisposition: map[string]int{}}
	for _, m := range []string{MetricFailureRate, MetricReferRate, MetricAutomationRate, MetricAvgLatencyMS} {
		s := Evaluate(empty, Rule{Metric: m, Op: OpGreaterThan, Threshold: 0})
		if s.Computable || s.Firing {
			t.Fatalf("%s on empty metrics: got %+v, want not computable", m, s)
		}
	}
	// Volume is always computable (zero is a real count).
	if s := Evaluate(empty, Rule{Metric: MetricVolume, Op: OpGreaterThan, Threshold: 0}); !s.Computable {
		t.Fatalf("volume should be computable on empty metrics: %+v", s)
	}
}

func TestValidation(t *testing.T) {
	if ValidMetric("nope") || !ValidMetric(MetricFailureRate) {
		t.Fatal("ValidMetric")
	}
	if ValidOp("ge") || !ValidOp(OpLessThan) {
		t.Fatal("ValidOp")
	}
}
