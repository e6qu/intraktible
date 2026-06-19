// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth provides MVP authentication: API keys (sandbox/production scopes)
// for the data/decision APIs and a minimal session for the builder UI. Every
// authenticated request carries an org/workspace-scoped identity.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
)

// DefaultSessionTTL is how long an issued session stays valid.
const DefaultSessionTTL = 24 * time.Hour

// Scope distinguishes sandbox from production API keys.
type Scope string

const (
	Sandbox    Scope = "sandbox"
	Production Scope = "production"
)

// Role is the authorization level of an authenticated principal. Roles are
// ordered — each includes the capabilities of the ones below it:
// viewer < operator < editor < approver < admin.
type Role string

const (
	RoleViewer   Role = "viewer"   // read everything
	RoleOperator Role = "operator" // + run the platform: /decide, cases, agent runs, context ingest
	RoleEditor   Role = "editor"   // + author flows/agents/connectors/features
	RoleApprover Role = "approver" // + approve & deploy versions (the maker-checker checker)
	RoleAdmin    Role = "admin"    // everything
)

var roleRank = map[Role]int{
	RoleViewer: 1, RoleOperator: 2, RoleEditor: 3, RoleApprover: 4, RoleAdmin: 5,
}

// Rank returns the role's level (0 for empty/unknown).
func (r Role) Rank() int { return roleRank[r] }

// AtLeast reports whether r meets or exceeds the required role.
func (r Role) AtLeast(want Role) bool { return r.Rank() > 0 && r.Rank() >= want.Rank() }

// ParseRole maps a string to a Role, defaulting to viewer for empty/unknown input.
func ParseRole(s string) Role {
	if r := Role(s); r.Rank() > 0 {
		return r
	}
	return RoleViewer
}

// APIKey binds a secret to a tenant-scoped identity, a scope, and a role.
type APIKey struct {
	ID       string
	Identity identity.Identity
	Scope    Scope
	Role     Role
}

// Keyring resolves API-key secrets to identities. Secrets are stored hashed.
type Keyring struct {
	mu        sync.RWMutex
	keys      map[string]APIKey // sha256(secret) -> key
	resolvers []KeyResolver
}

// NewKeyring returns an empty keyring.
func NewKeyring() *Keyring { return &Keyring{keys: make(map[string]APIKey)} }

// Add registers a secret with its identity/scope.
func (k *Keyring) Add(secret string, key APIKey) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys[hash(secret)] = key
}

// KeyResolver resolves API-key secrets outside the static in-memory keyring.
type KeyResolver interface {
	ResolveSecret(secret string) (APIKey, bool)
}

// UseResolver adds a secondary resolver, such as the durable managed-token store.
func (k *Keyring) UseResolver(resolver KeyResolver) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.resolvers = append(k.resolvers, resolver)
}

// Resolve looks up a presented secret. The key is the SHA-256 of the secret, so a
// direct map lookup is both O(1) and constant-time in the number of registered
// keys — the prior linear scan with a per-entry constant-time compare leaked the
// keyring size through timing while adding no real protection (the lookup key is
// already a fixed-width hash, not the secret).
func (k *Keyring) Resolve(secret string) (APIKey, bool) {
	h := hash(secret)
	k.mu.RLock()
	key, ok := k.keys[h]
	k.mu.RUnlock()
	if ok {
		return key, true
	}
	for _, resolver := range k.resolvers {
		if key, ok := resolver.ResolveSecret(secret); ok {
			return key, true
		}
	}
	return APIKey{}, false
}

// Sessions is a minimal in-memory session store for the builder UI: tokens map to
// tenant identities and expire after a TTL.
type Sessions struct {
	mu       sync.RWMutex
	sessions map[string]session
	ttl      time.Duration
	now      func() time.Time
}

type session struct {
	id      identity.Identity
	role    Role
	expires time.Time
}

// NewSessions returns an empty session store using DefaultSessionTTL.
func NewSessions() *Sessions {
	return &Sessions{
		sessions: make(map[string]session),
		ttl:      DefaultSessionTTL,
		now:      time.Now,
	}
}

// TTL returns the session lifetime (used to align the cookie's max-age).
func (s *Sessions) TTL() time.Duration { return s.ttl }

// Issue creates a session token for id with role, valid for the TTL. The
// in-memory store never fails; the error is part of the SessionStore contract.
func (s *Sessions) Issue(id identity.Identity, role Role) (string, error) {
	tok := newToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[tok] = session{id: id, role: role, expires: s.now().Add(s.ttl)}
	return tok, nil
}

// Resolve returns the identity + role for a token, treating an expired one as absent.
func (s *Sessions) Resolve(tok string) (identity.Identity, Role, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[tok]
	if !ok || s.now().After(sess.expires) {
		return identity.Identity{}, "", false
	}
	return sess.id, sess.role, true
}

// Revoke invalidates a session token (logout); unknown tokens are a no-op.
func (s *Sessions) Revoke(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tok)
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
