// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
)

// maxProviderBody caps a provider response read into memory (same as the HTTP
// connector — a misbehaving upstream must not exhaust memory).
const maxProviderBody = 1 << 20

// --- outbound request auth (shared by the http, graphql, and provider adapters) ---

// authConfig configures outbound authentication on a connector request. Its field
// name ("auth") is a recognized credential key, so the whole block is sealed at
// rest and redacted at the HTTP boundary — the token never leaves the server.
// Type selects the scheme:
//
//	bearer: Authorization: Bearer <token>
//	header: <name>: <value>            (e.g. X-Api-Key: …)
//	basic:  HTTP Basic <username:password>
//	query:  appends ?<name>=<value> to the URL
//	oauth2: OAuth2 client_credentials — fetch a token from token_url (cached by
//	        its expiry) and send it as Authorization: Bearer
type authConfig struct {
	Type     string `json:"type"`
	Token    string `json:"token,omitempty"`
	Name     string `json:"name,omitempty"`
	Value    string `json:"value,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// oauth2 (client_credentials grant)
	TokenURL     string `json:"token_url,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// validate checks the auth block is internally consistent at define time, so a
// misconfigured connector fails loudly when saved rather than silently sending
// unauthenticated requests at fetch time.
func (a *authConfig) validate() error {
	if a == nil {
		return nil
	}
	switch a.Type {
	case "", "none":
	case "bearer":
		if a.Token == "" {
			return fmt.Errorf("context-layer: bearer auth needs a token")
		}
	case "header", "query":
		if a.Name == "" {
			return fmt.Errorf("context-layer: %s auth needs a name", a.Type)
		}
		// An empty value sends Name with no credential — a request that LOOKS
		// authenticated but isn't. Require it explicitly rather than silently
		// emitting Header.Set(name, "") / q.Set(name, "").
		if a.Value == "" {
			return fmt.Errorf("context-layer: %s auth needs a value", a.Type)
		}
	case "basic":
		if a.Username == "" {
			return fmt.Errorf("context-layer: basic auth needs a username")
		}
	case "oauth2":
		u, err := url.Parse(a.TokenURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("context-layer: oauth2 auth needs an http(s) token_url, got %q", a.TokenURL)
		}
		if a.ClientID == "" || a.ClientSecret == "" {
			return fmt.Errorf("context-layer: oauth2 auth needs client_id and client_secret")
		}
	default:
		return fmt.Errorf("context-layer: unknown auth type %q (bearer|header|basic|query|oauth2)", a.Type)
	}
	return nil
}

// authorize attaches authentication to req. For oauth2 it fetches (or reuses a
// cached) client_credentials token over the egress-guarded client; other schemes
// are synchronous. The connectors call this from Fetch (which has the context +
// client); recording the connector's response keeps replay stable regardless of
// when/whether a token was fetched.
func (a *authConfig) authorize(ctx context.Context, req *http.Request, client *http.Client) error {
	if a == nil {
		return nil
	}
	if a.Type == "oauth2" {
		tok, err := a.oauthToken(ctx, client)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil
	}
	a.apply(req)
	return nil
}

// apply attaches a synchronous (non-oauth2) auth scheme to req.
func (a *authConfig) apply(req *http.Request) {
	switch a.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+a.Token)
	case "header":
		req.Header.Set(a.Name, a.Value)
	case "basic":
		req.SetBasicAuth(a.Username, a.Password)
	case "query":
		q := req.URL.Query()
		q.Set(a.Name, a.Value)
		req.URL.RawQuery = q.Encode()
	}
}

// oauthTokens caches client_credentials tokens by token_url+client_id+scope until
// shortly before they expire, so a connector doesn't fetch a token per call. It is
// process-local shell state; the connector response is what replay reads.
var oauthTokens = struct {
	mu  sync.Mutex
	tok map[string]oauthEntry
}{tok: map[string]oauthEntry{}}

type oauthEntry struct {
	token  string
	expiry time.Time
}

func (a *authConfig) oauthToken(ctx context.Context, client *http.Client) (string, error) {
	// The key is tenant-scoped (org\x00workspace) AND includes a fingerprint of the
	// client_secret: tenant-scoping keeps this process-global cache from ever serving
	// one tenant's token to another that happens to share token_url/client_id/scope,
	// and the secret fingerprint means a rotated secret doesn't keep serving a stale
	// token (and two connectors with the same id but different secrets never collide).
	id, _ := identity.From(ctx)
	secretFP := sha256.Sum256([]byte(a.ClientSecret))
	key := id.Org + "\x00" + id.Workspace + "\x00" + a.TokenURL + "\x00" + a.ClientID + "\x00" + a.Scope + "\x00" + hex.EncodeToString(secretFP[:8])
	now := time.Now()
	oauthTokens.mu.Lock()
	if e, ok := oauthTokens.tok[key]; ok && now.Before(e.expiry) {
		oauthTokens.mu.Unlock()
		return e.token, nil
	}
	oauthTokens.mu.Unlock()

	tok, ttl, err := fetchClientCredentialsToken(ctx, client, a)
	if err != nil {
		return "", err
	}
	// Re-read the clock AFTER the (up to several-second) token round-trip: using the
	// pre-fetch `now` would truncate the new entry's effective TTL by the fetch
	// latency and sweep with a stale clock.
	now = time.Now()
	oauthTokens.mu.Lock()
	// Evict entries that have expired (a deleted/rotated connector's key is never
	// looked up again, so it would otherwise linger) before inserting the fresh one.
	for k, e := range oauthTokens.tok {
		if !now.Before(e.expiry) {
			delete(oauthTokens.tok, k)
		}
	}
	oauthTokens.tok[key] = oauthEntry{token: tok, expiry: now.Add(ttl)}
	oauthTokens.mu.Unlock()
	return tok, nil
}

// fetchClientCredentialsToken performs the OAuth2 client_credentials grant against
// token_url and returns the access token + a cache TTL (with a safety margin). The
// call goes through the egress-guarded client, so the token endpoint is SSRF-safe.
func fetchClientCredentialsToken(ctx context.Context, client *http.Client, a *authConfig) (string, time.Duration, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {a.ClientID},
		"client_secret": {a.ClientSecret},
	}
	if a.Scope != "" {
		form.Set("scope", a.Scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("context-layer: oauth2 token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("context-layer: oauth2 token fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if err != nil {
		return "", 0, fmt.Errorf("context-layer: oauth2 token read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("context-layer: oauth2 token endpoint status %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &tr); err != nil || tr.AccessToken == "" {
		return "", 0, fmt.Errorf("context-layer: oauth2 token response has no access_token")
	}
	// Default to 5 minutes when the provider omits expires_in; cache to 30s before
	// expiry so an in-flight request never uses a just-expired token.
	ttl := 5 * time.Minute
	if tr.ExpiresIn > 0 {
		ttl = time.Duration(tr.ExpiresIn) * time.Second
	}
	if ttl > 30*time.Second {
		ttl -= 30 * time.Second
	}
	return tr.AccessToken, ttl, nil
}

// applyHeaders sets static custom headers. It is applied before auth so an auth
// scheme always wins over a stray header of the same name.
func applyHeaders(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
}

// flattenQuery turns a flat JSON object of params into URL query values (scalars
// only; nested values are skipped). Used by GET-oriented provider adapters.
func flattenQuery(params json.RawMessage) (url.Values, error) {
	q := url.Values{}
	if len(params) == 0 {
		return q, nil
	}
	var m map[string]any
	if err := json.Unmarshal(params, &m); err != nil {
		return nil, fmt.Errorf("context-layer: provider params must be a flat JSON object: %w", err)
	}
	for k, v := range m {
		switch t := v.(type) {
		case string:
			q.Set(k, t)
		case float64:
			q.Set(k, strconv.FormatFloat(t, 'f', -1, 64))
		case bool:
			q.Set(k, strconv.FormatBool(t))
		default:
			// Fail loudly rather than silently dropping a value we can't encode as a
			// scalar query param — otherwise the upstream request would differ from what
			// the flow asked for (e.g. a dropped Stripe `expand` array) with no signal.
			return nil, fmt.Errorf("context-layer: query param %q has unsupported type %T (only string/number/bool); flatten arrays/objects into scalar params", k, v)
		}
	}
	return q, nil
}

// readJSONResponse drains an upstream response, enforcing a 2xx status, a body
// cap, and a JSON body — the shared contract every adapter returns to a flow.
// The caller owns closing resp.Body.
func readJSONResponse(resp *http.Response, who string) (json.RawMessage, error) {
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if err != nil {
		return nil, fmt.Errorf("context-layer: %s read: %w", who, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("context-layer: %s status %d", who, resp.StatusCode)
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("context-layer: %s returned non-JSON body", who)
	}
	return json.RawMessage(b), nil
}

// --- Plaid connector (open banking / transactions) ---

// plaidHosts maps a Plaid environment to its API host. The egress guard still
// applies at dial time, so even a misconfigured host can't reach internal targets.
var plaidHosts = map[string]string{
	"sandbox":     "sandbox.plaid.com",
	"development": "development.plaid.com",
	"production":  "production.plaid.com",
}

type plaidConfig struct {
	Env      string `json:"env"`
	ClientID string `json:"client_id"`
	Secret   string `json:"secret"`
	Path     string `json:"path"`
}

// plaidConnector calls a Plaid endpoint. Plaid authenticates by including
// client_id + secret in the JSON request body (not a header), which this adapter
// injects so the flow only supplies the request-specific fields.
type plaidConnector struct {
	baseURL  string
	clientID string
	secret   string
	path     string
	client   *http.Client
}

func newPlaid(config json.RawMessage, egress EgressPolicy) (plaidConnector, error) {
	var cfg plaidConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return plaidConnector{}, fmt.Errorf("context-layer: plaid connector config: %w", err)
		}
	}
	host, ok := plaidHosts[cfg.Env]
	if !ok {
		return plaidConnector{}, fmt.Errorf("context-layer: plaid connector needs env (sandbox|development|production), got %q", cfg.Env)
	}
	if cfg.ClientID == "" || cfg.Secret == "" {
		return plaidConnector{}, fmt.Errorf("context-layer: plaid connector needs client_id and secret")
	}
	if !strings.HasPrefix(cfg.Path, "/") {
		return plaidConnector{}, fmt.Errorf("context-layer: plaid connector path must start with /, got %q", cfg.Path)
	}
	return plaidConnector{
		baseURL: "https://" + host, clientID: cfg.ClientID, secret: cfg.Secret,
		path: cfg.Path, client: egress.Client(fetchTimeout),
	}, nil
}

// Fetch merges the flow's params with the Plaid credentials and POSTs them as the
// JSON request body, returning the decoded response.
func (p plaidConnector) Fetch(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	body := map[string]any{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &body); err != nil {
			return nil, fmt.Errorf("context-layer: plaid connector params must be a JSON object: %w", err)
		}
	}
	body["client_id"] = p.clientID
	body["secret"] = p.secret
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("context-layer: plaid connector request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+p.path, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("context-layer: plaid connector request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("context-layer: plaid connector fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return readJSONResponse(resp, "plaid connector")
}

// --- Stripe connector (payments) ---

const stripeBaseURL = "https://api.stripe.com"

type stripeConfig struct {
	SecretKey string `json:"secret_key"`
	Path      string `json:"path"`
}

// stripeConnector retrieves a Stripe resource. Stripe authenticates with a bearer
// secret key and returns JSON on GET, so this adapter is a read path: the flow
// supplies the resource path (and optional query params) and gets the object back.
type stripeConnector struct {
	baseURL   string
	secretKey string
	path      string
	client    *http.Client
}

func newStripe(config json.RawMessage, egress EgressPolicy) (stripeConnector, error) {
	var cfg stripeConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return stripeConnector{}, fmt.Errorf("context-layer: stripe connector config: %w", err)
		}
	}
	if cfg.SecretKey == "" {
		return stripeConnector{}, fmt.Errorf("context-layer: stripe connector needs a secret_key")
	}
	if !strings.HasPrefix(cfg.Path, "/") {
		return stripeConnector{}, fmt.Errorf("context-layer: stripe connector path must start with /, got %q", cfg.Path)
	}
	return stripeConnector{baseURL: stripeBaseURL, secretKey: cfg.SecretKey, path: cfg.Path, client: egress.Client(fetchTimeout)}, nil
}

// Fetch GETs the configured Stripe resource (with any flat params as query string)
// under bearer auth, returning the decoded object.
func (s stripeConnector) Fetch(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	q, err := flattenQuery(params)
	if err != nil {
		return nil, err
	}
	u := s.baseURL + s.path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("context-layer: stripe connector request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.secretKey)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("context-layer: stripe connector fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return readJSONResponse(resp, "stripe connector")
}
