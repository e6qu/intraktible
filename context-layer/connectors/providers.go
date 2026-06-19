// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
type authConfig struct {
	Type     string `json:"type"`
	Token    string `json:"token,omitempty"`
	Name     string `json:"name,omitempty"`
	Value    string `json:"value,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
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
	case "basic":
		if a.Username == "" {
			return fmt.Errorf("context-layer: basic auth needs a username")
		}
	default:
		return fmt.Errorf("context-layer: unknown auth type %q (bearer|header|basic|query)", a.Type)
	}
	return nil
}

// apply attaches the configured authentication to req.
func (a *authConfig) apply(req *http.Request) {
	if a == nil {
		return
	}
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
