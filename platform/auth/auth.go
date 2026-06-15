// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth provides MVP authentication: API keys (sandbox/production scopes)
// for the data/decision APIs and a minimal session for the builder UI. Every
// authenticated request carries an org/workspace-scoped identity.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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

// APIKey binds a secret to a tenant-scoped identity and a scope.
type APIKey struct {
	ID       string
	Identity identity.Identity
	Scope    Scope
}

// Keyring resolves API-key secrets to identities. Secrets are stored hashed.
type Keyring struct {
	mu   sync.RWMutex
	keys map[string]APIKey // sha256(secret) -> key
}

// NewKeyring returns an empty keyring.
func NewKeyring() *Keyring { return &Keyring{keys: make(map[string]APIKey)} }

// Add registers a secret with its identity/scope.
func (k *Keyring) Add(secret string, key APIKey) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys[hash(secret)] = key
}

// Resolve looks up a presented secret in constant-ish time.
func (k *Keyring) Resolve(secret string) (APIKey, bool) {
	h := hash(secret)
	k.mu.RLock()
	defer k.mu.RUnlock()
	for stored, key := range k.keys {
		if subtle.ConstantTimeCompare([]byte(stored), []byte(h)) == 1 {
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

// Issue creates a session token for id, valid for the TTL.
func (s *Sessions) Issue(id identity.Identity) string {
	tok := newToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[tok] = session{id: id, expires: s.now().Add(s.ttl)}
	return tok
}

// Resolve returns the identity for a token, treating an expired one as absent.
func (s *Sessions) Resolve(tok string) (identity.Identity, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[tok]
	if !ok || s.now().After(sess.expires) {
		return identity.Identity{}, false
	}
	return sess.id, true
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
