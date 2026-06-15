// SPDX-License-Identifier: AGPL-3.0-or-later

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpTimeout bounds a single completion call.
const httpTimeout = 60 * time.Second

// HTTP is a real provider speaking the OpenAI-compatible Chat Completions API
// (`POST {base}/chat/completions`), which OpenAI, Ollama, vLLM, and many gateways
// — including Anthropic via a compatible endpoint — implement. When a Request
// carries a Schema it asks for a JSON object and returns the reply as Structured.
type HTTP struct {
	name    string
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewHTTP builds an HTTP provider. name is how it is registered/selected; model is
// the default used when a Request does not set one.
func NewHTTP(name, baseURL, apiKey, model string) HTTP {
	return HTTP{
		name: name, baseURL: baseURL, apiKey: apiKey, model: model,
		client: &http.Client{Timeout: httpTimeout},
	}
}

// Name identifies the provider.
func (h HTTP) Name() string { return h.name }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	ResponseFormat *responseFmt  `json:"response_format,omitempty"`
}

type responseFmt struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Model string `json:"model"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Complete sends the system+prompt as a chat completion and returns the reply.
func (h HTTP) Complete(ctx context.Context, req Request) (Response, error) {
	model := req.Model
	if model == "" {
		model = h.model
	}
	msgs := make([]chatMessage, 0, 2)
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.System})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: req.Prompt})
	cr := chatRequest{Model: model, Messages: msgs}
	if len(req.Schema) > 0 {
		cr.ResponseFormat = &responseFmt{Type: "json_object"}
	}

	body, err := json.Marshal(cr)
	if err != nil {
		return Response{}, fmt.Errorf("ai: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/chat/completions", bytes.NewReader(body)) // #nosec G107 -- operator-configured provider endpoint
	if err != nil {
		return Response{}, fmt.Errorf("ai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if h.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+h.apiKey)
	}

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("ai: call %s: %w", h.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Response{}, fmt.Errorf("ai: read response: %w", err)
	}

	var cresp chatResponse
	if err := json.Unmarshal(raw, &cresp); err != nil {
		return Response{}, fmt.Errorf("ai: decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if cresp.Error != nil {
			return Response{}, fmt.Errorf("ai: provider error (status %d): %s", resp.StatusCode, cresp.Error.Message)
		}
		return Response{}, fmt.Errorf("ai: provider status %d", resp.StatusCode)
	}
	if len(cresp.Choices) == 0 {
		return Response{}, fmt.Errorf("ai: provider returned no choices")
	}
	content := cresp.Choices[0].Message.Content
	out := Response{Model: model}
	if cresp.Model != "" {
		out.Model = cresp.Model
	}
	if len(req.Schema) > 0 {
		if !json.Valid([]byte(content)) {
			return Response{}, fmt.Errorf("ai: structured request but reply was not JSON")
		}
		out.Structured = json.RawMessage(content)
	} else {
		out.Text = content
	}
	return out, nil
}
