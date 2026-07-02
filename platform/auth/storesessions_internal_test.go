// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
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

	tok, _ := s.Issue(id, RoleEditor, Production)
	got, _, scope, ok := s.Resolve(tok)
	if !ok || got != id {
		t.Fatalf("resolve fresh session: got=%v ok=%v", got, ok)
	}
	if scope != Production {
		t.Fatalf("session must round-trip its scope: got %q, want production", scope)
	}

	// Durability: a NEW instance over the same store resolves the session — what
	// makes sessions survive a restart when the store is durable.
	s2 := NewStoreSessions(st)
	s2.now = func() time.Time { return clock }
	if _, _, _, ok := s2.Resolve(tok); !ok {
		t.Fatal("session should be readable from a second store-backed instance")
	}

	// Expiry.
	clock = clock.Add(2 * time.Hour)
	if _, _, _, ok := s.Resolve(tok); ok {
		t.Fatal("expired session should not resolve")
	}

	// Revoke.
	tok2, _ := s.Issue(id, RoleEditor, Sandbox)
	if _, _, _, ok := s.Resolve(tok2); !ok {
		t.Fatal("fresh session should resolve")
	}
	s.Revoke(tok2)
	if _, _, _, ok := s.Resolve(tok2); ok {
		t.Fatal("revoked session should not resolve")
	}
}

// TestResolveDeletesExpiredSession: an expired row can never resolve again, so
// Resolve deletes it instead of leaving it to accumulate (previously only
// Revoke deleted rows, so auth_sessions grew without bound).
func TestResolveDeletesExpiredSession(t *testing.T) {
	st := store.NewMemory()
	clock := time.Now()
	s := NewStoreSessions(st)
	s.now = func() time.Time { return clock }
	s.ttl = time.Hour
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}

	tok, _ := s.Issue(id, RoleEditor, Production)
	clock = clock.Add(2 * time.Hour)
	if _, _, _, ok := s.Resolve(tok); ok {
		t.Fatal("expired session should not resolve")
	}
	if _, ok, _ := st.Get(context.Background(), sessionCollection, hash(tok)); ok {
		t.Fatal("resolving an expired session should delete its row")
	}
}

// TestIssueSweepsExpiredSessions: issuing a session opportunistically prunes
// previously-expired rows, so tokens that are never presented again still get
// cleaned up.
func TestIssueSweepsExpiredSessions(t *testing.T) {
	st := store.NewMemory()
	clock := time.Now()
	s := NewStoreSessions(st)
	s.now = func() time.Time { return clock }
	s.ttl = time.Hour
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}

	old1, _ := s.Issue(id, RoleEditor, Production)
	old2, _ := s.IssueSSO(id, RoleEditor, ScopeAll)
	clock = clock.Add(2 * time.Hour)

	fresh, _ := s.Issue(id, RoleEditor, Production)
	for _, tok := range []string{old1, old2} {
		if _, ok, _ := st.Get(context.Background(), sessionCollection, hash(tok)); ok {
			t.Fatal("issue should sweep previously-expired session rows")
		}
	}
	if _, _, _, ok := s.Resolve(fresh); !ok {
		t.Fatal("the freshly issued session must survive the sweep")
	}
	recs, _ := st.List(context.Background(), sessionCollection, "")
	if len(recs) != 1 {
		t.Fatalf("auth_sessions rows = %d, want 1 (the fresh session)", len(recs))
	}
}

// TestSessionValidatorRevokesSSO proves an SSO session stops resolving once the
// validator (e.g. the SCIM deprovisioning gate) rejects its user, while non-SSO
// sessions and SSO sessions of still-valid users are untouched — across both stores.
func TestSessionValidatorRevokesSSO(t *testing.T) {
	stores := map[string]SessionStore{
		"memory": NewSessions(),
		"store":  NewStoreSessions(store.NewMemory()),
	}
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			deactivated := map[string]bool{}
			s.SetValidator(func(id identity.Identity) bool { return !deactivated[id.Actor] })
			ada := identity.Identity{Org: "o", Workspace: "w", Actor: "ada"}
			grace := identity.Identity{Org: "o", Workspace: "w", Actor: "grace"}

			ssoTok, _ := s.IssueSSO(ada, RoleEditor, ScopeAll)
			keyTok, _ := s.Issue(ada, RoleEditor, Production) // non-SSO, never revalidated
			okTok, _ := s.IssueSSO(grace, RoleEditor, ScopeAll)

			if _, _, _, ok := s.Resolve(ssoTok); !ok {
				t.Fatal("active SSO user should resolve")
			}

			deactivated["ada"] = true
			if _, _, _, ok := s.Resolve(ssoTok); ok {
				t.Fatal("deactivated SSO user's session must not resolve")
			}
			if _, _, _, ok := s.Resolve(keyTok); !ok {
				t.Fatal("non-SSO session must be unaffected by the SSO validator")
			}
			if _, _, _, ok := s.Resolve(okTok); !ok {
				t.Fatal("still-active SSO user's session should resolve")
			}
		})
	}
}
