// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/e6qu/intraktible/platform/comments"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/notifications"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestParseMentions(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"hey @alice and @bob, also @alice again", []string{"alice", "bob"}},
		{"no mentions here", []string{}},
		{"email a@b.com is not a mention", []string{}},
		{"@lead start-of-line works", []string{"lead"}},
	}
	for _, c := range cases {
		if got := notifications.ParseMentions(c.body); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("ParseMentions(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}

func TestInboxFromMentions(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	author := identity.Identity{Org: "demo", Workspace: "main", Actor: "author"}
	reviewer := identity.Identity{Org: "demo", Workspace: "main", Actor: "reviewer"}

	ch := comments.NewHandler(log)
	// Author mentions the reviewer and themselves; the self-mention is not notified.
	cid, _, err := ch.Post(ctx, author, comments.Subject{Type: "policy", ID: "p1"}, "please review @reviewer — cc @author", "")
	if err != nil {
		t.Fatal(err)
	}

	project := func() store.Store {
		s := store.NewMemory()
		if err := projection.New(log, s, comments.Projector{}, notifications.Projector{}).Start(ctx); err != nil {
			t.Fatal(err)
		}
		return s
	}

	s := project()
	mine, err := notifications.List(ctx, s, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].Read || mine[0].SubjectID != "p1" || mine[0].Author != "author" {
		t.Fatalf("reviewer inbox wrong: %+v", mine)
	}
	if authorInbox, _ := notifications.List(ctx, s, author); len(authorInbox) != 0 {
		t.Fatalf("self-mention should not notify the author: %+v", authorInbox)
	}

	// The reviewer marks it read; a fresh projection reflects it.
	nh := notifications.NewHandler(log)
	if _, err := nh.MarkRead(ctx, reviewer, mine[0].NotificationID); err != nil {
		t.Fatal(err)
	}
	// Another user cannot mark the reviewer's notification read.
	if _, err := nh.MarkRead(ctx, author, mine[0].NotificationID); err == nil {
		t.Fatal("a non-recipient must not be able to mark the notification read")
	}

	after, _ := notifications.List(ctx, project(), reviewer)
	if len(after) != 1 || !after[0].Read {
		t.Fatalf("notification not marked read: %+v", after)
	}
	_ = cid
}
