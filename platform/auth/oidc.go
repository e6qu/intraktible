// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"fmt"

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
	Org          string
	Workspace    string
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
	return &OIDCAuthenticator{
		cfg:      cfg,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
	}, nil
}

// Name is the provider's identifier (used in login/callback URLs).
func (a *OIDCAuthenticator) Name() string { return a.cfg.Name }

// AuthCodeURL is the IdP URL to redirect the user to, carrying the CSRF state and
// the nonce that binds the returned ID token to this request.
func (a *OIDCAuthenticator) AuthCodeURL(state, nonce string) string {
	return a.oauth.AuthCodeURL(state, oidc.Nonce(nonce))
}

// OIDCLogin is a verified login: the mapped identity and role.
type OIDCLogin struct {
	Identity identity.Identity
	Role     Role
}

// Exchange completes the flow: it swaps code for tokens, verifies the ID token
// (signature via the issuer's JWKS, issuer, audience, expiry) and the nonce,
// then maps the verified claims to an identity + role.
func (a *OIDCAuthenticator) Exchange(ctx context.Context, code, nonce string) (OIDCLogin, error) {
	tok, err := a.oauth.Exchange(ctx, code)
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
	email, _ := claims["email"].(string)
	if email == "" {
		email = idTok.Subject
	}
	return OIDCLogin{
		Identity: identity.Identity{Org: a.cfg.Org, Workspace: a.cfg.Workspace, Actor: email},
		Role:     a.roleFor(claims),
	}, nil
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
