// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"testing"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
)

// White-box: drive the session clock to exercise expiry and revoke.
func TestSessionExpiryAndRevoke(t *testing.T) {
	s := NewSessions()
	clock := time.Now()
	s.now = func() time.Time { return clock }
	s.ttl = time.Hour
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}

	tok := s.Issue(id)
	if _, ok := s.Resolve(tok); !ok {
		t.Fatal("a fresh session should resolve")
	}

	clock = clock.Add(2 * time.Hour) // past the TTL
	if _, ok := s.Resolve(tok); ok {
		t.Fatal("an expired session should not resolve")
	}

	tok2 := s.Issue(id) // issued at the advanced clock, still valid
	if _, ok := s.Resolve(tok2); !ok {
		t.Fatal("a freshly issued session should resolve")
	}
	s.Revoke(tok2)
	if _, ok := s.Resolve(tok2); ok {
		t.Fatal("a revoked session should not resolve")
	}
}
