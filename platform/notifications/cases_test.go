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
	mine, err := notifications.List(ctx, s, alice, notifications.Access{})
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
	queue, err := notifications.List(ctx, s, bob, notifications.Access{ReviewTasks: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || !strings.Contains(queue[0].Snippet, "New review task") {
		t.Fatalf("reviewer should see the queued open task, got %+v", queue)
	}
	if !strings.HasPrefix(queue[0].NotificationID, "bob:") || queue[0].Recipient != "bob" {
		t.Fatalf("shared task was not personalized for independent read state: %+v", queue[0])
	}
	if _, err := notifications.NewHandler(log).MarkRead(ctx, bob, queue[0].NotificationID); err != nil {
		t.Fatal(err)
	}
	replayed := store.NewMemory()
	if err := projection.New(log, replayed, notifications.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := notifications.List(ctx, replayed, bob, notifications.Access{ReviewTasks: true})
	if err != nil || len(after) != 1 || !after[0].Read {
		t.Fatalf("personal shared-task read receipt did not replay: %+v err=%v", after, err)
	}
	other, err := notifications.List(ctx, replayed,
		identity.Identity{Org: "demo", Workspace: "main", Actor: "carol"},
		notifications.Access{ReviewTasks: true})
	if err != nil || len(other) != 1 || other[0].Read {
		t.Fatalf("bob's read receipt hid the task from carol: %+v err=%v", other, err)
	}
	if no, _ := notifications.List(ctx, s, bob, notifications.Access{}); len(no) != 0 {
		t.Fatalf("a non-reviewer must not see the queue: %+v", no)
	}
}
