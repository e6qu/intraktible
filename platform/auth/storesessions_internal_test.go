// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Both session implementations satisfy the interface the handlers depend on.
var (
	_ SessionStore = (*Sessions)(nil)
	_ SessionStore = (*StoreSessions)(nil)
)

func TestStoreSessions(t *testing.T) {
	st := store.NewMemory()
	clock := time.Now()
	s := NewStoreSessions(st)
	s.now = func() time.Time { return clock }
	s.ttl = time.Hour
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}

	tok := s.Issue(id, RoleEditor)
	if got, _, ok := s.Resolve(tok); !ok || got != id {
		t.Fatalf("resolve fresh session: got=%v ok=%v", got, ok)
	}

	// Durability: a NEW instance over the same store resolves the session — what
	// makes sessions survive a restart when the store is durable.
	s2 := NewStoreSessions(st)
	s2.now = func() time.Time { return clock }
	if _, _, ok := s2.Resolve(tok); !ok {
		t.Fatal("session should be readable from a second store-backed instance")
	}

	// Expiry.
	clock = clock.Add(2 * time.Hour)
	if _, _, ok := s.Resolve(tok); ok {
		t.Fatal("expired session should not resolve")
	}

	// Revoke.
	tok2 := s.Issue(id, RoleEditor)
	if _, _, ok := s.Resolve(tok2); !ok {
		t.Fatal("fresh session should resolve")
	}
	s.Revoke(tok2)
	if _, _, ok := s.Resolve(tok2); ok {
		t.Fatal("revoked session should not resolve")
	}
}
