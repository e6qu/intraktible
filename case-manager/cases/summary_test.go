// SPDX-License-Identifier: AGPL-3.0-or-later

package cases_test

import (
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/domain"
)

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	views := []cases.CaseView{
		// on track, assigned
		{Status: domain.StatusNeedsReview, Assignee: "adam", SLADays: 5, CreatedAt: now.Add(-1 * day)},
		// due soon, unassigned
		{Status: domain.StatusInProgress, SLADays: 2, CreatedAt: now.Add(-36 * time.Hour)},
		// overdue, unassigned
		{Status: domain.StatusNeedsReview, SLADays: 1, CreatedAt: now.Add(-3 * day)},
		// completed: excluded from SLA buckets even though its deadline passed
		{Status: domain.StatusCompleted, Assignee: "bea", SLADays: 1, CreatedAt: now.Add(-9 * day)},
		// completed AND unassigned: must count toward NEITHER the SLA buckets NOR the
		// "unassigned" backlog (it is off the queue, not waiting for an owner).
		{Status: domain.StatusCompleted, SLADays: 1, CreatedAt: now.Add(-9 * day)},
	}

	s := cases.Summarize(views, now)

	if s.Total != 5 {
		t.Errorf("Total = %d, want 5", s.Total)
	}
	if s.ByStatus[domain.StatusNeedsReview] != 2 || s.ByStatus[domain.StatusInProgress] != 1 || s.ByStatus[domain.StatusCompleted] != 2 {
		t.Errorf("ByStatus = %v", s.ByStatus)
	}
	if s.Unassigned != 2 {
		t.Errorf("Unassigned = %d, want 2 (a completed unassigned case must not count)", s.Unassigned)
	}
	if s.DueSoon != 1 {
		t.Errorf("DueSoon = %d, want 1", s.DueSoon)
	}
	if s.Overdue != 1 {
		t.Errorf("Overdue = %d, want 1 (completed case excluded)", s.Overdue)
	}
}
