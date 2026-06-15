// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"math"
	"time"
)

// SLA buckets, derived from a case's deadline relative to "now".
const (
	SLAOnTrack = "on_track"
	SLADueSoon = "due_soon" // due within one day
	SLAOverdue = "overdue"
)

// Deadline is when a case opened at createdAt with an slaDays window is due.
// An slaDays of 0 means the case is due at the moment it opened.
func Deadline(createdAt time.Time, slaDays int) time.Time {
	return createdAt.AddDate(0, 0, slaDays)
}

// DaysLeft is the whole days remaining until the deadline, floored — a partial
// final day still counts until it elapses — and goes negative once overdue.
func DaysLeft(createdAt time.Time, slaDays int, now time.Time) int {
	rem := Deadline(createdAt, slaDays).Sub(now)
	return int(math.Floor(rem.Hours() / 24))
}

// SLAState buckets a case by how close it is to (or past) its deadline.
func SLAState(createdAt time.Time, slaDays int, now time.Time) string {
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
