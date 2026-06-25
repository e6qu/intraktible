// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	cmevents "github.com/e6qu/intraktible/case-manager/events"
	deevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/store"
)

// caseEventTypes are the case-lifecycle events the notifications projector folds
// into the case index + task notifications. The fuzzer drives each with an
// arbitrary payload so a truncated/garbage one must not panic the projector.
var caseEventTypes = []string{
	cmevents.TypeReviewRequested,
	deevents.TypeManualReviewRequested,
	cmevents.TypeCaseAssigned,
	cmevents.TypeCaseSLAReminder,
	cmevents.TypeCaseSLABreached,
}

// FuzzCaseProjector asserts the notifications case-index projector never panics on
// an arbitrary case-event payload: it decodes ReviewRequested / ManualReviewRequested
// / CaseAssigned / CaseSLAReminder / CaseSLABreached, folds them into the case index,
// and writes a task notification or no-op. A malformed payload must come back as a
// decode error, never a crash, and an SLA event for an unknown case must no-op.
func FuzzCaseProjector(f *testing.F) {
	seeds := []string{
		`{"case_id":"c1","company_name":"Acme","case_type":"aml","sla_days":3}`,
		`{"case_id":"c1","assignee":"alice"}`,
		`{"case_id":"c1"}`,
		`{}`,
		`{"case_id":"","company_name":"","case_type":""}`,
		`{"sla_days":-1,"case_id":"c2"}`,
		`null`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, payloadJSON string) {
		if !json.Valid([]byte(payloadJSON)) {
			return
		}
		ctx := context.Background()
		s := store.NewMemory()
		proj := notifications.Projector{}
		// Replay every case-event type over the SAME store and a rising seq, so the
		// CaseAssigned/SLA index lookups run against whatever a prior event wrote.
		for i, typ := range caseEventTypes {
			e := eventlog.Envelope{
				Org: "demo", Workspace: "main", Actor: "system",
				Type: typ, Time: time.Unix(int64(i), 0).UTC(), Seq: uint64(i + 1),
				Payload: json.RawMessage(payloadJSON),
			}
			// INVARIANT: Apply returns (a decode error or nil), never panics.
			_ = proj.Apply(ctx, e, s)
		}
		// The fold must leave the store in a listable state for any recipient.
		if _, err := notifications.List(ctx, s, identity.Identity{Org: "demo", Workspace: "main", Actor: "alice"}, true); err != nil {
			t.Fatalf("List after fold: %v", err)
		}
	})
}
