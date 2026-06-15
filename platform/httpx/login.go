// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"errors"
	"net/http"

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
		tok := sessions.Issue(key.Identity, key.Role)
		// HttpOnly + SameSite are always set; Secure is gated on TLS so the cookie
		// still works over plain http in local dev and is Secure behind TLS in prod.
		http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is set when serving over TLS
			Name:     sessionCookie,
			Value:    tok,
			Path:     "/",
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(sessions.TTL().Seconds()),
		})
		writeIdentity(w, key.Identity)
	}
}

// LogoutHandler revokes the request's session and clears the cookie. It is public
// (clearing a cookie needs no auth) and idempotent.
func LogoutHandler(sessions auth.SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			sessions.Revoke(c.Value)
		}
		http.SetCookie(w, &http.Cookie{ // #nosec G124 -- expiring cookie (MaxAge<0, empty value)
			Name: sessionCookie, Value: "", Path: "/",
			HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1,
		})
		w.WriteHeader(http.StatusNoContent)
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
		writeIdentity(w, id)
	}
}

func writeIdentity(w http.ResponseWriter, id identity.Identity) {
	JSON(w, http.StatusOK, map[string]string{
		"org": id.Org, "workspace": id.Workspace, "actor": id.Actor,
	})
}
