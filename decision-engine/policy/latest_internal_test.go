// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import "testing"

// latestVersion must pick the version tracked by Latest, not the last slice element:
// Versions is appended in event order, which is not guaranteed to be version order,
// so selecting by position could silently run the wrong spec on the decide path.
func TestLatestVersionSelectsByLatestNotPosition(t *testing.T) {
	pv := View{
		Latest: 2,
		// Deliberately out of order, with the latest NOT last.
		Versions: []VersionView{{Version: 1}, {Version: 3}, {Version: 2}},
	}
	if got := latestVersion(pv); got.Version != 2 {
		t.Fatalf("latestVersion = v%d, want v2 (the tracked Latest)", got.Version)
	}

	// Fallback: if Latest somehow isn't present, fall back to the last element
	// rather than panic.
	pv2 := View{Latest: 99, Versions: []VersionView{{Version: 1}, {Version: 2}}}
	if got := latestVersion(pv2); got.Version != 2 {
		t.Fatalf("fallback latestVersion = v%d, want v2 (last element)", got.Version)
	}
}
