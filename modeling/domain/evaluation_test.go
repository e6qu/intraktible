// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"testing"
	"time"
)

func TestEvaluateBinaryIncludesUncertaintyFairnessAndLeakage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []ScoredRow{
		{EntityID: "a", Score: 0.9, Label: 1, Partition: "validation", Segments: map[string]string{"region": "north"}, ObservationAt: at},
		{EntityID: "b", Score: 0.8, Label: 1, Partition: "validation", Segments: map[string]string{"region": "south"}, ObservationAt: at},
		{EntityID: "c", Score: 0.2, Label: 0, Partition: "validation", Segments: map[string]string{"region": "north"}, ObservationAt: at},
		{EntityID: "d", Score: 0.1, Label: 0, Partition: "train", Segments: map[string]string{"region": "south"}, ObservationAt: at},
	}
	report, err := EvaluateBinary(rows, EvaluationOptions{MinimumSegmentN: 3})
	if err != nil {
		t.Fatal(err)
	}
	if report.AUC != 1 || report.Accuracy != 1 || report.PassedLeakageChecks {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Segments) != 2 || len(report.Calibration) != 10 ||
		report.AccuracyCI.Lower <= 0 || report.AccuracyCI.Upper > 1 {
		t.Fatalf("structured evidence is incomplete: %+v", report)
	}
}
