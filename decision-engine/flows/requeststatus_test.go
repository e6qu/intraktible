// SPDX-License-Identifier: AGPL-3.0-or-later

package flows_test

import (
	"testing"

	"github.com/e6qu/intraktible/decision-engine/flows"
)

func TestRequestStatusValid(t *testing.T) {
	for _, s := range []flows.RequestStatus{flows.RequestPending, flows.RequestApproved, flows.RequestRejected} {
		if !s.Valid() {
			t.Fatalf("request status %q should be valid", s)
		}
	}
	for _, s := range []flows.RequestStatus{"", "cancelled", "Pending"} {
		if s.Valid() {
			t.Fatalf("request status %q should be invalid", s)
		}
	}
}
