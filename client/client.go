// SPDX-License-Identifier: AGPL-3.0-or-later

// Package client is a typed Go SDK for the intraktible public data-plane API —
// the contract published at /openapi.json. It wraps the decide hot path,
// decision history, and flow management over net/http with no third-party
// dependencies, so a Go service can call a decision flow in a few lines.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Client calls an intraktible instance. Construct it with New; it is safe for
// concurrent use.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient sets the underlying HTTP client (for timeouts, transports, or
// test servers). The default is http.DefaultClient.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// New builds a Client for the given base URL (e.g. "https://decide.acme.com")
// authenticating with apiKey via the X-Api-Key header.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: http.DefaultClient}
	for _, o := range opts {
		o(c)
	}
	return c
}

// APIError is a non-2xx response, carrying the server's status and message.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("intraktible: http %d: %s", e.Status, e.Message)
}

// DecideRequest is the input to a decision: the data to decide on, plus an
// optional Context Layer entity whose features are injected.
type DecideRequest struct {
	Data       map[string]any `json:"data"`
	EntityType string         `json:"entity_type,omitempty"`
	EntityID   string         `json:"entity_id,omitempty"`
}

// DecideResult is a recorded decision. A flow whose logic errors completes with
// Status "failed" and a populated Error (it is not a transport error).
type DecideResult struct {
	DecisionID  string         `json:"decision_id"`
	Status      string         `json:"status"`
	Data        map[string]any `json:"data,omitempty"`
	Disposition string         `json:"disposition,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// BatchResult is the outcome of a batch decide: a summary plus per-row results.
type BatchResult struct {
	Summary map[string]any   `json:"summary"`
	Results []map[string]any `json:"results"`
}

// Decision is a row of recorded decision history.
type Decision struct {
	DecisionID  string `json:"decision_id"`
	Slug        string `json:"slug"`
	Version     int    `json:"version"`
	Environment string `json:"environment"`
	Status      string `json:"status"`
	Disposition string `json:"disposition,omitempty"`
}

// Flow is a flow summary.
type Flow struct {
	FlowID string `json:"flow_id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Latest int    `json:"latest"`
}

// FlowDoc is a flow-as-code document (the shape /openapi.json's FlowImport, and
// what the export endpoint produces).
type FlowDoc struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name,omitempty"`
	Graph       json.RawMessage `json:"graph"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ImportResult reports a flow-as-code import.
type ImportResult struct {
	FlowID    string `json:"flow_id"`
	Slug      string `json:"slug"`
	Version   int    `json:"version"`
	Created   bool   `json:"created"`
	Published bool   `json:"published"`
}

// Identity is the authenticated caller.
type Identity struct {
	Org       string `json:"org"`
	Workspace string `json:"workspace"`
	Actor     string `json:"actor"`
	Scope     string `json:"scope"`
	Role      string `json:"role"`
}

// Decide runs the live version of slug in env against req and returns the
// recorded decision.
func (c *Client) Decide(ctx context.Context, slug, env string, req DecideRequest) (DecideResult, error) {
	return do[DecideResult](ctx, c, http.MethodPost, decidePath(slug, env, "decide"), req)
}

// DecideBatch runs each row of dataset through the recorded decide path.
func (c *Client) DecideBatch(ctx context.Context, slug, env string, dataset []map[string]any) (BatchResult, error) {
	return do[BatchResult](ctx, c, http.MethodPost, decidePath(slug, env, "decide/batch"),
		map[string]any{"dataset": dataset})
}

// ListDecisions returns recorded decisions, newest first.
func (c *Client) ListDecisions(ctx context.Context) ([]Decision, error) {
	out, err := do[struct {
		Decisions []Decision `json:"decisions"`
	}](ctx, c, http.MethodGet, "/v1/decisions", nil)
	return out.Decisions, err
}

// GetDecision reads one decision by id.
func (c *Client) GetDecision(ctx context.Context, decisionID string) (Decision, error) {
	return do[Decision](ctx, c, http.MethodGet, "/v1/decisions/"+url.PathEscape(decisionID), nil)
}

// ListFlows returns the caller's flows.
func (c *Client) ListFlows(ctx context.Context) ([]Flow, error) {
	out, err := do[struct {
		Flows []Flow `json:"flows"`
	}](ctx, c, http.MethodGet, "/v1/flows", nil)
	return out.Flows, err
}

// CreateFlow creates an empty flow and returns its id.
func (c *Client) CreateFlow(ctx context.Context, slug, name string) (string, error) {
	out, err := do[struct {
		FlowID string `json:"flow_id"`
	}](ctx, c, http.MethodPost, "/v1/flows", map[string]string{"slug": slug, "name": name})
	return out.FlowID, err
}

// GetFlow reads one flow by id.
func (c *Client) GetFlow(ctx context.Context, flowID string) (Flow, error) {
	return do[Flow](ctx, c, http.MethodGet, "/v1/flows/"+url.PathEscape(flowID), nil)
}

// ImportFlow upserts a flow from a flow-as-code document (create-if-new, else
// publish a new version; identical content is a no-op).
func (c *Client) ImportFlow(ctx context.Context, doc FlowDoc) (ImportResult, error) {
	return do[ImportResult](ctx, c, http.MethodPost, "/v1/flows/import", doc)
}

// Me returns the authenticated caller's identity, scope, and role.
func (c *Client) Me(ctx context.Context) (Identity, error) {
	return do[Identity](ctx, c, http.MethodGet, "/v1/me", nil)
}

func decidePath(slug, env, tail string) string {
	return "/v1/flows/" + url.PathEscape(slug) + "/" + url.PathEscape(env) + "/" + tail
}

// do issues one request: it marshals body (when non-nil), sets auth, and decodes
// a 2xx JSON body into T. A non-2xx response becomes an *APIError carrying the
// server's {error} message when present.
func do[T any](ctx context.Context, c *Client, method, path string, body any) (T, error) {
	var zero T
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return zero, fmt.Errorf("intraktible: marshal request: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return zero, err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return zero, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return zero, &APIError{Status: resp.StatusCode, Message: msg}
	}
	var out T
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return zero, fmt.Errorf("intraktible: decode response: %w", err)
		}
	}
	return out, nil
}
