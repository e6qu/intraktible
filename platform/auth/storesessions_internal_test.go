// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"path/filepath"
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
	_ = s.Revoke(tok2)
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
	old2, _ := s.IssueSSO(id, RoleEditor, ScopeAll, SSOSession{})
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

			ssoTok, _ := s.IssueSSO(ada, RoleEditor, ScopeAll, SSOSession{})
			keyTok, _ := s.Issue(ada, RoleEditor, Production) // non-SSO, never revalidated
			okTok, _ := s.IssueSSO(grace, RoleEditor, ScopeAll, SSOSession{})

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

func TestSSOSessionRoundTrips(t *testing.T) {
	stores := map[string]SessionStore{
		"memory": NewSessions(),
		"store":  NewStoreSessions(store.NewMemory()),
	}
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			want := SSOSession{Protocol: "oidc", Provider: "shauth", Issuer: "https://auth.example.test", Subject: "subject", SID: "sid", IDToken: "signed.id.token", ClientID: "intraktible", EndSessionEndpoint: "https://auth.example.test/oauth2/sessions/logout", PostLogoutRedirectURL: "https://intraktible.example.test/v1/auth/signed-out"}
			token, err := s.IssueSSO(identity.Identity{Org: "o", Workspace: "w", Actor: "a"}, RoleViewer, ScopeAll, want)
			if err != nil {
				t.Fatalf("issue SSO session: %v", err)
			}
			if got, ok, err := s.SSOSession(token); err != nil || !ok || got != want {
				t.Fatalf("SSO session = %#v ok=%v, want %#v", got, ok, want)
			}
			_ = s.Revoke(token)
			if got, ok, err := s.SSOSession(token); err != nil || ok || got != (SSOSession{}) {
				t.Fatalf("revoked SSO session = %#v ok=%v", got, ok)
			}
		})
	}
}

func TestRevokeWithSSOReturnsMetadataAfterRemovingSession(t *testing.T) {
	stores := map[string]SessionStore{
		"memory": NewSessions(),
		"store":  NewStoreSessions(store.NewMemory()),
	}
	for name, sessions := range stores {
		t.Run(name, func(t *testing.T) {
			want := SSOSession{
				Protocol: "oidc", Provider: "shauth", Issuer: "https://auth.example.test",
				Subject: "subject", SID: "sid", ClientID: "intraktible",
				EndSessionEndpoint:    "https://auth.example.test/oauth2/sessions/logout",
				PostLogoutRedirectURL: "https://intraktible.example.test/v1/auth/signed-out",
			}
			token, err := sessions.IssueSSO(identity.Identity{Org: "o", Workspace: "w", Actor: "a"}, RoleViewer, ScopeAll, want)
			if err != nil {
				t.Fatal(err)
			}
			got, ok, err := sessions.RevokeWithSSO(token)
			if err != nil || !ok || got != want {
				t.Fatalf("RevokeWithSSO = %#v ok=%v err=%v, want %#v", got, ok, err, want)
			}
			if _, _, _, ok := sessions.Resolve(token); ok {
				t.Fatal("RevokeWithSSO returned before removing the session")
			}
			if got, ok, err := sessions.RevokeWithSSO(token); err != nil || ok || got != (SSOSession{}) {
				t.Fatalf("idempotent RevokeWithSSO = %#v ok=%v err=%v", got, ok, err)
			}
		})
	}
}

func TestRevokeOIDCFrontChannelSessionsScopesByProviderIssuerAndSID(t *testing.T) {
	stores := map[string]SessionStore{
		"memory": NewSessions(),
		"store":  NewStoreSessions(store.NewMemory()),
	}
	for name, sessions := range stores {
		t.Run(name, func(t *testing.T) {
			id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}
			base := SSOSession{Protocol: "oidc", Provider: "shauth", Issuer: "https://auth.example.test", Subject: "subject", SID: "sid-1"}
			matched, _ := sessions.IssueSSO(id, RoleViewer, ScopeAll, base)
			otherSID := base
			otherSID.SID = "sid-2"
			otherSIDToken, _ := sessions.IssueSSO(id, RoleViewer, ScopeAll, otherSID)
			otherProvider := base
			otherProvider.Provider = "other"
			otherProviderToken, _ := sessions.IssueSSO(id, RoleViewer, ScopeAll, otherProvider)

			if err := sessions.RevokeOIDCFrontChannelSessions("shauth", base.Issuer, base.SID); err != nil {
				t.Fatal(err)
			}
			if _, _, _, ok := sessions.Resolve(matched); ok {
				t.Fatal("front-channel sid-matched session remained")
			}
			if _, _, _, ok := sessions.Resolve(otherSIDToken); !ok {
				t.Fatal("front-channel logout revoked a different sid")
			}
			if _, _, _, ok := sessions.Resolve(otherProviderToken); !ok {
				t.Fatal("front-channel logout revoked a different provider")
			}
		})
	}
}

func TestRevokeOIDCSessionsScopesByProviderIssuerSIDAndSubject(t *testing.T) {
	stores := map[string]SessionStore{
		"memory": NewSessions(),
		"store":  NewStoreSessions(store.NewMemory()),
	}
	for name, sessions := range stores {
		t.Run(name, func(t *testing.T) {
			id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}
			base := SSOSession{Protocol: "oidc", Provider: "shauth", Issuer: "https://auth.example.test", Subject: "subject"}
			first := base
			first.SID = "sid-1"
			second := base
			second.SID = "sid-2"
			other := base
			other.Provider = "other"
			firstToken, _ := sessions.IssueSSO(id, RoleViewer, ScopeAll, first)
			secondToken, _ := sessions.IssueSSO(id, RoleViewer, ScopeAll, second)
			otherToken, _ := sessions.IssueSSO(id, RoleViewer, ScopeAll, other)
			if accepted, err := sessions.RevokeOIDCSessions("shauth", base.Issuer, "client", "jti-1", "sid-1", base.Subject, time.Now().Add(time.Hour)); err != nil || !accepted {
				t.Fatal(err)
			}
			if _, _, _, ok := sessions.Resolve(firstToken); ok {
				t.Fatal("sid-matched session remained")
			}
			if _, _, _, ok := sessions.Resolve(secondToken); !ok {
				t.Fatal("unrelated sid session was revoked")
			}
			if accepted, err := sessions.RevokeOIDCSessions("shauth", base.Issuer, "client", "jti-2", "", base.Subject, time.Now().Add(time.Hour)); err != nil || !accepted {
				t.Fatal(err)
			}
			if _, _, _, ok := sessions.Resolve(secondToken); ok {
				t.Fatal("subject-matched session remained")
			}
			if _, _, _, ok := sessions.Resolve(otherToken); !ok {
				t.Fatal("different provider session was revoked")
			}
		})
	}
}

func TestStoreSessionsReadsLegacyTopLevelLogoutURL(t *testing.T) {
	st := store.NewMemory()
	const token = "legacy-session-token"
	const logoutURL = "https://auth.example.test/logout"
	legacy := map[string]any{
		"identity":   identity.Identity{Org: "o", Workspace: "w", Actor: "a"},
		"role":       RoleViewer,
		"scope":      ScopeAll,
		"expires":    time.Now().Add(time.Hour),
		"sso":        true,
		"logout_url": logoutURL,
	}
	if err := store.PutDoc(context.Background(), st, sessionCollection, hash(token), legacy); err != nil {
		t.Fatal(err)
	}
	sessions := NewStoreSessions(st)
	sso, ok, err := sessions.SSOSession(token)
	if err != nil || !ok || sso.LogoutURL != logoutURL {
		t.Fatalf("legacy SSO metadata = %#v ok=%v err=%v", sso, ok, err)
	}
}

func TestOIDCLogoutReplayClaimSurvivesStoreRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := store.NewSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}
	sso := SSOSession{Protocol: "oidc", Provider: "shauth", Issuer: "https://auth.example.test/", Subject: "subject", SID: "sid"}
	sessions := NewStoreSessions(st)
	first, _ := sessions.IssueSSO(id, RoleViewer, ScopeAll, sso)
	expires := time.Now().Add(time.Hour)
	accepted, err := sessions.RevokeOIDCSessions("shauth", sso.Issuer, "client", "jti", sso.SID, sso.Subject, expires)
	if err != nil || !accepted {
		t.Fatalf("first logout accepted=%v err=%v", accepted, err)
	}
	if _, _, _, ok := sessions.Resolve(first); ok {
		t.Fatal("first matching session remained")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = store.NewSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sessions = NewStoreSessions(st)
	newSession, _ := sessions.IssueSSO(id, RoleViewer, ScopeAll, sso)
	accepted, err = sessions.RevokeOIDCSessions("shauth", sso.Issuer, "client", "jti", sso.SID, sso.Subject, expires)
	if err != nil || accepted {
		t.Fatalf("replayed logout accepted=%v err=%v", accepted, err)
	}
	if _, _, _, ok := sessions.Resolve(newSession); !ok {
		t.Fatal("replayed logout revoked a session created after the first logout")
	}
}
