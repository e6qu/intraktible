// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import "testing"

func TestOIDCConfigFromEnvBindsOrganizationAndWorkspace(t *testing.T) {
	t.Setenv("INTRAKTIBLE_OIDC_SHAUTH_ISSUER", "https://auth.example.test")
	t.Setenv("INTRAKTIBLE_OIDC_SHAUTH_CLIENT_ID", "intraktible-dev")
	t.Setenv("INTRAKTIBLE_OIDC_SHAUTH_CLIENT_SECRET", "secret")
	t.Setenv("INTRAKTIBLE_OIDC_SHAUTH_REDIRECT_URL", "https://intraktible.example.test/v1/auth/oidc/shauth/callback")
	t.Setenv("INTRAKTIBLE_OIDC_SHAUTH_POST_LOGOUT_REDIRECT_URL", "https://intraktible.example.test/v1/auth/signed-out")
	t.Setenv("INTRAKTIBLE_OIDC_SHAUTH_ORG", "e6qu")
	t.Setenv("INTRAKTIBLE_OIDC_SHAUTH_WORKSPACE", "dev")

	config := oidcConfigFromEnv("shauth")
	if config.Org != "e6qu" || config.Workspace != "dev" {
		t.Fatalf("OIDC tenancy = (%q, %q), want (e6qu, dev)", config.Org, config.Workspace)
	}
	if config.PostLogoutRedirectURL != "https://intraktible.example.test/v1/auth/signed-out" {
		t.Fatalf("OIDC post-logout redirect URL = %q", config.PostLogoutRedirectURL)
	}
}
