// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cmevents "github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

// TestTaskNotificationsFromCaseLifecycle proves the reminder/notification system pulls
// reviewers to their human-review tasks: an unassigned case surfaces to the shared
// reviewer queue; assignment, a due-soon reminder, and an overdue breach each notify the
// assignee; and a non-reviewer does not see the queue.
func TestTaskNotificationsFromCaseLifecycle(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	sys := identity.Identity{Org: "demo", Workspace: "main", Actor: "system"}
	now := time.Now()
	emit := func(typ string, payload any) {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := eventlog.AppendJSON(ctx, log, sys.Org, sys.Workspace, sys.Actor,
			cmevents.StreamCases, typ, now, json.RawMessage(b)); err != nil {
			t.Fatal(err)
		}
	}

	emit(cmevents.TypeReviewRequested, cmevents.ReviewRequested{CaseID: "case_1", CompanyName: "Acme", CaseType: "aml", SLADays: 3})
	emit(cmevents.TypeCaseAssigned, cmevents.CaseAssigned{CaseID: "case_1", Assignee: "alice"})
	emit(cmevents.TypeCaseSLAReminder, cmevents.CaseSLAReminder{CaseID: "case_1"})
	emit(cmevents.TypeCaseSLABreached, cmevents.CaseSLABreached{CaseID: "case_1"})

	s := store.NewMemory()
	if err := projection.New(log, s, notifications.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}

	alice := identity.Identity{Org: "demo", Workspace: "main", Actor: "alice"}
	mine, err := notifications.List(ctx, s, alice, false)
	if err != nil {
		t.Fatal(err)
	}
	// Alice (the assignee) gets assigned + due-soon + overdue, all linked to her case.
	if len(mine) != 3 {
		t.Fatalf("assignee should have 3 task notifications, got %d: %+v", len(mine), mine)
	}
	for _, n := range mine {
		if n.Kind != notifications.KindTask || n.SubjectType != "case" || n.SubjectID != "case_1" {
			t.Fatalf("unexpected notification: %+v", n)
		}
	}

	// A review-capable user who owns nothing still sees the unassigned-open task via the
	// shared reviewer queue — but only when the queue is included.
	bob := identity.Identity{Org: "demo", Workspace: "main", Actor: "bob"}
	queue, err := notifications.List(ctx, s, bob, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || !strings.Contains(queue[0].Snippet, "New review task") {
		t.Fatalf("reviewer should see the queued open task, got %+v", queue)
	}
	if no, _ := notifications.List(ctx, s, bob, false); len(no) != 0 {
		t.Fatalf("a non-reviewer must not see the queue: %+v", no)
	}
}
