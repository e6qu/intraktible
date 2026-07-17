// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// SessionStore issues, resolves, and revokes builder-UI sessions. Both the
// in-memory Sessions and the store-backed StoreSessions satisfy it, so the auth
// middleware and login handlers depend on the interface.
type SessionStore interface {
	Issue(id identity.Identity, role Role, scope Scope) (string, error)
	// IssueSSO is Issue for an SSO-authenticated session. Such a session is
	// revalidated on every Resolve against the store's validator (e.g. the SCIM
	// deprovisioning gate), so a deactivated or downgraded user loses access within
	// the request cycle instead of surviving until the TTL.
	IssueSSO(id identity.Identity, role Role, scope Scope, logoutURL string) (string, error)
	Resolve(tok string) (identity.Identity, Role, Scope, bool)
	// LogoutURL returns the front-channel identity-provider logout URL bound to an
	// SSO session. An empty result means the session has no provider logout step.
	LogoutURL(tok string) string
	Revoke(tok string)
	// SetValidator installs a predicate consulted on Resolve for SSO sessions; a
	// false result rejects the session. A nil validator (the default) accepts every
	// unexpired session. The validator must be safe for concurrent use.
	SetValidator(v SessionValidator)
	TTL() time.Duration // session lifetime, used to align the cookie max-age
}

// SessionValidator re-checks an SSO session's identity on each Resolve. It returns
// false when the user is no longer entitled (e.g. SCIM-deactivated), so the session
// is rejected even before its TTL elapses.
type SessionValidator func(id identity.Identity) bool

// sessionCollection holds session documents (keyed by the hashed token).
const sessionCollection = "auth_sessions"

type storedSession struct {
	Identity  identity.Identity `json:"identity"`
	Role      Role              `json:"role,omitempty"`
	Scope     Scope             `json:"scope,omitempty"`
	Expires   time.Time         `json:"expires"`
	SSO       bool              `json:"sso,omitempty"`
	LogoutURL string            `json:"logout_url,omitempty"`
}

// StoreSessions persists sessions in a store.Store, so they survive a restart when
// the store is durable (e.g. SQLite). Tokens are stored hashed. It is NOT a
// projection — a projection rebuild never touches this collection.
type StoreSessions struct {
	store    store.Store
	ttl      time.Duration
	now      func() time.Time
	validate SessionValidator
}

// NewStoreSessions builds a store-backed session store using DefaultSessionTTL.
func NewStoreSessions(s store.Store) *StoreSessions {
	return &StoreSessions{store: s, ttl: DefaultSessionTTL, now: time.Now}
}

// WithNow overrides the clock session issue/expiry reads (deterministic tests,
// the demo seeder) and returns the session store.
func (s *StoreSessions) WithNow(now func() time.Time) *StoreSessions {
	s.now = now
	return s
}

// TTL returns the session lifetime.
func (s *StoreSessions) TTL() time.Duration { return s.ttl }

// SetValidator installs the per-Resolve SSO revalidation predicate.
func (s *StoreSessions) SetValidator(v SessionValidator) { s.validate = v }

// Issue creates a session token for id, valid for the TTL. A persist failure is
// returned so the caller can fail the login loudly rather than hand back a token
// that will never resolve.
func (s *StoreSessions) Issue(id identity.Identity, role Role, scope Scope) (string, error) {
	return s.issue(id, role, scope, false, "")
}

// IssueSSO creates a session marked SSO, so Resolve revalidates it.
func (s *StoreSessions) IssueSSO(id identity.Identity, role Role, scope Scope, logoutURL string) (string, error) {
	return s.issue(id, role, scope, true, logoutURL)
}

func (s *StoreSessions) issue(id identity.Identity, role Role, scope Scope, sso bool, logoutURL string) (string, error) {
	s.sweepExpired()
	tok := newToken()
	rec := storedSession{Identity: id, Role: role, Scope: scope, Expires: s.now().Add(s.ttl), SSO: sso, LogoutURL: logoutURL}
	if err := store.PutDoc(context.Background(), s.store, sessionCollection, hash(tok), rec); err != nil {
		return "", fmt.Errorf("auth: persist session: %w", err)
	}
	return tok, nil
}

// Resolve returns the identity + role + scope for a token, treating an
// expired/missing one — or a store error — as not authenticated. A session
// persisted before scopes were stored resolves with an empty scope, which the
// environment gate treats as fail-closed (the holder re-authenticates). An SSO
// session whose user the validator now rejects (e.g. SCIM-deactivated) is treated
// as absent, so deprovisioning takes effect without waiting out the TTL.
func (s *StoreSessions) Resolve(tok string) (identity.Identity, Role, Scope, bool) {
	rec, ok, err := store.GetDoc[storedSession](context.Background(), s.store, sessionCollection, hash(tok))
	if err != nil {
		slog.Error("auth: resolve session failed", "err", err)
		return identity.Identity{}, "", "", false
	}
	if !ok {
		return identity.Identity{}, "", "", false
	}
	if s.now().After(rec.Expires) {
		// Best-effort cleanup: an expired row can never resolve again, and only
		// Revoke otherwise deletes rows — leaving it would grow the collection
		// unboundedly. A delete failure only defers the cleanup to the next sweep.
		if err := s.store.Delete(context.Background(), sessionCollection, hash(tok)); err != nil {
			slog.Error("auth: delete expired session failed", "err", err)
		}
		return identity.Identity{}, "", "", false
	}
	if rec.SSO && s.validate != nil && !s.validate(rec.Identity) {
		return identity.Identity{}, "", "", false
	}
	return rec.Identity, rec.Role, rec.Scope, true
}

// LogoutURL returns the identity-provider logout URL saved with a current
// session. It deliberately does not consult the SCIM validator: a deprovisioned
// user must still be able to complete identity-provider logout.
func (s *StoreSessions) LogoutURL(tok string) string {
	rec, ok, err := store.GetDoc[storedSession](context.Background(), s.store, sessionCollection, hash(tok))
	if err != nil {
		slog.Error("auth: resolve session logout URL failed", "err", err)
		return ""
	}
	if !ok || s.now().After(rec.Expires) {
		return ""
	}
	return rec.LogoutURL
}

// sweepExpired deletes expired session rows on each Issue, bounding the growth
// of auth_sessions (an expired row is otherwise only removed if its exact token
// is resolved or revoked). The store API offers no expiry index — only a full
// collection list — so this scans every session per login; the accepted
// tradeoff is that builder-UI sessions are low-volume and the sweep itself
// keeps the collection near the live-session count. Failures are logged, not
// returned: a login must not fail because cleanup did.
func (s *StoreSessions) sweepExpired() {
	ctx := context.Background()
	recs, err := s.store.List(ctx, sessionCollection, "")
	if err != nil {
		slog.Error("auth: sweep expired sessions failed", "err", err)
		return
	}
	now := s.now()
	for _, r := range recs {
		var rec storedSession
		if err := json.Unmarshal(r.Doc, &rec); err != nil {
			slog.Error("auth: corrupt session row", "err", err, "key", r.Key)
			continue
		}
		if now.After(rec.Expires) {
			if err := s.store.Delete(ctx, sessionCollection, r.Key); err != nil {
				slog.Error("auth: sweep expired session failed", "err", err, "key", r.Key)
			}
		}
	}
}

// Revoke invalidates a session token (logout); unknown tokens are a no-op.
func (s *StoreSessions) Revoke(tok string) {
	if err := s.store.Delete(context.Background(), sessionCollection, hash(tok)); err != nil {
		slog.Error("auth: revoke session failed", "err", err)
	}
}
