// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

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
	oidcStateCookie  = "oidc_state"
	oidcNonceCookie  = "oidc_nonce"
	oidcPKCECookie   = "oidc_pkce"
	oidcReturnCookie = "oidc_return"
	oidcCookiePath   = "/v1/auth/oidc/"
)

// OIDCHandler serves SSO login for the configured OIDC providers: a redirect to
// the IdP and a callback that verifies the result and issues a session. It is
// public (mounted outside the authenticated chain).
type OIDCHandler struct {
	authers     map[string]*auth.OIDCAuthenticator
	sessions    auth.SessionStore
	gate        LoginGate
	roleAugment RoleAugmenter
}

// SetGate installs a login gate (e.g. SCIM's active-user check) consulted after
// token verification, before a session is issued.
func (h *OIDCHandler) SetGate(g LoginGate) { h.gate = g }

// SetRoleAugmenter installs a role augmenter (e.g. SCIM group → role) applied to
// the token-derived role before the session is issued.
func (h *OIDCHandler) SetRoleAugmenter(a RoleAugmenter) { h.roleAugment = a }

// NewOIDCHandler builds the handler over the configured providers; the callback
// redirects to the validated local return target carried through the flow.
func NewOIDCHandler(sessions auth.SessionStore, authers ...*auth.OIDCAuthenticator) *OIDCHandler {
	m := make(map[string]*auth.OIDCAuthenticator, len(authers))
	for _, a := range authers {
		m[a.Name()] = a
	}
	return &OIDCHandler{authers: m, sessions: sessions}
}

// Routes registers the public SSO endpoints.
func (h *OIDCHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/auth/oidc/providers", h.providers)
	mux.HandleFunc("GET /v1/auth/oidc/{provider}/login", h.login)
	mux.HandleFunc("GET /v1/auth/oidc/{provider}/callback", h.callback)
	mux.HandleFunc("POST /v1/auth/oidc/{provider}/backchannel-logout", h.backChannelLogout)
	mux.HandleFunc("GET /v1/auth/signed-out", h.signedOut)
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
	returnTarget, ok := safeReturnTarget(r.URL.Query().Get("return_to"))
	if !ok {
		Error(w, http.StatusBadRequest, errors.New("sso: invalid return target"))
		return
	}
	state, nonce, verifier := randToken(), randToken(), randToken()
	setFlowCookie(w, r, oidcStateCookie, state, oidcCookiePath)
	setFlowCookie(w, r, oidcNonceCookie, nonce, oidcCookiePath)
	setFlowCookie(w, r, oidcPKCECookie, verifier, oidcCookiePath)
	setFlowCookie(w, r, oidcReturnCookie, base64.RawURLEncoding.EncodeToString([]byte(returnTarget)), oidcCookiePath)
	http.Redirect(w, r, a.AuthCodeURL(state, nonce, verifier), http.StatusFound)
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
	if err != nil || state.Value == "" || !secureEqual(state.Value, r.URL.Query().Get("state")) {
		Error(w, http.StatusBadRequest, errors.New("sso: invalid or missing state"))
		return
	}
	nonce, err := r.Cookie(oidcNonceCookie)
	if err != nil || nonce.Value == "" {
		Error(w, http.StatusBadRequest, errors.New("sso: missing nonce"))
		return
	}
	verifier, err := r.Cookie(oidcPKCECookie)
	if err != nil || verifier.Value == "" {
		Error(w, http.StatusBadRequest, errors.New("sso: missing PKCE verifier"))
		return
	}
	returnTarget := "/"
	if returnCookie, cookieErr := r.Cookie(oidcReturnCookie); cookieErr == nil {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(returnCookie.Value)
		if decodeErr != nil {
			Error(w, http.StatusBadRequest, errors.New("sso: invalid return target"))
			return
		}
		var targetOK bool
		returnTarget, targetOK = safeReturnTarget(string(decoded))
		if !targetOK {
			Error(w, http.StatusBadRequest, errors.New("sso: invalid return target"))
			return
		}
	}
	clearFlowCookie(w, r, oidcStateCookie, oidcCookiePath)
	clearFlowCookie(w, r, oidcNonceCookie, oidcCookiePath)
	clearFlowCookie(w, r, oidcPKCECookie, oidcCookiePath)
	clearFlowCookie(w, r, oidcReturnCookie, oidcCookiePath)

	login, err := a.Exchange(r.Context(), r.URL.Query().Get("code"), nonce.Value, verifier.Value)
	if err != nil {
		Error(w, http.StatusUnauthorized, err)
		return
	}
	if !finishLogin(w, r, h.sessions, h.gate, h.roleAugment, login.Identity, login.Role, login.SSO) {
		return
	}
	http.Redirect(w, r, returnTarget, http.StatusFound)
}

func safeReturnTarget(raw string) (string, bool) {
	if raw == "" {
		return "/", true
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(raw, `\`) || strings.Contains(parsed.Path, `\`) {
		return "", false
	}
	return raw, true
}

// finishLogin applies the deprovisioning gate and role augmenter, then issues a
// session cookie. It returns false (after writing 403) when the gate denies the
// user — e.g. one deactivated in the IdP via SCIM, even with a valid token.
// Shared by the OIDC and SAML callbacks.
func finishLogin(w http.ResponseWriter, r *http.Request, sessions auth.SessionStore, gate LoginGate, aug RoleAugmenter, id identity.Identity, role auth.Role, sso auth.SSOSession) bool {
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
	tok, err := sessions.IssueSSO(id, role, auth.ScopeAll, sso)
	if err != nil {
		Error(w, http.StatusInternalServerError, err)
		return false
	}
	setSessionCookie(w, r, tok, sessions.TTL())
	return true
}

func (h *OIDCHandler) backChannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a, ok := h.authers[r.PathValue("provider")]
	if !ok {
		Error(w, http.StatusNotFound, errors.New("unknown sso provider"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		Error(w, http.StatusBadRequest, errors.New("sso: invalid back-channel logout request"))
		return
	}
	raw := r.PostForm.Get("logout_token")
	if raw == "" {
		Error(w, http.StatusBadRequest, errors.New("sso: logout_token is required"))
		return
	}
	logout, err := a.VerifyLogoutToken(r.Context(), raw)
	if err != nil {
		Error(w, http.StatusBadRequest, err)
		return
	}
	accepted, err := h.sessions.RevokeOIDCSessions(a.Name(), a.Issuer(), a.ClientID(), logout.JTI, logout.SID, logout.Subject, time.Now().Add(10*time.Minute))
	if err != nil {
		Error(w, http.StatusServiceUnavailable, err)
		return
	}
	if !accepted {
		Error(w, http.StatusBadRequest, errors.New("sso: logout token was already used"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *OIDCHandler) signedOut(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err := h.sessions.Revoke(cookie.Value); err != nil {
			Error(w, http.StatusServiceUnavailable, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- expiring cookie (MaxAge<0, empty value)
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: requestIsSecure(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><title>Signed out · Intraktible</title><style>:root{font:16px system-ui,sans-serif;color-scheme:light dark}body{min-height:100vh;margin:0;display:grid;place-items:center;background:#f7f7fb;color:#161524}main{width:min(28rem,calc(100% - 3rem));padding:2rem;border:1px solid #d8d6e6;border-radius:1rem;background:#fff;box-shadow:0 1rem 3rem #1615241f}h1{margin-top:0}a{display:inline-block;padding:.7rem 1rem;border-radius:.5rem;background:#6d28d9;color:#fff;font-weight:700;text-decoration:none}a:focus-visible{outline:3px solid #0891b2;outline-offset:3px}@media(prefers-color-scheme:dark){body{background:#11101b;color:#f5f3ff}main{background:#1c192b;border-color:#3d3756}}</style></head><body><main><h1>You are signed out</h1><p>Your Intraktible browser session and shared identity-provider session have ended.</p><a href="/">Return to Intraktible</a></main></body></html>`))
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
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600, // the login round-trip is short-lived
	})
}

func clearFlowCookie(w http.ResponseWriter, r *http.Request, name, path string) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- expiring cookie (MaxAge<0, empty value)
		Name: name, Value: "", Path: path,
		HttpOnly: true, Secure: requestIsSecure(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// randToken returns a 256-bit random hex string for CSRF state / nonce.
func randToken() string {
	var b [32]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("httpx: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
