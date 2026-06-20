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
	"github.com/e6qu/intraktible/platform/identity"
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
	oidcCookiePath  = "/v1/auth/oidc/"
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
	setFlowCookie(w, r, oidcStateCookie, state, oidcCookiePath)
	setFlowCookie(w, r, oidcNonceCookie, nonce, oidcCookiePath)
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
	clearFlowCookie(w, r, oidcStateCookie, oidcCookiePath)
	clearFlowCookie(w, r, oidcNonceCookie, oidcCookiePath)

	login, err := a.Exchange(r.Context(), r.URL.Query().Get("code"), nonce.Value)
	if err != nil {
		Error(w, http.StatusUnauthorized, err)
		return
	}
	if !finishLogin(w, r, h.sessions, h.gate, h.roleAugment, login.Identity, login.Role) {
		return
	}
	http.Redirect(w, r, h.postLoginURL, http.StatusFound)
}

// finishLogin applies the deprovisioning gate and role augmenter, then issues a
// session cookie. It returns false (after writing 403) when the gate denies the
// user — e.g. one deactivated in the IdP via SCIM, even with a valid token.
// Shared by the OIDC and SAML callbacks.
func finishLogin(w http.ResponseWriter, r *http.Request, sessions auth.SessionStore, gate LoginGate, aug RoleAugmenter, id identity.Identity, role auth.Role) bool {
	if gate != nil && !gate(r.Context(), id.Org, id.Workspace, id.Actor) {
		Error(w, http.StatusForbidden, errors.New("sso: user is deactivated"))
		return false
	}
	if aug != nil {
		role = aug(r.Context(), id.Org, id.Workspace, id.Actor, role)
	}
	// An SSO-authenticated human operates the builder across environments (subject
	// to their role); scope restriction is an API-key concept, so SSO sessions get
	// the unrestricted scope rather than no scope (which the env gate denies).
	tok, err := sessions.Issue(id, role, auth.ScopeAll)
	if err != nil {
		Error(w, http.StatusInternalServerError, err)
		return false
	}
	setSessionCookie(w, r, tok, sessions.TTL())
	return true
}

// setFlowCookie writes a short-lived, path-scoped cookie that carries SSO
// round-trip state (OIDC state/nonce, SAML relay-state/request-id). Shared by the
// OIDC and SAML handlers.
func setFlowCookie(w http.ResponseWriter, r *http.Request, name, value, path string) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is gated on TLS, like the session cookie
		Name:     name,
		Value:    value,
		Path:     path,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600, // the login round-trip is short-lived
	})
}

func clearFlowCookie(w http.ResponseWriter, r *http.Request, name, path string) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- expiring cookie (MaxAge<0, empty value)
		Name: name, Value: "", Path: path,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// randToken returns a 256-bit random hex string for CSRF state / nonce.
func randToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
