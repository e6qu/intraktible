// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"errors"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/identity"
)

// sessionCookie is the cookie name the Authenticate middleware reads.
const sessionCookie = "session"

// LoginHandler exchanges a valid API key for a session cookie, so the builder UI
// can authenticate once instead of sending the key on every request. It is a
// public endpoint (mounted outside the authenticated chain).
func LoginHandler(keyring *auth.Keyring, sessions auth.SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			APIKey string `json:"api_key"`
		}
		if err := DecodeJSON(r, &req); err != nil {
			Error(w, http.StatusBadRequest, err)
			return
		}
		key, ok := keyring.Resolve(req.APIKey)
		if !ok {
			Error(w, http.StatusUnauthorized, errors.New("invalid api key"))
			return
		}
		// Carry the key's scope into the session so the exchange cannot widen a
		// sandbox-scoped key to every environment (the env gate reads this scope).
		tok, err := sessions.Issue(key.Identity, key.Role, key.Scope)
		if err != nil {
			Error(w, http.StatusInternalServerError, err)
			return
		}
		setSessionCookie(w, r, tok, sessions.TTL())
		writeIdentity(w, key.Identity)
	}
}

// setSessionCookie writes the session cookie. HttpOnly + SameSite are always set;
// Secure is gated on TLS so the cookie still works over plain http in local dev
// and is Secure behind TLS in prod.
func setSessionCookie(w http.ResponseWriter, r *http.Request, tok string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is set when serving over TLS
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

// LogoutHandler revokes the request's session, clears the cookie, and returns the
// server-configured identity-provider front-channel logout URL for an SSO session.
// It is public (clearing a cookie needs no auth) and idempotent.
func LogoutHandler(sessions auth.SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logoutURL := ""
		if c, err := r.Cookie(sessionCookie); err == nil {
			logoutURL = sessions.LogoutURL(c.Value)
			sessions.Revoke(c.Value)
		}
		http.SetCookie(w, &http.Cookie{ // #nosec G124 -- expiring cookie (MaxAge<0, empty value)
			Name: sessionCookie, Value: "", Path: "/",
			HttpOnly: true, Secure: requestIsSecure(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
		})
		JSON(w, http.StatusOK, map[string]string{"logout_url": logoutURL})
	}
}

// MeHandler returns the authenticated caller's identity. It is mounted inside the
// authenticated chain, so the identity is always present.
func MeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := identity.From(r.Context())
		if !ok {
			Error(w, http.StatusUnauthorized, errors.New("authentication required"))
			return
		}
		// Include the caller's role + scope so the UI can hide admin-only surfaces
		// from non-admins (the role is resolved by Authenticate into the Principal).
		role, scope := RoleOf(r.Context()), ""
		if p, ok := PrincipalOf(r.Context()); ok {
			scope = string(p.Scope)
		}
		JSON(w, http.StatusOK, map[string]string{
			"org": id.Org, "workspace": id.Workspace, "actor": id.Actor,
			"role": string(role), "scope": scope,
		})
	}
}

func writeIdentity(w http.ResponseWriter, id identity.Identity) {
	JSON(w, http.StatusOK, map[string]string{
		"org": id.Org, "workspace": id.Workspace, "actor": id.Actor,
	})
}
