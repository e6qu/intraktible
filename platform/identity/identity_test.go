// SPDX-License-Identifier: AGPL-3.0-or-later

package identity_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/platform/identity"
)

func TestValid(t *testing.T) {
	cases := []struct {
		name string
		id   identity.Identity
		ok   bool
	}{
		{"full", identity.Identity{Org: "o", Workspace: "w", Actor: "a"}, true},
		{"missing org", identity.Identity{Workspace: "w", Actor: "a"}, false},
		{"missing workspace", identity.Identity{Org: "o", Actor: "a"}, false},
		{"missing actor", identity.Identity{Org: "o", Workspace: "w"}, false},
		{"empty", identity.Identity{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.id.Valid()
			if c.ok && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestNew(t *testing.T) {
	id, err := identity.New("o", "w", "a")
	if err != nil || id.Org != "o" || id.Workspace != "w" || id.Actor != "a" {
		t.Fatalf("New valid = (%+v, %v)", id, err)
	}
	if _, err := identity.New("", "w", "a"); err == nil {
		t.Fatal("New with empty org should error")
	}
	if _, err := identity.New("o/x", "w", "a"); err == nil {
		t.Fatal("New with '/' in org should error")
	}
}

func TestContextRoundTrip(t *testing.T) {
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}
	ctx := identity.With(context.Background(), id)
	got, ok := identity.From(ctx)
	if !ok || got != id {
		t.Fatalf("From: got %+v ok=%v, want %+v true", got, ok, id)
	}
	if _, ok := identity.From(context.Background()); ok {
		t.Fatal("From on bare context should report ok=false")
	}
}
