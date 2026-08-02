// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
)

// extensionConfig is the config of an out-of-process extension connector: a
// trusted HTTP endpoint (inside the VPC or behind an auth boundary) that
// receives the fetch params as a POST body and returns a JSON response. Unlike
// the generic HTTP connector, the extension type never carries credentials in
// its config — the extension endpoint owns its own auth, and the platform sends
// only the opaque tenant identity and the fetch params. This is the
// out-of-process extension protocol: no third-party code runs inside the
// API/decision process.
type extensionConfig struct {
	URL     string            `json:"url"`
	Timeout int               `json:"timeout_seconds"`
	Headers map[string]string `json:"headers,omitempty"`
}

type extensionConnector struct {
	url     string
	timeout time.Duration
	headers map[string]string
	client  *http.Client
}

func newExtension(config json.RawMessage, egress EgressPolicy) (extensionConnector, error) {
	var cfg extensionConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return extensionConnector{}, fmt.Errorf("context-layer: extension connector config: %w", err)
	}
	if cfg.URL == "" {
		return extensionConnector{}, fmt.Errorf("context-layer: extension connector needs a url")
	}
	timeout := 30
	if cfg.Timeout > 0 {
		timeout = cfg.Timeout
	}
	return extensionConnector{
		url:     cfg.URL,
		timeout: time.Duration(timeout) * time.Second,
		headers: cfg.Headers,
		client:  egress.Client(time.Duration(timeout) * time.Second),
	}, nil
}

// extensionRequest is the POST body the platform sends to the extension
// endpoint.
type extensionRequest struct {
	Org       string          `json:"org"`
	Workspace string          `json:"workspace"`
	Params    json.RawMessage `json:"params"`
}

// extensionResponse is what the extension endpoint must return.
type extensionResponse struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

func (e extensionConnector) Fetch(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	id, _ := identity.From(ctx)
	body, err := json.Marshal(extensionRequest{
		Org:       id.Org,
		Workspace: id.Workspace,
		Params:    params,
	})
	if err != nil {
		return nil, fmt.Errorf("context-layer: extension marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("context-layer: extension request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("context-layer: extension fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("context-layer: extension returned %d: %s", resp.StatusCode, string(raw))
	}

	var result extensionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("context-layer: extension response decode: %w", err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("context-layer: extension error: %s", result.Error)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("context-layer: extension returned no data")
	}
	return result.Data, nil
}
