// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/domain"
)

// FuzzSLA asserts the SLA arithmetic stays sane for any (createdAt, slaDays, now):
// no panic, a known bucket, and no overflow wrapping a far-future deadline into a
// "wrong" (e.g. overdue) bucket. SweepSLA folds slaDays straight from recorded event
// payloads, so an absurd or negative window must be clamped, not trusted — the
// MaxSLADays cap exists precisely so the AddDate/Duration math can't wrap.
func FuzzSLA(f *testing.F) {
	seeds := []struct {
		createdUnix int64
		slaDays     int
		nowUnix     int64
	}{
		{1_700_000_000, 3, 1_700_000_000},
		{1_700_000_000, 0, 1_700_086_400},
		{1_700_000_000, -1, 1_700_000_000},
		{0, 1 << 40, 1_700_000_000},
		{-1 << 40, 5, 1 << 40},
		{1_700_000_000, 1<<63 - 1, 1_700_000_000},
		{1_700_000_000, -(1 << 62), 1_700_000_000},
	}
	for _, s := range seeds {
		f.Add(s.createdUnix, s.slaDays, s.nowUnix)
	}
	f.Fuzz(func(t *testing.T, createdUnix int64, slaDays int, nowUnix int64) {
		createdAt := time.Unix(createdUnix, 0).UTC()
		now := time.Unix(nowUnix, 0).UTC()

		state := domain.SLAState(createdAt, slaDays, now)
		switch state {
		case domain.SLAOnTrack, domain.SLADueSoon, domain.SLAOverdue:
		default:
			t.Fatalf("SLAState returned unknown bucket %q", state)
		}

		left := domain.DaysLeft(createdAt, slaDays, now)
		// DaysLeft and SLAState must agree: a non-negative number of days left is never
		// overdue, and a positive (> 1 day) margin is on track, not due-soon/overdue.
		// A wrapping overflow would break this consistency.
		if left < 0 && state == domain.SLAOnTrack {
			t.Fatalf("inconsistent: DaysLeft=%d (overdue) but SLAState=on_track (created=%v sla=%d now=%v)", left, createdAt, slaDays, now)
		}
		if left > 1 && state != domain.SLAOnTrack {
			t.Fatalf("inconsistent: DaysLeft=%d (>1) but SLAState=%q (created=%v sla=%d now=%v)", left, state, createdAt, slaDays, now)
		}

		// The deadline of a non-negative window must never land before createdAt — that
		// only happens when AddDate overflows the time range, which the clamp prevents.
		if slaDays >= 0 && domain.Deadline(createdAt, slaDays).Before(createdAt) {
			t.Fatalf("deadline before createdAt for non-negative sla %d (created=%v)", slaDays, createdAt)
		}
	})
}
