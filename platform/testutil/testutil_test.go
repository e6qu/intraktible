// SPDX-License-Identifier: AGPL-3.0-or-later

package testutil_test

import (
	"testing"

	"github.com/e6qu/intraktible/platform/testutil"
)

func TestEventually(t *testing.T) {
	calls := 0
	if !testutil.Eventually(t, func() bool {
		calls++
		return calls >= 3
	}) {
		t.Fatal("Eventually should succeed once the condition holds")
	}
	if testutil.Eventually(t, func() bool { return false }) {
		t.Fatal("Eventually should report false when the condition never holds")
	}
}
