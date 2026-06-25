// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"math"
	"time"
)

// SLAStatus buckets a case by its deadline relative to "now". A named type (not a
// bare string) — the same convention as CaseStatus in this package — so the
// read-model field and the switch arms are a known bucket, not an arbitrary string.
type SLAStatus string

// SLA buckets, derived from a case's deadline relative to "now".
const (
	SLAOnTrack SLAStatus = "on_track"
	SLADueSoon SLAStatus = "due_soon" // due within one day
	SLAOverdue SLAStatus = "overdue"
)

// MaxSLADays caps an SLA window (~27 years) so an absurd value can't overflow the
// AddDate/Duration arithmetic in Deadline/DaysLeft into a wrong (wrapped) bucket.
const MaxSLADays = 10000

// DefaultSLADays is the SLA window applied when a case opens without one: a zero
// window makes the case due the instant it opens (immediately overdue), so a
// sensible default gives the reviewer time to act.
const DefaultSLADays = 3

// Deadline is when a case opened at createdAt with an slaDays window is due.
// An slaDays of 0 means the case is due at the moment it opened. The window is
// clamped to [0, MaxSLADays]: a value persisted outside that range (the SLA sweep
// folds slaDays straight from recorded event payloads, which the open-command
// validation does not re-check) would otherwise overflow AddDate and wrap a
// far-future deadline into the past, mis-bucketing the case as overdue.
func Deadline(createdAt time.Time, slaDays int) time.Time {
	if slaDays < 0 {
		slaDays = 0
	}
	if slaDays > MaxSLADays {
		slaDays = MaxSLADays
	}
	return createdAt.AddDate(0, 0, slaDays)
}

// DaysLeft is the whole days remaining until the deadline, floored — a partial
// final day still counts until it elapses — and goes negative once overdue.
func DaysLeft(createdAt time.Time, slaDays int, now time.Time) int {
	rem := Deadline(createdAt, slaDays).Sub(now)
	return int(math.Floor(rem.Hours() / 24))
}

// SLAState buckets a case by how close it is to (or past) its deadline.
func SLAState(createdAt time.Time, slaDays int, now time.Time) SLAStatus {
	deadline := Deadline(createdAt, slaDays)
	switch {
	case !now.Before(deadline):
		return SLAOverdue
	case deadline.Sub(now) <= 24*time.Hour:
		return SLADueSoon
	default:
		return SLAOnTrack
	}
}
