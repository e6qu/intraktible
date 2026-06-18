// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"

	"github.com/e6qu/intraktible/platform/auth"
)

// LoginGate decides whether a verified SSO user may obtain a session — the hook
// SCIM uses to block a deactivated user. A nil gate allows every verified login.
type LoginGate func(ctx context.Context, org, workspace, email string) bool

// RoleAugmenter may raise a verified user's role from an external source (e.g.
// SCIM group membership) — it takes the role derived from the IdP token and
// returns the effective role. A nil augmenter leaves the token-derived role.
type RoleAugmenter func(ctx context.Context, org, workspace, email string, base auth.Role) auth.Role

const (
	oidcStateCookie = "oidc_state"
	oidcNonceCookie = "oidc_nonce"
)

// OIDCHandler serves SSO login for the configured OIDC providers: a redirect to
// the IdP and a callback that verifies the result and issues a session. It is
// public (mounted outside the authenticated chain).
type OIDCHandler struct {
	authers      map[string]*auth.OIDCAuthenticator
	sessions     auth.SessionStore
	postLoginURL string
	gate         LoginGate
	roleAugment  RoleAugmenter
}

// SetGate installs a login gate (e.g. SCIM's active-user check) consulted after
// token verification, before a session is issued.
func (h *OIDCHandler) SetGate(g LoginGate) { h.gate = g }

// SetRoleAugmenter installs a role augmenter (e.g. SCIM group → role) applied to
// the token-derived role before the session is issued.
func (h *OIDCHandler) SetRoleAugmenter(a RoleAugmenter) { h.roleAugment = a }

// NewOIDCHandler builds the handler over the configured providers; the callback
// redirects to "/" (the app) on success.
func NewOIDCHandler(sessions auth.SessionStore, authers ...*auth.OIDCAuthenticator) *OIDCHandler {
	m := make(map[string]*auth.OIDCAuthenticator, len(authers))
	for _, a := range authers {
		m[a.Name()] = a
	}
	return &OIDCHandler{authers: m, sessions: sessions, postLoginURL: "/"}
}

// Routes registers the public SSO endpoints.
func (h *OIDCHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/auth/oidc/providers", h.providers)
	mux.HandleFunc("GET /v1/auth/oidc/{provider}/login", h.login)
	mux.HandleFunc("GET /v1/auth/oidc/{provider}/callback", h.callback)
}

// providers lists the configured provider names so the login UI can render a
// button per provider.
func (h *OIDCHandler) providers(w http.ResponseWriter, _ *http.Request) {
	names := make([]string, 0, len(h.authers))
	for name := range h.authers {
		names = append(names, name)
	}
	sort.Strings(names)
	JSON(w, http.StatusOK, map[string]any{"providers": names})
}

// login starts the flow: it mints a CSRF state + a nonce, stashes them in
// short-lived cookies, and redirects to the IdP.
func (h *OIDCHandler) login(w http.ResponseWriter, r *http.Request) {
	a, ok := h.authers[r.PathValue("provider")]
	if !ok {
		Error(w, http.StatusNotFound, errors.New("unknown sso provider"))
		return
	}
	state, nonce := randToken(), randToken()
	h.setFlowCookie(w, r, oidcStateCookie, state)
	h.setFlowCookie(w, r, oidcNonceCookie, nonce)
	http.Redirect(w, r, a.AuthCodeURL(state, nonce), http.StatusFound)
}

// callback verifies the CSRF state and the returned ID token, then issues a
// session and redirects into the app.
func (h *OIDCHandler) callback(w http.ResponseWriter, r *http.Request) {
	a, ok := h.authers[r.PathValue("provider")]
	if !ok {
		Error(w, http.StatusNotFound, errors.New("unknown sso provider"))
		return
	}
	state, err := r.Cookie(oidcStateCookie)
	if err != nil || state.Value == "" || state.Value != r.URL.Query().Get("state") {
		Error(w, http.StatusBadRequest, errors.New("sso: invalid or missing state"))
		return
	}
	nonce, err := r.Cookie(oidcNonceCookie)
	if err != nil || nonce.Value == "" {
		Error(w, http.StatusBadRequest, errors.New("sso: missing nonce"))
		return
	}
	h.clearFlowCookie(w, r, oidcStateCookie)
	h.clearFlowCookie(w, r, oidcNonceCookie)

	login, err := a.Exchange(r.Context(), r.URL.Query().Get("code"), nonce.Value)
	if err != nil {
		Error(w, http.StatusUnauthorized, err)
		return
	}
	// Honor deprovisioning: a user deactivated in the IdP (via SCIM) is refused a
	// session even though the IdP still issued a valid token.
	if h.gate != nil && !h.gate(r.Context(), login.Identity.Org, login.Identity.Workspace, login.Identity.Actor) {
		Error(w, http.StatusForbidden, errors.New("sso: user is deactivated"))
		return
	}
	if h.roleAugment != nil {
		login.Role = h.roleAugment(r.Context(), login.Identity.Org, login.Identity.Workspace, login.Identity.Actor, login.Role)
	}
	tok := h.sessions.Issue(login.Identity, login.Role)
	setSessionCookie(w, r, tok, h.sessions.TTL())
	http.Redirect(w, r, h.postLoginURL, http.StatusFound)
}

func (h *OIDCHandler) setFlowCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is gated on TLS, like the session cookie
		Name:     name,
		Value:    value,
		Path:     "/v1/auth/oidc/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600, // the login round-trip is short-lived
	})
}

func (h *OIDCHandler) clearFlowCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- expiring cookie (MaxAge<0, empty value)
		Name: name, Value: "", Path: "/v1/auth/oidc/",
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// randToken returns a 256-bit random hex string for CSRF state / nonce.
func randToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
