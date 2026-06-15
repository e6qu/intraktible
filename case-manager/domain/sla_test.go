// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/domain"
)

func TestDaysLeft(t *testing.T) {
	opened := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		slaDays int
		now     time.Time
		want    int
	}{
		{"five days out, just opened", 5, opened, 5},
		{"one day elapsed", 5, opened.Add(24 * time.Hour), 4},
		{"partial final day still counts", 5, opened.Add(4*24*time.Hour + 12*time.Hour), 0},
		{"twelve hours overdue", 5, opened.Add(5*24*time.Hour + 12*time.Hour), -1},
		{"zero-day sla is due at open", 0, opened, 0},
	}
	for _, c := range cases {
		if got := domain.DaysLeft(opened, c.slaDays, c.now); got != c.want {
			t.Errorf("%s: DaysLeft = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSLAState(t *testing.T) {
	opened := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		slaDays int
		now     time.Time
		want    string
	}{
		{"well within window", 5, opened.Add(24 * time.Hour), domain.SLAOnTrack},
		{"under a day to go", 5, opened.Add(4*24*time.Hour + 6*time.Hour), domain.SLADueSoon},
		{"exactly at deadline is overdue", 5, opened.Add(5 * 24 * time.Hour), domain.SLAOverdue},
		{"past deadline", 5, opened.Add(6 * 24 * time.Hour), domain.SLAOverdue},
	}
	for _, c := range cases {
		if got := domain.SLAState(opened, c.slaDays, c.now); got != c.want {
			t.Errorf("%s: SLAState = %q, want %q", c.name, got, c.want)
		}
	}
}
