// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/auth"
	"github.com/e6qu/intraktible/platform/httpx"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/kms"
	"github.com/e6qu/intraktible/platform/scim"
	"github.com/e6qu/intraktible/platform/secretbox"
)

// aiGuardrailsFromEnv reads the AI guardrail config: per-provider rate limit
// (INTRAKTIBLE_AI_RATE_LIMIT_RPS / _BURST), free-text PII redaction
// (INTRAKTIBLE_AI_GUARDRAIL_PII), structured-output field redaction
// (INTRAKTIBLE_AI_GUARDRAIL_REDACT_FIELDS, CSV), and jailbreak/injection blocking
// (INTRAKTIBLE_AI_GUARDRAIL_BLOCK_INJECTION). All off by default.
func aiGuardrailsFromEnv() (ai.Guardrails, error) {
	g := ai.Guardrails{
		RedactPII:      truthy(os.Getenv("INTRAKTIBLE_AI_GUARDRAIL_PII")),
		RedactFields:   splitCSV(os.Getenv("INTRAKTIBLE_AI_GUARDRAIL_REDACT_FIELDS")),
		BlockInjection: truthy(os.Getenv("INTRAKTIBLE_AI_GUARDRAIL_BLOCK_INJECTION")),
	}
	if v := strings.TrimSpace(os.Getenv("INTRAKTIBLE_AI_RATE_LIMIT_RPS")); v != "" {
		rps, err := strconv.ParseFloat(v, 64)
		if err != nil || rps < 0 {
			return ai.Guardrails{}, fmt.Errorf("INTRAKTIBLE_AI_RATE_LIMIT_RPS %q: want a non-negative number", v)
		}
		g.RatePerSec = rps
	}
	if v := strings.TrimSpace(os.Getenv("INTRAKTIBLE_AI_RATE_LIMIT_BURST")); v != "" {
		b, err := strconv.Atoi(v)
		if err != nil || b < 0 {
			return ai.Guardrails{}, fmt.Errorf("INTRAKTIBLE_AI_RATE_LIMIT_BURST %q: want a non-negative integer", v)
		}
		g.Burst = b
	}
	return g, nil
}

// buildAIRegistry wires the AI providers from the environment. The canned Stub
// is OPT-IN only (dev/tests set INTRAKTIBLE_AI_STUB=1). It used to be registered
// unconditionally, so a server with no provider configured silently served
// canned text as if a model had answered — agent runs, AI nodes, and the
// copilot all recorded fake output as authentic. Without a provider those
// operations now fail loudly instead.
func buildAIRegistry(guardrails ai.Guardrails, egress connectors.EgressPolicy) *ai.Registry {
	reg := ai.NewRegistry()
	if base := os.Getenv("INTRAKTIBLE_AI_BASE_URL"); base != "" {
		name := os.Getenv("INTRAKTIBLE_AI_PROVIDER")
		if name == "" {
			name = "openai"
		}
		provider := ai.NewHTTP(name, base, os.Getenv("INTRAKTIBLE_AI_API_KEY"), os.Getenv("INTRAKTIBLE_AI_MODEL"),
			ai.WithHTTPClient(egress.Client(ai.HTTPTimeout)))
		reg.Register(ai.Guard(provider, guardrails))
		slog.Info("ai: registered HTTP provider", "name", name, "model", os.Getenv("INTRAKTIBLE_AI_MODEL"))
	}
	if truthy(os.Getenv("INTRAKTIBLE_AI_STUB")) {
		slog.Warn("ai: deterministic STUB registered (INTRAKTIBLE_AI_STUB) — canned responses, dev/tests only")
		reg.Register(ai.Guard(ai.Stub{}, guardrails))
	}
	if _, err := reg.Get(""); err != nil {
		slog.Warn("ai: no provider configured — AI nodes, agent runs, and the copilot will fail until one is set",
			"set", "INTRAKTIBLE_AI_BASE_URL (or INTRAKTIBLE_AI_STUB=1 for dev)")
	}
	return reg
}

// connectorSecretBoxFromEnv builds the connector secret keyring. The primary
// (encrypting) key is INTRAKTIBLE_CONNECTOR_SECRET_KEY; optional prior keys for
// decrypting already-sealed values during a rotation are a comma-separated list
// in INTRAKTIBLE_CONNECTOR_SECRET_KEYS_PREVIOUS. Returns nil when no key is set.
func connectorSecretBoxFromEnv(ctx context.Context) (*connectors.Keyring, error) {
	// An external KMS (AWS/GCP), when configured, takes precedence: the key never
	// leaves the provider and the local env key is not needed.
	managed, err := kms.FromEnv(ctx)
	if err != nil {
		return nil, err
	}
	if k, ok := managed.Get(); ok {
		slog.Info("connectors: using external KMS", "provider", os.Getenv("INTRAKTIBLE_KMS_PROVIDER"))
		return connectors.NewKMSKeyring("kms:"+os.Getenv("INTRAKTIBLE_KMS_PROVIDER"), k), nil
	}
	// Otherwise a local key (base64/hex 32 bytes) seals connector credentials, with
	// any previous keys retained for decrypt during rotation. Shares the key-decoding
	// + keyring construction with encryption-at-rest (platform/secretbox).
	kr, err := secretbox.KeyringFromKeys(
		os.Getenv("INTRAKTIBLE_CONNECTOR_SECRET_KEY"),
		splitCSV(os.Getenv("INTRAKTIBLE_CONNECTOR_SECRET_KEYS_PREVIOUS"))...,
	)
	if err != nil {
		return nil, fmt.Errorf("connectors: %w", err)
	}
	return kr, nil
}

// oidcAuthenticators builds an OIDC authenticator per name in
// INTRAKTIBLE_OIDC_PROVIDERS (e.g. "google,aws"), reading each provider's config
// from INTRAKTIBLE_OIDC_<NAME>_* env vars. An operator who explicitly names a
// provider expects it to work, so a provider that fails to initialize (bad config, a
// discovery error) is a LOUD startup failure, not a silent skip that leaves SSO
// quietly unavailable — mirroring the production preflight's fail-fast stance.
func oidcAuthenticators(ctx context.Context) ([]*auth.OIDCAuthenticator, error) {
	raw := strings.TrimSpace(os.Getenv("INTRAKTIBLE_OIDC_PROVIDERS"))
	if raw == "" {
		return nil, nil
	}
	var out []*auth.OIDCAuthenticator
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		a, err := auth.NewOIDCAuthenticator(ctx, oidcConfigFromEnv(name))
		if err != nil {
			return nil, fmt.Errorf("sso: OIDC provider %q is configured but failed to initialize: %w", name, err)
		}
		out = append(out, a)
	}
	return out, nil
}

func oidcConfigFromEnv(name string) auth.OIDCConfig {
	p := "INTRAKTIBLE_OIDC_" + strings.ToUpper(name) + "_"
	cfg := auth.OIDCConfig{
		Name:                  name,
		Issuer:                os.Getenv(p + "ISSUER"),
		ClientID:              os.Getenv(p + "CLIENT_ID"),
		ClientSecret:          os.Getenv(p + "CLIENT_SECRET"),
		RedirectURL:           os.Getenv(p + "REDIRECT_URL"),
		PostLogoutRedirectURL: os.Getenv(p + "POST_LOGOUT_REDIRECT_URL"),
		Org:                   os.Getenv(p + "ORG"),
		Workspace:             os.Getenv(p + "WORKSPACE"),
		GroupsClaim:           os.Getenv(p + "GROUPS_CLAIM"),
		GroupRoles:            parseGroupRoles(os.Getenv(p + "GROUP_ROLES")),
		DefaultRole:           auth.Role(os.Getenv(p + "DEFAULT_ROLE")),
	}
	// Sensible per-provider defaults so operators set the minimum.
	if cfg.Issuer == "" && name == "google" {
		cfg.Issuer = "https://accounts.google.com"
	}
	if cfg.GroupsClaim == "" {
		switch name {
		case "google":
			cfg.GroupsClaim = "groups"
		case "aws":
			cfg.GroupsClaim = "cognito:groups"
		}
	}
	return cfg
}

// parseGroupRoles parses "group1:role1,group2:role2" into a group→role map.
func parseGroupRoles(s string) map[string]auth.Role {
	m := map[string]auth.Role{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if k, v, ok := strings.Cut(pair, ":"); ok {
			m[strings.TrimSpace(k)] = auth.Role(strings.TrimSpace(v))
		}
	}
	return m
}

// samlAuthenticators builds a SAML SP authenticator per name in
// INTRAKTIBLE_SAML_PROVIDERS, reading each provider's config (incl. the IdP
// metadata, SP cert, and SP key from files) from INTRAKTIBLE_SAML_<NAME>_* env.
// An explicitly-named provider that fails to initialize is a LOUD startup failure
// (see oidcAuthenticators) — not a silent skip that disables SSO unnoticed.
func samlAuthenticators() ([]*auth.SAMLAuthenticator, error) {
	raw := strings.TrimSpace(os.Getenv("INTRAKTIBLE_SAML_PROVIDERS"))
	if raw == "" {
		return nil, nil
	}
	var out []*auth.SAMLAuthenticator
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		cfg, err := samlConfigFromEnv(name)
		if err != nil {
			return nil, fmt.Errorf("sso: SAML provider %q config: %w", name, err)
		}
		a, err := auth.NewSAMLAuthenticator(cfg)
		if err != nil {
			return nil, fmt.Errorf("sso: SAML provider %q failed to initialize: %w", name, err)
		}
		out = append(out, a)
	}
	return out, nil
}

func samlConfigFromEnv(name string) (auth.SAMLConfig, error) {
	p := "INTRAKTIBLE_SAML_" + strings.ToUpper(name) + "_"
	idpMeta, err := readFileEnv(p + "IDP_METADATA_FILE")
	if err != nil {
		return auth.SAMLConfig{}, err
	}
	cert, err := readFileEnv(p + "CERT_FILE")
	if err != nil {
		return auth.SAMLConfig{}, err
	}
	keyPEM, err := readFileEnv(p + "KEY_FILE")
	if err != nil {
		return auth.SAMLConfig{}, err
	}
	return auth.SAMLConfig{
		Name:            name,
		EntityID:        os.Getenv(p + "ENTITY_ID"),
		ACSURL:          os.Getenv(p + "ACS_URL"),
		MetadataURL:     os.Getenv(p + "METADATA_URL"),
		IDPMetadataXML:  idpMeta,
		CertPEM:         cert,
		KeyPEM:          keyPEM,
		Org:             os.Getenv(p + "ORG"),
		Workspace:       os.Getenv(p + "WORKSPACE"),
		EmailAttribute:  os.Getenv(p + "EMAIL_ATTR"),
		GroupsAttribute: os.Getenv(p + "GROUPS_ATTR"),
		GroupRoles:      parseGroupRoles(os.Getenv(p + "GROUP_ROLES")),
		DefaultRole:     auth.Role(os.Getenv(p + "DEFAULT_ROLE")),
	}, nil
}

// readFileEnv reads the file named by an env var, or returns "" when unset.
func readFileEnv(key string) (string, error) {
	path := strings.TrimSpace(os.Getenv(key))
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path) // #nosec G304 G703 -- operator-provided config file path (env), not user input
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return string(b), nil
}

func samlNames(as []*auth.SAMLAuthenticator) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name())
	}
	return out
}

func oidcNames(as []*auth.OIDCAuthenticator) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name())
	}
	return out
}

// scimRoleAugmenter raises a verified user's role to the highest role mapped
// from their SCIM group memberships (never below the token-derived base).
func scimRoleAugmenter(users *scim.Store, groupRoles map[string]auth.Role) httpx.RoleAugmenter {
	return func(ctx context.Context, org, workspace, email string, base auth.Role) auth.Role {
		names, err := users.GroupsForUser(ctx, identity.Identity{Org: org, Workspace: workspace}, email)
		if err != nil {
			return base
		}
		role := base
		for _, name := range names {
			if r, ok := groupRoles[name]; ok && r.Rank() > role.Rank() {
				role = r
			}
		}
		return role
	}
}

// splitCSV parses a comma-separated env value into a trimmed, non-empty list.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envFloat reads a float env var, returning fallback when unset or unparseable.
func envFloat(key string, fallback float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

// envInt reads an int env var, returning fallback when unset or unparseable.
func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// truthy reports whether an env value reads as enabled (1/true/yes/on).
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// driftWindowDays reads INTRAKTIBLE_MODEL_DRIFT_WINDOW (in days) for the drift
// scheduler's firing window. 0 (absent/invalid/non-positive) means all-time.
func driftWindowDays() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("INTRAKTIBLE_MODEL_DRIFT_WINDOW")))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
