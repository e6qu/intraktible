// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"errors"
	"net/http"
	"sort"

	"github.com/e6qu/intraktible/platform/auth"
)

const (
	samlCookiePath    = "/v1/auth/saml/"
	samlRelayCookie   = "saml_relay"
	samlRequestCookie = "saml_request"
)

// SAMLHandler serves SAML 2.0 SP login: an SP-initiated redirect to the IdP, the
// Assertion Consumer Service (ACS) callback that verifies the signed response and
// issues a session, and the SP metadata. Public (mounted outside the auth chain).
type SAMLHandler struct {
	authers      map[string]*auth.SAMLAuthenticator
	sessions     auth.SessionStore
	postLoginURL string
	gate         LoginGate
	roleAugment  RoleAugmenter
}

// NewSAMLHandler builds the handler over the configured providers.
func NewSAMLHandler(sessions auth.SessionStore, authers ...*auth.SAMLAuthenticator) *SAMLHandler {
	m := make(map[string]*auth.SAMLAuthenticator, len(authers))
	for _, a := range authers {
		m[a.Name()] = a
	}
	return &SAMLHandler{authers: m, sessions: sessions, postLoginURL: "/"}
}

// SetGate installs the deprovisioning gate (e.g. SCIM's active-user check).
func (h *SAMLHandler) SetGate(g LoginGate) { h.gate = g }

// SetRoleAugmenter installs the role augmenter (e.g. SCIM group → role).
func (h *SAMLHandler) SetRoleAugmenter(a RoleAugmenter) { h.roleAugment = a }

// Routes registers the public SAML endpoints.
func (h *SAMLHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/auth/saml/providers", h.providers)
	mux.HandleFunc("GET /v1/auth/saml/{provider}/login", h.login)
	mux.HandleFunc("POST /v1/auth/saml/{provider}/acs", h.acs)
	mux.HandleFunc("GET /v1/auth/saml/{provider}/metadata", h.metadata)
}

func (h *SAMLHandler) providers(w http.ResponseWriter, _ *http.Request) {
	names := make([]string, 0, len(h.authers))
	for name := range h.authers {
		names = append(names, name)
	}
	sort.Strings(names)
	JSON(w, http.StatusOK, map[string]any{"providers": names})
}

// login mints a relay-state, stashes it and the AuthnRequest id in short-lived
// cookies, and redirects to the IdP.
func (h *SAMLHandler) login(w http.ResponseWriter, r *http.Request) {
	a, ok := h.authers[r.PathValue("provider")]
	if !ok {
		Error(w, http.StatusNotFound, errors.New("unknown sso provider"))
		return
	}
	relay := randToken()
	redirectURL, requestID, err := a.AuthnRequest(relay)
	if err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	setFlowCookie(w, r, samlRelayCookie, relay, samlCookiePath)
	setFlowCookie(w, r, samlRequestCookie, requestID, samlCookiePath)
	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// acs consumes the IdP's signed SAMLResponse: it checks the relay-state (CSRF),
// validates the assertion (against the request id), and issues a session.
func (h *SAMLHandler) acs(w http.ResponseWriter, r *http.Request) {
	a, ok := h.authers[r.PathValue("provider")]
	if !ok {
		Error(w, http.StatusNotFound, errors.New("unknown sso provider"))
		return
	}
	if err := r.ParseForm(); err != nil {
		Error(w, http.StatusBadRequest, err)
		return
	}
	relay, err := r.Cookie(samlRelayCookie)
	if err != nil || relay.Value == "" || !secureEqual(relay.Value, r.PostFormValue("RelayState")) {
		Error(w, http.StatusBadRequest, errors.New("sso: invalid or missing relay state"))
		return
	}
	var requestIDs []string
	if req, err := r.Cookie(samlRequestCookie); err == nil && req.Value != "" {
		requestIDs = []string{req.Value}
	}
	clearFlowCookie(w, r, samlRelayCookie, samlCookiePath)
	clearFlowCookie(w, r, samlRequestCookie, samlCookiePath)

	login, err := a.ParseACS(r, requestIDs)
	if err != nil {
		Error(w, http.StatusUnauthorized, err)
		return
	}
	if !finishLogin(w, r, h.sessions, h.gate, h.roleAugment, login.Identity, login.Role, auth.SSOSession{Protocol: "saml"}) {
		return
	}
	http.Redirect(w, r, h.postLoginURL, http.StatusFound)
}

// metadata serves this SP's SAML metadata, for registering with the IdP.
func (h *SAMLHandler) metadata(w http.ResponseWriter, r *http.Request) {
	a, ok := h.authers[r.PathValue("provider")]
	if !ok {
		Error(w, http.StatusNotFound, errors.New("unknown sso provider"))
		return
	}
	md, err := a.Metadata()
	if err != nil {
		Error(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(md)
}
