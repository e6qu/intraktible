// SPDX-License-Identifier: AGPL-3.0-or-later

// Package testutil holds small shared helpers used by tests across the tree.
package testutil

import (
	"testing"
	"time"
)

// Eventually polls cond until it returns true or a one-second deadline elapses,
// reporting whether it succeeded. Used to assert on asynchronous projection
// updates delivered via the in-process event bus.
func Eventually(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
