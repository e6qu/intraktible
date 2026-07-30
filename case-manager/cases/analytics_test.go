// SPDX-License-Identifier: AGPL-3.0-or-later

package cases_test

import (
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/domain"
)

func TestOperationalAnalyticsUsesRecordedTimingAndCurrentBacklogClock(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	views := []cases.CaseView{
		{
			CaseID: "open", Status: domain.StatusInProgress, Priority: domain.PriorityHigh,
			Queue: "aml", Assignee: "alice", CreatedAt: now.Add(-48 * time.Hour),
			FirstActionAt: now.Add(-47 * time.Hour), SLADays: 1, SLABreached: true,
			QA: &cases.QAReview{Status: "completed", Agreement: false, Override: true},
		},
		{
			CaseID: "done", Status: domain.StatusCompleted, Priority: domain.PriorityNormal,
			Queue: "aml", Assignee: "bob", CreatedAt: now.Add(-10 * time.Hour),
			FirstActionAt: now.Add(-9 * time.Hour), ResolvedAt: now.Add(-2 * time.Hour), SLADays: 3,
		},
		{
			CaseID: "new", Status: domain.StatusNeedsReview, Priority: domain.PriorityCritical,
			CreatedAt: now.Add(-2 * time.Hour), SLADays: 3,
		},
	}
	reviewers := []cases.ReviewerView{
		{Profile: domain.ReviewerProfile{Actor: "alice", Capacity: 2, Active: true}},
		{Profile: domain.ReviewerProfile{Actor: "bob", Capacity: 4, Active: true}},
	}
	queues := []cases.QueueView{
		{Definition: domain.QueueDefinition{Key: "aml", Capacity: 4}},
		{Definition: domain.QueueDefinition{Key: "empty", Capacity: 3}},
	}
	got := cases.BuildAnalytics(views, reviewers, queues, now)
	if got.Total != 3 || got.Open != 2 || got.Completed != 1 ||
		got.Unassigned != 1 || got.Unrouted != 1 || got.Overdue != 1 || got.SLABreached != 1 {
		t.Fatalf("analytics counters = %+v", got)
	}
	if got.AverageFirstActionSecs != 3600 || got.AverageResolutionSecs != 8*3600 {
		t.Fatalf("timing averages first=%v resolution=%v", got.AverageFirstActionSecs, got.AverageResolutionSecs)
	}
	if got.QASelected != 1 || got.QACompleted != 1 || got.QADisagreements != 1 || got.QAOverrides != 1 {
		t.Fatalf("QA analytics = %+v", got)
	}
	if len(got.Workloads) != 2 || got.Workloads[0].Actor != "alice" || got.Workloads[0].Utilization != 0.5 {
		t.Fatalf("workloads = %+v", got.Workloads)
	}
	if len(got.Queues) != 3 || got.Queues[0].Queue != "aml" ||
		got.Queues[0].Capacity != 4 || got.Queues[0].Utilization != 0.25 ||
		got.Queues[2].Queue != "empty" || got.Queues[2].Open != 0 {
		t.Fatalf("queue analytics = %+v", got.Queues)
	}
}
