// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
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
	IssueSSO(id identity.Identity, role Role, scope Scope, sso SSOSession) (string, error)
	Resolve(tok string) (identity.Identity, Role, Scope, bool)
	SSOSession(tok string) (SSOSession, bool, error)
	// RevokeWithSSO atomically revokes tok and returns the identity-provider
	// coordinates that were bound to it. A successful return means the local
	// session is already unusable before the caller starts provider logout.
	RevokeWithSSO(tok string) (SSOSession, bool, error)
	RevokeOIDCFrontChannelSessions(provider, issuer, sid string) error
	RevokeOIDCSessions(provider, issuer, clientID, jti, sid, subject string, replayExpires time.Time) (bool, error)
	Revoke(tok string) error
	// SetValidator installs a predicate consulted on Resolve for SSO sessions; a
	// false result rejects the session. A nil validator (the default) accepts every
	// unexpired session. The validator must be safe for concurrent use.
	SetValidator(v SessionValidator)
	TTL() time.Duration // session lifetime, used to align the cookie max-age
}

// SSOSession is the server-side identity-provider state bound to one browser
// session. Browser cookies contain only the opaque Intraktible session token.
type SSOSession struct {
	Protocol              string `json:"protocol,omitempty"`
	Provider              string `json:"provider,omitempty"`
	Issuer                string `json:"issuer,omitempty"`
	Subject               string `json:"subject,omitempty"`
	SID                   string `json:"sid,omitempty"`
	IDToken               string `json:"id_token,omitempty"`
	ClientID              string `json:"client_id,omitempty"`
	EndSessionEndpoint    string `json:"end_session_endpoint,omitempty"`
	PostLogoutRedirectURL string `json:"post_logout_redirect_url,omitempty"`
	LogoutURL             string `json:"logout_url,omitempty"`
}

// SessionValidator re-checks an SSO session's identity on each Resolve. It returns
// false when the user is no longer entitled (e.g. SCIM-deactivated), so the session
// is rejected even before its TTL elapses.
type SessionValidator func(id identity.Identity) bool

// sessionCollection holds session documents (keyed by the hashed token).
const sessionCollection = "auth_sessions"

type storedSession struct {
	Identity identity.Identity `json:"identity"`
	Role     Role              `json:"role,omitempty"`
	Scope    Scope             `json:"scope,omitempty"`
	Expires  time.Time         `json:"expires"`
	SSO      bool              `json:"sso,omitempty"`
	SSOData  SSOSession        `json:"sso_data,omitempty"`
	// LogoutURL decodes the top-level field written before structured SSOData was
	// introduced. It is read only so an in-flight legacy session can still finish
	// identity-provider logout during a rolling upgrade.
	LogoutURL string `json:"logout_url,omitempty"`
}

type storedOIDCLogoutToken struct {
	Marker  string    `json:"marker"`
	Expires time.Time `json:"expires"`
}

const oidcLogoutReplayCollection = "auth_oidc_logout_replays"

// StoreSessions persists sessions in a store.Store, so they survive a restart when
// the store is durable (e.g. SQLite). Tokens are stored hashed. It is NOT a
// projection — a projection rebuild never touches this collection.
type StoreSessions struct {
	store    store.Store
	ttl      time.Duration
	now      func() time.Time
	validate SessionValidator
	logoutMu sync.Mutex
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
	return s.issue(id, role, scope, false, SSOSession{})
}

// IssueSSO creates a session marked SSO, so Resolve revalidates it.
func (s *StoreSessions) IssueSSO(id identity.Identity, role Role, scope Scope, sso SSOSession) (string, error) {
	return s.issue(id, role, scope, true, sso)
}

func (s *StoreSessions) issue(id identity.Identity, role Role, scope Scope, sso bool, ssoData SSOSession) (string, error) {
	s.sweepExpired()
	tok := newToken()
	rec := storedSession{Identity: id, Role: role, Scope: scope, Expires: s.now().Add(s.ttl), SSO: sso, SSOData: ssoData}
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

// SSOSession returns the identity-provider state saved with a current session.
// It deliberately does not consult the SCIM validator: a deprovisioned user
// must still be able to complete identity-provider logout.
func (s *StoreSessions) SSOSession(tok string) (SSOSession, bool, error) {
	rec, ok, err := store.GetDoc[storedSession](context.Background(), s.store, sessionCollection, hash(tok))
	if err != nil {
		return SSOSession{}, false, fmt.Errorf("auth: resolve SSO session: %w", err)
	}
	if !ok || s.now().After(rec.Expires) {
		return SSOSession{}, false, nil
	}
	if rec.SSOData == (SSOSession{}) && rec.LogoutURL != "" {
		return SSOSession{LogoutURL: rec.LogoutURL}, rec.SSO, nil
	}
	return rec.SSOData, rec.SSO, nil
}

// RevokeWithSSO removes a browser session and returns its provider logout
// coordinates in one store operation. Durable stores use a transaction so two
// replicas cannot both observe the same live session during logout.
func (s *StoreSessions) RevokeWithSSO(tok string) (SSOSession, bool, error) {
	ctx := context.Background()
	key := hash(tok)
	if txStore, ok := s.store.(store.TxStore); ok {
		tx, err := txStore.Begin(ctx)
		if err != nil {
			return SSOSession{}, false, fmt.Errorf("auth: begin session logout transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		sso, isSSO, err := revokeSessionWithSSO(ctx, tx, key, s.now())
		if err != nil {
			return SSOSession{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return SSOSession{}, false, fmt.Errorf("auth: commit session logout: %w", err)
		}
		return sso, isSSO, nil
	}

	s.logoutMu.Lock()
	defer s.logoutMu.Unlock()
	return revokeSessionWithSSO(ctx, s.store, key, s.now())
}

func revokeSessionWithSSO(ctx context.Context, target store.Store, key string, now time.Time) (SSOSession, bool, error) {
	rec, ok, err := store.GetDoc[storedSession](ctx, target, sessionCollection, key)
	if err != nil {
		return SSOSession{}, false, fmt.Errorf("auth: resolve session for logout: %w", err)
	}
	if !ok {
		return SSOSession{}, false, nil
	}
	if err := target.Delete(ctx, sessionCollection, key); err != nil {
		return SSOSession{}, false, fmt.Errorf("auth: revoke session: %w", err)
	}
	if now.After(rec.Expires) || !rec.SSO {
		return SSOSession{}, false, nil
	}
	if rec.SSOData == (SSOSession{}) && rec.LogoutURL != "" {
		return SSOSession{LogoutURL: rec.LogoutURL}, true, nil
	}
	return rec.SSOData, true, nil
}

// RevokeOIDCFrontChannelSessions removes the session named by an issuer-bound
// sid received through OpenID Connect Front-Channel Logout.
func (s *StoreSessions) RevokeOIDCFrontChannelSessions(provider, issuer, sid string) error {
	ctx := context.Background()
	if txStore, ok := s.store.(store.TxStore); ok {
		tx, err := txStore.Begin(ctx)
		if err != nil {
			return fmt.Errorf("auth: begin OpenID Connect front-channel logout transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := revokeOIDCSessions(ctx, tx, provider, issuer, sid, ""); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("auth: commit OpenID Connect front-channel logout: %w", err)
		}
		return nil
	}

	s.logoutMu.Lock()
	defer s.logoutMu.Unlock()
	return revokeOIDCSessions(ctx, s.store, provider, issuer, sid, "")
}

// RevokeOIDCSessions removes the session identified by sid, or every session
// for subject when sid is absent, after a verified Back-Channel Logout request.
// The logout-token claim and session deletion share one durable transaction, so
// retries, task restarts, and multiple replicas cannot reuse a jti or observe a
// recorded token whose revocation failed.
func (s *StoreSessions) RevokeOIDCSessions(provider, issuer, clientID, jti, sid, subject string, replayExpires time.Time) (bool, error) {
	ctx := context.Background()
	key := oidcLogoutTokenKey(provider, issuer, clientID, jti)
	rec := storedOIDCLogoutToken{Marker: newToken(), Expires: replayExpires}
	if txStore, ok := s.store.(store.TxStore); ok {
		tx, err := txStore.Begin(ctx)
		if err != nil {
			return false, fmt.Errorf("auth: begin OpenID Connect logout transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := sweepOIDCLogoutTokens(ctx, tx, s.now()); err != nil {
			return false, err
		}
		claimed, err := claimOIDCLogoutToken(ctx, tx, key, rec, s.now())
		if err != nil || !claimed {
			return claimed, err
		}
		if err := revokeOIDCSessions(ctx, tx, provider, issuer, sid, subject); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("auth: commit OpenID Connect logout: %w", err)
		}
		return true, nil
	}

	// Ephemeral stores do not provide transactions. StoreSessions serializes their
	// claim and delete sequence in-process; durable multi-replica stores implement
	// TxStore and always take the atomic path above.
	s.logoutMu.Lock()
	defer s.logoutMu.Unlock()
	if err := sweepOIDCLogoutTokens(ctx, s.store, s.now()); err != nil {
		return false, err
	}
	claimed, err := claimOIDCLogoutToken(ctx, s.store, key, rec, s.now())
	if err != nil || !claimed {
		return claimed, err
	}
	if err := revokeOIDCSessions(ctx, s.store, provider, issuer, sid, subject); err != nil {
		_ = s.store.Delete(ctx, oidcLogoutReplayCollection, key)
		return false, err
	}
	return true, nil
}

func sweepOIDCLogoutTokens(ctx context.Context, target store.Store, now time.Time) error {
	records, err := target.List(ctx, oidcLogoutReplayCollection, "")
	if err != nil {
		return fmt.Errorf("auth: list OpenID Connect logout tokens: %w", err)
	}
	for _, record := range records {
		var token storedOIDCLogoutToken
		if err := json.Unmarshal(record.Doc, &token); err != nil {
			return fmt.Errorf("auth: decode OpenID Connect logout token %q: %w", record.Key, err)
		}
		if !token.Expires.After(now) {
			if err := target.Delete(ctx, oidcLogoutReplayCollection, record.Key); err != nil {
				return fmt.Errorf("auth: delete expired OpenID Connect logout token %q: %w", record.Key, err)
			}
		}
	}
	return nil
}

func claimOIDCLogoutToken(ctx context.Context, target store.Store, key string, rec storedOIDCLogoutToken, now time.Time) (bool, error) {
	if tx, ok := target.(store.Tx); ok {
		raw, exists, err := tx.GetForUpdate(ctx, oidcLogoutReplayCollection, key)
		if err != nil {
			return false, fmt.Errorf("auth: read OpenID Connect logout token: %w", err)
		}
		if exists {
			var prior storedOIDCLogoutToken
			if err := json.Unmarshal(raw, &prior); err != nil {
				return false, fmt.Errorf("auth: decode OpenID Connect logout token: %w", err)
			}
			if prior.Expires.After(now) {
				return false, nil
			}
			if err := tx.Delete(ctx, oidcLogoutReplayCollection, key); err != nil {
				return false, fmt.Errorf("auth: delete expired OpenID Connect logout token: %w", err)
			}
		}
		raw, err = json.Marshal(rec)
		if err != nil {
			return false, fmt.Errorf("auth: encode OpenID Connect logout token: %w", err)
		}
		if err := tx.PutIfAbsent(ctx, oidcLogoutReplayCollection, key, raw); err != nil {
			return false, fmt.Errorf("auth: claim OpenID Connect logout token: %w", err)
		}
		stored, ok, err := tx.GetForUpdate(ctx, oidcLogoutReplayCollection, key)
		if err != nil {
			return false, fmt.Errorf("auth: confirm OpenID Connect logout token claim: %w", err)
		}
		if !ok {
			return false, fmt.Errorf("auth: confirm OpenID Connect logout token claim: token is missing")
		}
		var winner storedOIDCLogoutToken
		if err := json.Unmarshal(stored, &winner); err != nil {
			return false, fmt.Errorf("auth: decode OpenID Connect logout token claim: %w", err)
		}
		return winner.Marker == rec.Marker, nil
	}

	prior, exists, err := store.GetDoc[storedOIDCLogoutToken](ctx, target, oidcLogoutReplayCollection, key)
	if err != nil {
		return false, fmt.Errorf("auth: read OpenID Connect logout token: %w", err)
	}
	if exists && prior.Expires.After(now) {
		return false, nil
	}
	if err := store.PutDoc(ctx, target, oidcLogoutReplayCollection, key, rec); err != nil {
		return false, fmt.Errorf("auth: claim OpenID Connect logout token: %w", err)
	}
	return true, nil
}

func revokeOIDCSessions(ctx context.Context, target store.Store, provider, issuer, sid, subject string) error {
	recs, err := target.List(ctx, sessionCollection, "")
	if err != nil {
		return fmt.Errorf("auth: list sessions for OpenID Connect logout: %w", err)
	}
	for _, item := range recs {
		var rec storedSession
		if err := json.Unmarshal(item.Doc, &rec); err != nil {
			return fmt.Errorf("auth: decode session %q for OpenID Connect logout: %w", item.Key, err)
		}
		data := rec.SSOData
		if !rec.SSO || data.Protocol != "oidc" || data.Provider != provider || data.Issuer != issuer {
			continue
		}
		matches := sid != "" && data.SID == sid
		if sid == "" {
			matches = subject != "" && data.Subject == subject
		}
		if matches {
			if err := target.Delete(ctx, sessionCollection, item.Key); err != nil {
				return fmt.Errorf("auth: revoke OpenID Connect session %q: %w", item.Key, err)
			}
		}
	}
	return nil
}

func oidcLogoutTokenKey(provider, issuer, clientID, jti string) string {
	return hash(provider + "\x00" + issuer + "\x00" + clientID + "\x00" + jti)
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
func (s *StoreSessions) Revoke(tok string) error {
	if err := s.store.Delete(context.Background(), sessionCollection, hash(tok)); err != nil {
		return fmt.Errorf("auth: revoke session: %w", err)
	}
	return nil
}
