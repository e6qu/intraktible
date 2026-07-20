// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth provides MVP authentication: API keys (sandbox/production scopes)
// for the data/decision APIs and a minimal session for the builder UI. Every
// authenticated request carries an org/workspace-scoped identity.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
)

// DefaultSessionTTL is how long an issued session stays valid.
const DefaultSessionTTL = 24 * time.Hour

// Scope restricts which decision environments an API key may call. Beyond the two
// concrete environments it supports a wildcard ("*" = any environment) and a
// trailing-"/*" prefix pattern, so a key can be bound to one environment, all of
// them, or a family — the model generalises as more environments are added.
type Scope string

const (
	Sandbox    Scope = "sandbox"
	Production Scope = "production"
	ScopeAll   Scope = "*" // any environment
)

// Allows reports whether a key with this scope may call the given environment. An
// empty scope grants NOTHING (fail closed): key creation defaults an empty scope to
// Sandbox (see APIKeys.Define), so a scopeless scope at this point means corruption
// or a pre-scoping legacy key — denying it is safer than the old fail-open behaviour
// that would silently grant every environment.
func (s Scope) Allows(env string) bool {
	p := string(s)
	switch {
	case p == "":
		return false
	case p == string(ScopeAll):
		return true
	case strings.HasSuffix(p, "/*"):
		return strings.HasPrefix(env, strings.TrimSuffix(p, "*"))
	default:
		return p == env
	}
}

// ValidScope reports whether s is an acceptable scope value to store on a key.
func ValidScope(s Scope) bool {
	return s == Sandbox || s == Production || s == ScopeAll || strings.HasSuffix(string(s), "/*")
}

// Covers reports whether this scope is a ceiling for other: every environment other
// permits, s permits too. It gates credential minting/rotation so a caller cannot
// create or re-secret a key broader than their own scope (a sandbox-scoped admin
// must not mint a production key). "*" covers everything; a "foo/*" prefix pattern
// covers concrete envs and sub-patterns beneath it; otherwise the scopes must match.
func (s Scope) Covers(other Scope) bool {
	switch {
	case s == ScopeAll:
		return true
	case s == other:
		return true
	case strings.HasSuffix(string(s), "/*"):
		return strings.HasPrefix(string(other), strings.TrimSuffix(string(s), "*"))
	default:
		return false
	}
}

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
	validate SessionValidator
	logout   map[string]time.Time
}

type session struct {
	id      identity.Identity
	role    Role
	scope   Scope
	expires time.Time
	sso     bool
	ssoData SSOSession
}

// NewSessions returns an empty session store using DefaultSessionTTL.
func NewSessions() *Sessions {
	return &Sessions{
		sessions: make(map[string]session),
		ttl:      DefaultSessionTTL,
		now:      time.Now,
		logout:   make(map[string]time.Time),
	}
}

// TTL returns the session lifetime (used to align the cookie's max-age).
func (s *Sessions) TTL() time.Duration { return s.ttl }

// Issue creates a session token for id with role and scope, valid for the TTL. The
// scope is carried so a session minted from a scoped API key cannot silently widen
// to every environment (see SessionStore). The in-memory store never fails; the
// error is part of the SessionStore contract.
func (s *Sessions) Issue(id identity.Identity, role Role, scope Scope) (string, error) {
	return s.issue(id, role, scope, false, SSOSession{})
}

// IssueSSO is Issue for an SSO session, which Resolve revalidates each time.
func (s *Sessions) IssueSSO(id identity.Identity, role Role, scope Scope, sso SSOSession) (string, error) {
	return s.issue(id, role, scope, true, sso)
}

func (s *Sessions) issue(id identity.Identity, role Role, scope Scope, sso bool, ssoData SSOSession) (string, error) {
	tok := newToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[tok] = session{id: id, role: role, scope: scope, expires: s.now().Add(s.ttl), sso: sso, ssoData: ssoData}
	return tok, nil
}

// SSOSession returns the identity-provider state bound to a current SSO session.
func (s *Sessions) SSOSession(tok string) (SSOSession, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[tok]
	if !ok || s.now().After(sess.expires) {
		return SSOSession{}, false, nil
	}
	return sess.ssoData, sess.sso, nil
}

// RevokeWithSSO atomically removes tok and returns its provider coordinates.
func (s *Sessions) RevokeWithSSO(tok string) (SSOSession, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[tok]
	if !ok {
		return SSOSession{}, false, nil
	}
	delete(s.sessions, tok)
	if s.now().After(sess.expires) || !sess.sso {
		return SSOSession{}, false, nil
	}
	return sess.ssoData, true, nil
}

// RevokeOIDCFrontChannelSessions removes the session named by the provider's
// issuer-bound sid.
func (s *Sessions) RevokeOIDCFrontChannelSessions(provider, issuer, sid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, sess := range s.sessions {
		data := sess.ssoData
		if sess.sso && data.Protocol == "oidc" && data.Provider == provider && data.Issuer == issuer && data.SID == sid {
			delete(s.sessions, token)
		}
	}
	return nil
}

// RevokeOIDCSessions removes the session identified by sid, or every session
// for subject when sid is absent, after a verified Back-Channel Logout request.
func (s *Sessions) RevokeOIDCSessions(provider, issuer, clientID, jti, sid, subject string, replayExpires time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for key, expires := range s.logout {
		if !expires.After(now) {
			delete(s.logout, key)
		}
	}
	key := oidcLogoutTokenKey(provider, issuer, clientID, jti)
	if _, replayed := s.logout[key]; replayed {
		return false, nil
	}
	for token, sess := range s.sessions {
		data := sess.ssoData
		if !sess.sso || data.Protocol != "oidc" || data.Provider != provider || data.Issuer != issuer {
			continue
		}
		matches := sid != "" && data.SID == sid
		if sid == "" {
			matches = subject != "" && data.Subject == subject
		}
		if matches {
			delete(s.sessions, token)
		}
	}
	s.logout[key] = replayExpires
	return true, nil
}

// SetValidator installs the per-Resolve SSO revalidation predicate.
func (s *Sessions) SetValidator(v SessionValidator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.validate = v
}

// Resolve returns the identity + role + scope for a token, treating an expired one
// — or an SSO session the validator now rejects (e.g. SCIM-deactivated) — as absent.
func (s *Sessions) Resolve(tok string) (identity.Identity, Role, Scope, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[tok]
	validate := s.validate
	s.mu.RUnlock()
	if !ok || s.now().After(sess.expires) {
		return identity.Identity{}, "", "", false
	}
	if sess.sso && validate != nil && !validate(sess.id) {
		return identity.Identity{}, "", "", false
	}
	return sess.id, sess.role, sess.scope, true
}

// Revoke invalidates a session token (logout); unknown tokens are a no-op.
func (s *Sessions) Revoke(tok string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tok)
	return nil
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newToken() string {
	var b [24]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("auth: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
