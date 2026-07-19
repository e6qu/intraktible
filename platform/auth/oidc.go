// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/e6qu/intraktible/platform/identity"
)

// OIDCConfig configures one OIDC identity provider (e.g. Google Workspace, AWS
// Cognito / IAM Identity Center). Users authenticated through it log into the
// provider's bound (Org, Workspace); their role comes from a groups claim.
type OIDCConfig struct {
	Name         string // url-safe identifier, e.g. "google" or "aws"
	Issuer       string // OIDC discovery issuer (e.g. https://accounts.google.com)
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// PostLogoutRedirectURL is this application's registered, same-origin signed-out
	// landing page. It is required when discovery advertises RP-Initiated Logout.
	PostLogoutRedirectURL string
	Org                   string
	Workspace             string
	// GroupsClaim is the ID-token claim carrying the user's groups ("groups" for
	// Google Workspace, "cognito:groups" for AWS Cognito). GroupRoles maps a group
	// to a role (the highest match wins); DefaultRole applies when none match.
	GroupsClaim string
	GroupRoles  map[string]Role
	DefaultRole Role
}

// OIDCAuthenticator runs the OIDC Authorization Code flow for one provider. It is
// safe for concurrent use after construction.
type OIDCAuthenticator struct {
	cfg      OIDCConfig
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
	logout   SSOSession
}

const backChannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

type oidcProviderMetadata struct {
	EndSessionEndpoint                string   `json:"end_session_endpoint"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// OIDCLogout identifies sessions named by a verified Back-Channel Logout token.
type OIDCLogout struct {
	Subject string
	SID     string
	JTI     string
	Issued  time.Time
}

// NewOIDCAuthenticator performs OIDC discovery against the issuer (a network
// call) and builds the flow. It fails loudly on missing required config.
func NewOIDCAuthenticator(ctx context.Context, cfg OIDCConfig) (*OIDCAuthenticator, error) {
	if cfg.Name == "" || cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, fmt.Errorf("auth: oidc provider needs name, issuer, client_id, redirect_url")
	}
	if cfg.Org == "" || cfg.Workspace == "" {
		return nil, fmt.Errorf("auth: oidc %q requires org and workspace", cfg.Name)
	}
	if cfg.DefaultRole.Rank() == 0 {
		cfg.DefaultRole = RoleViewer
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: oidc %q discovery: %w", cfg.Name, err)
	}
	endpoint := provider.Endpoint()
	var metadata oidcProviderMetadata
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("auth: oidc %q discovery metadata: %w", cfg.Name, err)
	}
	if metadata.EndSessionEndpoint != "" {
		if cfg.PostLogoutRedirectURL == "" {
			return nil, fmt.Errorf("auth: oidc %q requires a post-logout redirect URL", cfg.Name)
		}
		redirect, err := url.Parse(cfg.RedirectURL)
		if err != nil || redirect.Scheme == "" || redirect.Host == "" {
			return nil, fmt.Errorf("auth: oidc %q redirect_url must be absolute", cfg.Name)
		}
		postLogout, err := url.Parse(cfg.PostLogoutRedirectURL)
		if err != nil || postLogout.Scheme == "" || postLogout.Host == "" || postLogout.User != nil || postLogout.Path != "/v1/auth/signed-out" || postLogout.RawQuery != "" || postLogout.Fragment != "" {
			return nil, fmt.Errorf("auth: oidc %q post-logout redirect URL must use /v1/auth/signed-out without query or fragment", cfg.Name)
		}
		if !strings.EqualFold(redirect.Scheme, postLogout.Scheme) || !strings.EqualFold(redirect.Host, postLogout.Host) {
			return nil, fmt.Errorf("auth: oidc %q post-logout redirect URL must use the application redirect origin", cfg.Name)
		}
		logoutEndpoint, err := url.Parse(metadata.EndSessionEndpoint)
		if err != nil || logoutEndpoint.Scheme == "" || logoutEndpoint.Host == "" || logoutEndpoint.User != nil {
			return nil, fmt.Errorf("auth: oidc %q discovery returned an invalid end-session endpoint", cfg.Name)
		}
	}
	endpoint.AuthStyle = discoveredAuthStyle(metadata.TokenEndpointAuthMethodsSupported)
	return &OIDCAuthenticator{
		cfg:      cfg,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     endpoint,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
		logout: SSOSession{Protocol: "oidc", Provider: cfg.Name, Issuer: cfg.Issuer, ClientID: cfg.ClientID, EndSessionEndpoint: metadata.EndSessionEndpoint, PostLogoutRedirectURL: cfg.PostLogoutRedirectURL},
	}, nil
}

func discoveredAuthStyle(methods []string) oauth2.AuthStyle {
	for _, method := range methods {
		if method == "client_secret_post" {
			return oauth2.AuthStyleInParams
		}
	}
	for _, method := range methods {
		if method == "client_secret_basic" {
			return oauth2.AuthStyleInHeader
		}
	}
	return oauth2.AuthStyleAutoDetect
}

// Name is the provider's identifier (used in login/callback URLs).
func (a *OIDCAuthenticator) Name() string { return a.cfg.Name }

// Issuer is the verified discovery issuer used to scope logout revocation.
func (a *OIDCAuthenticator) Issuer() string { return a.logout.Issuer }

// ClientID is the audience that scopes Back-Channel Logout replay claims.
func (a *OIDCAuthenticator) ClientID() string { return a.cfg.ClientID }

// AuthCodeURL is the IdP URL to redirect the user to, carrying the CSRF state and
// the nonce that binds the returned ID token to this request.
func (a *OIDCAuthenticator) AuthCodeURL(state, nonce, verifier string) string {
	return a.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}

// OIDCLogin is a verified login: the mapped identity and role.
type OIDCLogin struct {
	Identity identity.Identity
	Role     Role
	SSO      SSOSession
}

// Exchange completes the flow: it swaps code for tokens, verifies the ID token
// (signature via the issuer's JWKS, issuer, audience, expiry) and the nonce,
// then maps the verified claims to an identity + role.
func (a *OIDCAuthenticator) Exchange(ctx context.Context, code, nonce, verifier string) (OIDCLogin, error) {
	if verifier == "" {
		return OIDCLogin{}, fmt.Errorf("auth: oidc %q missing PKCE verifier", a.cfg.Name)
	}
	tok, err := a.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return OIDCLogin{}, fmt.Errorf("auth: oidc %q token exchange: %w", a.cfg.Name, err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		return OIDCLogin{}, fmt.Errorf("auth: oidc %q response missing id_token", a.cfg.Name)
	}
	idTok, err := a.verifier.Verify(ctx, rawID)
	if err != nil {
		return OIDCLogin{}, fmt.Errorf("auth: oidc %q verify id_token: %w", a.cfg.Name, err)
	}
	if idTok.Nonce != nonce {
		return OIDCLogin{}, fmt.Errorf("auth: oidc %q nonce mismatch", a.cfg.Name)
	}
	var claims map[string]any
	if err := idTok.Claims(&claims); err != nil {
		return OIDCLogin{}, fmt.Errorf("auth: oidc %q claims: %w", a.cfg.Name, err)
	}
	// Only adopt the email as identity when the IdP marks it verified; an absent or
	// unverified email falls back to the provider-controlled subject (which a user
	// cannot self-assert as someone else's). A token with neither is rejected rather
	// than minting a session for an empty actor.
	email, _ := claims["email"].(string)
	verified, _ := claims["email_verified"].(bool)
	actor := email
	if email == "" || !verified {
		actor = idTok.Subject
	}
	if actor == "" {
		return OIDCLogin{}, fmt.Errorf("auth: oidc %q token has no verified email or subject", a.cfg.Name)
	}
	id, err := identity.New(a.cfg.Org, a.cfg.Workspace, actor)
	if err != nil {
		return OIDCLogin{}, fmt.Errorf("auth: oidc %q: %w", a.cfg.Name, err)
	}
	session := a.logout
	session.Issuer = idTok.Issuer
	session.Subject = idTok.Subject
	session.IDToken = rawID
	session.SID, _ = claims["sid"].(string)
	return OIDCLogin{Identity: id, Role: a.roleFor(claims), SSO: session}, nil
}

// VerifyLogoutToken verifies a signed OpenID Connect Back-Channel Logout token
// with the same signature, issuer, audience, and expiry checks used for ID
// tokens, followed by the Back-Channel Logout-specific claim checks.
func (a *OIDCAuthenticator) VerifyLogoutToken(ctx context.Context, raw string) (OIDCLogout, error) {
	token, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return OIDCLogout{}, fmt.Errorf("auth: oidc %q verify logout token: %w", a.cfg.Name, err)
	}
	var claims struct {
		Subject string                     `json:"sub"`
		SID     string                     `json:"sid"`
		Nonce   json.RawMessage            `json:"nonce"`
		JTI     string                     `json:"jti"`
		Issued  int64                      `json:"iat"`
		Events  map[string]json.RawMessage `json:"events"`
	}
	if err := token.Claims(&claims); err != nil {
		return OIDCLogout{}, fmt.Errorf("auth: oidc %q logout claims: %w", a.cfg.Name, err)
	}
	if claims.JTI == "" || claims.Issued == 0 || len(claims.Nonce) != 0 || (claims.SID == "" && claims.Subject == "") {
		return OIDCLogout{}, fmt.Errorf("auth: oidc %q logout token claims are invalid", a.cfg.Name)
	}
	event, ok := claims.Events[backChannelLogoutEvent]
	if !ok || !jsonObject(event) {
		return OIDCLogout{}, fmt.Errorf("auth: oidc %q logout token event is invalid", a.cfg.Name)
	}
	issued := time.Unix(claims.Issued, 0)
	now := time.Now()
	if issued.Before(now.Add(-5*time.Minute)) || issued.After(now.Add(time.Minute)) {
		return OIDCLogout{}, fmt.Errorf("auth: oidc %q logout token is stale", a.cfg.Name)
	}
	return OIDCLogout{Subject: claims.Subject, SID: claims.SID, JTI: claims.JTI, Issued: issued}, nil
}

func jsonObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil && value != nil
}

// roleFor maps the user's groups to the highest matching configured role, or the
// provider's default role when none match.
func (a *OIDCAuthenticator) roleFor(claims map[string]any) Role {
	role := a.cfg.DefaultRole
	for _, g := range groupsFromClaim(claims, a.cfg.GroupsClaim) {
		if r, ok := a.cfg.GroupRoles[g]; ok && r.Rank() > role.Rank() {
			role = r
		}
	}
	return role
}

// groupsFromClaim reads the configured groups claim, tolerating the array form
// (Google, Cognito) and a single string.
func groupsFromClaim(claims map[string]any, key string) []string {
	if key == "" {
		return nil
	}
	switch v := claims[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, g := range v {
			if s, ok := g.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		return []string{v}
	default:
		return nil
	}
}
