// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
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
	Issue(id identity.Identity, role Role) (string, error)
	Resolve(tok string) (identity.Identity, Role, bool)
	Revoke(tok string)
	TTL() time.Duration // session lifetime, used to align the cookie max-age
}

// sessionCollection holds session documents (keyed by the hashed token).
const sessionCollection = "auth_sessions"

type storedSession struct {
	Identity identity.Identity `json:"identity"`
	Role     Role              `json:"role,omitempty"`
	Expires  time.Time         `json:"expires"`
}

// StoreSessions persists sessions in a store.Store, so they survive a restart when
// the store is durable (e.g. SQLite). Tokens are stored hashed. It is NOT a
// projection — a projection rebuild never touches this collection.
type StoreSessions struct {
	store store.Store
	ttl   time.Duration
	now   func() time.Time
}

// NewStoreSessions builds a store-backed session store using DefaultSessionTTL.
func NewStoreSessions(s store.Store) *StoreSessions {
	return &StoreSessions{store: s, ttl: DefaultSessionTTL, now: time.Now}
}

// TTL returns the session lifetime.
func (s *StoreSessions) TTL() time.Duration { return s.ttl }

// Issue creates a session token for id, valid for the TTL. A persist failure is
// returned so the caller can fail the login loudly rather than hand back a token
// that will never resolve.
func (s *StoreSessions) Issue(id identity.Identity, role Role) (string, error) {
	tok := newToken()
	rec := storedSession{Identity: id, Role: role, Expires: s.now().Add(s.ttl)}
	if err := store.PutDoc(context.Background(), s.store, sessionCollection, hash(tok), rec); err != nil {
		return "", fmt.Errorf("auth: persist session: %w", err)
	}
	return tok, nil
}

// Resolve returns the identity + role for a token, treating an expired/missing one
// — or a store error — as not authenticated.
func (s *StoreSessions) Resolve(tok string) (identity.Identity, Role, bool) {
	rec, ok, err := store.GetDoc[storedSession](context.Background(), s.store, sessionCollection, hash(tok))
	if err != nil {
		slog.Error("auth: resolve session failed", "err", err)
		return identity.Identity{}, "", false
	}
	if !ok || s.now().After(rec.Expires) {
		return identity.Identity{}, "", false
	}
	return rec.Identity, rec.Role, true
}

// Revoke invalidates a session token (logout); unknown tokens are a no-op.
func (s *StoreSessions) Revoke(tok string) {
	if err := s.store.Delete(context.Background(), sessionCollection, hash(tok)); err != nil {
		slog.Error("auth: revoke session failed", "err", err)
	}
}
