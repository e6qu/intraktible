// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"

	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

const (
	oidcClientID = "test-client"
	oidcNonce    = "nonce-abc"
	oidcVerifier = "verifier-abc"
)

type oidcTestProvider struct {
	*httptest.Server
	privateKey *rsa.PrivateKey
}

// testOIDCProvider stands up an OIDC provider: oidctest serves JWKS, and a
// /token endpoint returns an ID token signed with the test key carrying fixed
// claims (email + groups + the nonce the callback test will present).
func testOIDCProvider(t *testing.T) *oidcTestProvider {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := &oidctest.Server{
		PublicKeys: []oidctest.PublicKey{{PublicKey: &priv.PublicKey, KeyID: "k1", Algorithm: oidc.RS256}},
	}
	var issuer string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token",
			"jwks_uri": issuer + "/keys", "response_types_supported": []string{"code"},
			"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{oidc.RS256},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post"}, "end_session_endpoint": issuer + "/oauth2/sessions/logout",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("code_verifier") != oidcVerifier {
			http.Error(w, "invalid PKCE verifier", http.StatusBadRequest)
			return
		}
		claims := fmt.Sprintf(
			`{"iss":%q,"aud":%q,"sub":"u-1","sid":"session-1","email":"ada@acme.com","email_verified":true,"groups":["admins","staff"],"nonce":%q,"exp":%d,"iat":%d}`,
			issuer, oidcClientID, oidcNonce, time.Now().Add(time.Hour).Unix(), time.Now().Unix())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "a", "token_type": "Bearer",
			"id_token": oidctest.SignIDToken(priv, "k1", oidc.RS256, claims),
		})
	})
	mux.Handle("/", srv) // discovery + keys
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	issuer = ts.URL
	srv.SetIssuer(ts.URL)
	return &oidcTestProvider{Server: ts, privateKey: priv}
}

func (provider *oidcTestProvider) sign(claims string) string {
	return oidctest.SignIDToken(provider.privateKey, "k1", oidc.RS256, claims)
}

func oidcHandler(t *testing.T) (*httpx.OIDCHandler, *auth.Sessions) {
	handler, sessions, _ := oidcFixture(t)
	return handler, sessions
}

func oidcFixture(t *testing.T) (*httpx.OIDCHandler, *auth.Sessions, *oidcTestProvider) {
	t.Helper()
	idp := testOIDCProvider(t)
	a, err := auth.NewOIDCAuthenticator(context.Background(), auth.OIDCConfig{
		Name: "test", Issuer: idp.URL, ClientID: oidcClientID, RedirectURL: "https://app/cb",
		PostLogoutRedirectURL: "https://app/v1/auth/signed-out",
		Org:                   "demo", Workspace: "main", GroupsClaim: "groups",
		GroupRoles:  map[string]auth.Role{"admins": auth.RoleAdmin},
		DefaultRole: auth.RoleViewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions()
	return httpx.NewOIDCHandler(sessions, a), sessions, idp
}

func TestOIDCLoginRedirectsWithStateAndNonce(t *testing.T) {
	h, _ := oidcHandler(t)
	mux := http.NewServeMux()
	h.Routes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/test/login", http.NoBody))
	if rec.Code != http.StatusFound {
		t.Fatalf("login -> %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "response_type=code") || !strings.Contains(loc, "nonce=") {
		t.Fatalf("login Location lacks an auth-code request: %s", loc)
	}
	cookies := rec.Result().Cookies()
	var state, nonce, verifier string
	for _, c := range cookies {
		switch c.Name {
		case "oidc_state":
			state = c.Value
		case "oidc_nonce":
			nonce = c.Value
		case "oidc_pkce":
			verifier = c.Value
		}
	}
	if state == "" || nonce == "" || verifier == "" {
		t.Fatalf("login did not set state, nonce, and PKCE cookies: %+v", cookies)
	}
	challenge := sha256.Sum256([]byte(verifier))
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("code_challenge_method") != "S256" || location.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) {
		t.Fatalf("login Location lacks the PKCE S256 challenge: %s", location)
	}

	// An unknown provider is a 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/nope/login", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider login -> %d, want 404", rec.Code)
	}
}

func TestOIDCLoginRejectsUnsafeReturnTargets(t *testing.T) {
	h, _ := oidcHandler(t)
	mux := http.NewServeMux()
	h.Routes(mux)
	for _, target := range []string{"https://evil.example/", "//evil.example/", "/%2f/evil.example/", `\evil.example`, `/\evil.example`, `/%5cevil.example`} {
		rec := httptest.NewRecorder()
		path := "/v1/auth/oidc/test/login?return_to=" + url.QueryEscape(target)
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("return target %q -> %d, want 400", target, rec.Code)
		}
	}
}

func TestOIDCRejectsCrossOriginOrAutoLoginPostLogoutRedirect(t *testing.T) {
	idp := testOIDCProvider(t)
	base := auth.OIDCConfig{Name: "test", Issuer: idp.URL, ClientID: oidcClientID, RedirectURL: "https://app.example.test/v1/auth/oidc/test/callback", Org: "demo", Workspace: "main"}
	for _, value := range []string{"https://auth.example.test/apps", "https://app.example.test/v1/auth/oidc/test/login", "https://app.example.test/v1/auth/signed-out?next=/"} {
		config := base
		config.PostLogoutRedirectURL = value
		if _, err := auth.NewOIDCAuthenticator(context.Background(), config); err == nil {
			t.Errorf("post-logout redirect %q was accepted", value)
		}
	}
	base.PostLogoutRedirectURL = "https://app.example.test/v1/auth/signed-out"
	if _, err := auth.NewOIDCAuthenticator(context.Background(), base); err != nil {
		t.Fatalf("same-origin signed-out landing was rejected: %v", err)
	}
}

func TestOIDCCallbackVerifiesAndIssuesSession(t *testing.T) {
	h, sessions := oidcHandler(t)
	mux := http.NewServeMux()
	h.Routes(mux)

	// Simulate the cookies login would have set; the test provider token carries oidcNonce.
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/test/callback?state=s1&code=xyz", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "oidc_state", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "oidc_nonce", Value: oidcNonce})
	req.AddCookie(&http.Cookie{Name: "oidc_pkce", Value: oidcVerifier})
	req.AddCookie(&http.Cookie{Name: "oidc_return", Value: base64.RawURLEncoding.EncodeToString([]byte("/engine/flow-1?tab=versions"))})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/engine/flow-1?tab=versions" {
		t.Fatalf("callback -> %d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var session string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" && c.Value != "" {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("callback did not issue a session cookie")
	}
	// The session maps to the OIDC identity, and the "admins" group → admin role.
	id, role, scope, ok := sessions.Resolve(session)
	if !ok || id.Actor != "ada@acme.com" || id.Org != "demo" || role != auth.RoleAdmin {
		t.Fatalf("session resolves to %+v role=%q ok=%v", id, role, ok)
	}
	// An SSO session operates the builder across environments.
	if scope != auth.ScopeAll {
		t.Fatalf("SSO session scope = %q, want unrestricted", scope)
	}
	sso, ok, err := sessions.SSOSession(session)
	if err != nil || !ok || sso.Protocol != "oidc" || sso.Provider != "test" || sso.Issuer == "" || sso.Subject != "u-1" || sso.SID != "session-1" || sso.IDToken == "" || sso.EndSessionEndpoint != sso.Issuer+"/oauth2/sessions/logout" || sso.PostLogoutRedirectURL != "https://app/v1/auth/signed-out" {
		t.Fatalf("OIDC session metadata = %#v ok=%v err=%v", sso, ok, err)
	}
}

func TestOIDCCallbackHonorsLoginGate(t *testing.T) {
	h, _ := oidcHandler(t)
	// A gate that denies the verified user (as SCIM would for a deactivated user).
	h.SetGate(func(_ context.Context, _, _, email string) bool { return email != "ada@acme.com" })
	mux := http.NewServeMux()
	h.Routes(mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/test/callback?state=s1&code=xyz", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "oidc_state", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "oidc_nonce", Value: oidcNonce})
	req.AddCookie(&http.Cookie{Name: "oidc_pkce", Value: oidcVerifier})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("deactivated user callback -> %d, want 403", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" && c.Value != "" {
			t.Fatal("a gated-out user must not receive a session")
		}
	}
}

func TestOIDCCallbackAugmentsRole(t *testing.T) {
	idp := testOIDCProvider(t)
	// No group→role mapping in the token, so the base role is the default (viewer).
	a, err := auth.NewOIDCAuthenticator(context.Background(), auth.OIDCConfig{
		Name: "test", Issuer: idp.URL, ClientID: oidcClientID, RedirectURL: "https://app/cb",
		PostLogoutRedirectURL: "https://app/v1/auth/signed-out",
		Org:                   "demo", Workspace: "main", DefaultRole: auth.RoleViewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions()
	h := httpx.NewOIDCHandler(sessions, a)
	// Augmenter elevates the verified user (as SCIM group→role would).
	h.SetRoleAugmenter(func(_ context.Context, _, _, _ string, base auth.Role) auth.Role {
		if auth.RoleEditor.Rank() > base.Rank() {
			return auth.RoleEditor
		}
		return base
	})
	mux := http.NewServeMux()
	h.Routes(mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/test/callback?state=s1&code=xyz", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "oidc_state", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "oidc_nonce", Value: oidcNonce})
	req.AddCookie(&http.Cookie{Name: "oidc_pkce", Value: oidcVerifier})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var session string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			session = c.Value
		}
	}
	_, role, _, ok := sessions.Resolve(session)
	if !ok || role != auth.RoleEditor {
		t.Fatalf("augmented session role = %q ok=%v, want editor", role, ok)
	}
}

func TestOIDCCallbackRejectsBadState(t *testing.T) {
	h, _ := oidcHandler(t)
	mux := http.NewServeMux()
	h.Routes(mux)

	// Cookie state and query state disagree → CSRF rejection.
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/test/callback?state=evil&code=xyz", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "oidc_state", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "oidc_nonce", Value: oidcNonce})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad-state callback -> %d, want 400", rec.Code)
	}
}

func TestOIDCBackChannelLogoutVerifiesRevokesAndRejectsReplay(t *testing.T) {
	h, sessions, provider := oidcFixture(t)
	mux := http.NewServeMux()
	h.Routes(mux)
	id := auth.SSOSession{Protocol: "oidc", Provider: "test", Issuer: provider.URL, Subject: "u-1", SID: "session-1"}
	revoked, _ := sessions.IssueSSO(testIdentity(), auth.RoleViewer, auth.ScopeAll, id)
	keptData := id
	keptData.SID = "session-2"
	kept, _ := sessions.IssueSSO(testIdentity(), auth.RoleViewer, auth.ScopeAll, keptData)
	almostSame := id
	almostSame.Issuer += "/"
	almostSameToken, _ := sessions.IssueSSO(testIdentity(), auth.RoleViewer, auth.ScopeAll, almostSame)
	now := time.Now()
	claims := fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":"u-1","sid":"session-1","iat":%d,"exp":%d,"jti":"logout-1","events":{"http://schemas.openid.net/event/backchannel-logout":{}}}`, provider.URL, oidcClientID, now.Unix(), now.Add(time.Minute).Unix())
	raw := provider.sign(claims)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, backChannelRequest(raw))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("back-channel logout -> %d: %s", rec.Code, rec.Body.String())
	}
	if _, _, _, ok := sessions.Resolve(revoked); ok {
		t.Fatal("sid-matched session remained")
	}
	if _, _, _, ok := sessions.Resolve(kept); !ok {
		t.Fatal("unrelated sid session was revoked")
	}
	if _, _, _, ok := sessions.Resolve(almostSameToken); !ok {
		t.Fatal("a session with a non-exact trailing-slash issuer was revoked")
	}
	replay := httptest.NewRecorder()
	mux.ServeHTTP(replay, backChannelRequest(raw))
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("logout replay -> %d, want 400", replay.Code)
	}
	parts := strings.Split(raw, ".")
	if parts[2][0] == 'A' {
		parts[2] = "B" + parts[2][1:]
	} else {
		parts[2] = "A" + parts[2][1:]
	}
	tampered := httptest.NewRecorder()
	mux.ServeHTTP(tampered, backChannelRequest(strings.Join(parts, ".")))
	if tampered.Code != http.StatusBadRequest {
		t.Fatalf("tampered logout token -> %d, want 400", tampered.Code)
	}
}

func TestOIDCBackChannelLogoutValidatesRequiredClaimsAndEventObject(t *testing.T) {
	h, _, provider := oidcFixture(t)
	mux := http.NewServeMux()
	h.Routes(mux)
	now := time.Now()
	for index, event := range []string{"null", "[]"} {
		claims := fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":"u-1","iat":%d,"exp":%d,"jti":"invalid-event-%d","events":{"http://schemas.openid.net/event/backchannel-logout":%s}}`, provider.URL, oidcClientID, now.Unix(), now.Add(time.Minute).Unix(), index, event)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, backChannelRequest(provider.sign(claims)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("logout event %s -> %d, want 400", event, rec.Code)
		}
	}
	for name, claims := range map[string]string{
		"missing expiry": fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":"u-1","iat":%d,"jti":"missing-expiry","events":{"http://schemas.openid.net/event/backchannel-logout":{}}}`, provider.URL, oidcClientID, now.Unix()),
		"empty nonce":    fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":"u-1","iat":%d,"exp":%d,"jti":"empty-nonce","nonce":"","events":{"http://schemas.openid.net/event/backchannel-logout":{}}}`, provider.URL, oidcClientID, now.Unix(), now.Add(time.Minute).Unix()),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, backChannelRequest(provider.sign(claims)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s logout token -> %d, want 400", name, rec.Code)
		}
	}
	validNonEmpty := fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":"u-1","iat":%d,"exp":%d,"jti":"non-empty-event","events":{"http://schemas.openid.net/event/backchannel-logout":{"reason":"provider logout"},"https://example.test/other":{}}}`, provider.URL, oidcClientID, now.Unix(), now.Add(time.Minute).Unix())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, backChannelRequest(provider.sign(validNonEmpty)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid non-empty event object -> %d: %s", rec.Code, rec.Body.String())
	}
}

func TestOIDCBackChannelLogoutReplayIsSharedAcrossHandlers(t *testing.T) {
	_, _, provider := oidcFixture(t)
	a, err := auth.NewOIDCAuthenticator(context.Background(), auth.OIDCConfig{
		Name: "test", Issuer: provider.URL, ClientID: oidcClientID, RedirectURL: "https://app/cb",
		PostLogoutRedirectURL: "https://app/v1/auth/signed-out", Org: "demo", Workspace: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStoreSessions(store.NewMemory())
	first := http.NewServeMux()
	second := http.NewServeMux()
	h1 := httpx.NewOIDCHandler(sessions, a)
	h2 := httpx.NewOIDCHandler(sessions, a)
	h1.Routes(first)
	h2.Routes(second)
	now := time.Now()
	claims := fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":"u-1","iat":%d,"exp":%d,"jti":"shared-replay","events":{"http://schemas.openid.net/event/backchannel-logout":{}}}`, provider.URL, oidcClientID, now.Unix(), now.Add(time.Minute).Unix())
	raw := provider.sign(claims)
	rec := httptest.NewRecorder()
	first.ServeHTTP(rec, backChannelRequest(raw))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first handler logout -> %d: %s", rec.Code, rec.Body.String())
	}
	replay := httptest.NewRecorder()
	second.ServeHTTP(replay, backChannelRequest(raw))
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("second handler replay -> %d, want 400", replay.Code)
	}
}

func backChannelRequest(token string) *http.Request {
	body := "logout_token=" + url.QueryEscape(token)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/oidc/test/backchannel-logout", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestOIDCSignedOutLandingClearsSessionWithoutStartingLogin(t *testing.T) {
	h, sessions := oidcHandler(t)
	mux := http.NewServeMux()
	h.Routes(mux)
	token, _ := sessions.Issue(testIdentity(), auth.RoleViewer, auth.ScopeAll)
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/signed-out", http.NoBody)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Fatalf("signed-out landing -> %d location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if _, _, _, ok := sessions.Resolve(token); ok {
		t.Fatal("signed-out landing retained local session")
	}
	if !strings.Contains(rec.Body.String(), "You are signed out") || strings.Contains(rec.Body.String(), "window.location") {
		t.Fatalf("signed-out landing unexpectedly starts sign-in: %s", rec.Body.String())
	}
}

func testIdentity() identity.Identity {
	return identity.Identity{Org: "demo", Workspace: "main", Actor: "ada@acme.com"}
}
