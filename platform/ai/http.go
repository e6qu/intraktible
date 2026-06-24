// SPDX-License-Identifier: AGPL-3.0-or-later

package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpTimeout bounds a single completion call. HTTPTimeout exposes it so the
// composition root can build an egress-guarded client with the matching timeout.
const httpTimeout = 60 * time.Second

// HTTPTimeout is the default single-completion-call timeout (see httpTimeout).
const HTTPTimeout = httpTimeout

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

// Option customizes an HTTP provider.
type Option func(*HTTP)

// WithHTTPClient injects the HTTP client the provider dials with — used at the
// composition root to supply the shared SSRF-safe egress client (dial-time IP
// blocking + cross-host redirect refusal), so a provider URL that redirects to a
// cloud metadata IP is blocked just like connector egress. The client's timeout
// should bound a single completion call.
func WithHTTPClient(c *http.Client) Option {
	return func(h *HTTP) {
		if c != nil {
			h.client = c
		}
	}
}

// NewHTTP builds an HTTP provider. name is how it is registered/selected; model is
// the default used when a Request does not set one. By default it dials with a
// plain client; pass WithHTTPClient at the composition root to enforce the egress
// guard.
func NewHTTP(name, baseURL, apiKey, model string, opts ...Option) HTTP {
	h := HTTP{
		name: name, baseURL: baseURL, apiKey: apiKey, model: model,
		client: &http.Client{Timeout: httpTimeout},
	}
	for _, opt := range opts {
		opt(&h)
	}
	return h
}

// Name identifies the provider.
func (h HTTP) Name() string { return h.name }

type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // a JSON-encoded string in the OpenAI API
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"` // always "function"
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Tools          []chatTool     `json:"tools,omitempty"`
	ResponseFormat *responseFmt   `json:"response_format,omitempty"`
	Stream         bool           `json:"stream,omitempty"`
	StreamOptions  *streamOptions `json:"stream_options,omitempty"`
}

// streamOptions opts a streaming request into a final usage frame; without it the
// OpenAI-compatible API omits token counts from the stream entirely.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatUsage is the OpenAI-compatible token accounting object.
type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// usage maps the wire accounting to the provider-neutral Usage.
func (u *chatUsage) usage() Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens}
}

// chatStreamChunk is one SSE delta frame from the streaming Chat Completions API.
// The final frame (when stream_options.include_usage is set) carries Usage with an
// empty Choices list.
type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Model string     `json:"model"`
	Usage *chatUsage `json:"usage,omitempty"`
}

type responseFmt struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Model string     `json:"model"`
	Usage *chatUsage `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// strptr wraps s so a chatMessage can carry a real (non-null) content string.
func strptr(s string) *string { return &s }

// buildMessages assembles the OpenAI message list from the request: system,
// user, then the tool-calling history (assistant tool-call turns and tool
// results) reconstructed each round.
func buildMessages(req Request) []chatMessage {
	msgs := make([]chatMessage, 0, 2+len(req.History))
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: strptr(req.System)})
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: strptr(req.Prompt)})
	for _, m := range req.History {
		switch m.Role {
		case "assistant":
			cm := chatMessage{Role: "assistant", Content: nil}
			for _, tc := range m.ToolCalls {
				var c chatToolCall
				c.ID, c.Type = tc.ID, "function"
				c.Function.Name = tc.Name
				c.Function.Arguments = string(tc.Arguments)
				cm.ToolCalls = append(cm.ToolCalls, c)
			}
			msgs = append(msgs, cm)
		case "tool":
			msgs = append(msgs, chatMessage{Role: "tool", ToolCallID: m.ToolCallID, Content: strptr(m.Content)})
		}
	}
	return msgs
}

// buildTools maps tool specs to the OpenAI function-tool shape.
func buildTools(tools []Tool) []chatTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]chatTool, 0, len(tools))
	for _, t := range tools {
		var ct chatTool
		ct.Type = "function"
		ct.Function.Name = t.Name
		ct.Function.Description = t.Description
		ct.Function.Parameters = t.Parameters
		out = append(out, ct)
	}
	return out
}

// Complete sends the system+prompt as a chat completion and returns the reply.
func (h HTTP) Complete(ctx context.Context, req Request) (Response, error) {
	model := req.Model
	if model == "" {
		model = h.model
	}
	cr := chatRequest{Model: model, Messages: buildMessages(req), Tools: buildTools(req.Tools)}
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

	// Check status BEFORE decoding: a 429/5xx/proxy error body is commonly non-JSON
	// (HTML/plain text), so decoding first would mask the real failure (e.g. a 502)
	// behind a misleading "invalid character '<'" parse error. Decode the error body
	// only opportunistically for a structured provider message.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp chatResponse
		if json.Unmarshal(raw, &errResp) == nil && errResp.Error != nil {
			return Response{}, fmt.Errorf("ai: provider error (status %d): %s", resp.StatusCode, errResp.Error.Message)
		}
		return Response{}, fmt.Errorf("ai: provider status %d", resp.StatusCode)
	}
	var cresp chatResponse
	if err := json.Unmarshal(raw, &cresp); err != nil {
		return Response{}, fmt.Errorf("ai: decode response (status %d): %w", resp.StatusCode, err)
	}
	if len(cresp.Choices) == 0 {
		return Response{}, fmt.Errorf("ai: provider returned no choices")
	}
	msg := cresp.Choices[0].Message
	out := Response{Model: model, Usage: cresp.Usage.usage()}
	if cresp.Model != "" {
		out.Model = cresp.Model
	}
	// Tool calls take precedence: the model wants to call tools before answering.
	if len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments),
			})
		}
		return out, nil
	}
	content := ""
	if msg.Content != nil {
		content = *msg.Content
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

// Stream sends a streaming Chat Completion (`stream: true`) and parses the SSE
// frames, invoking onChunk for each text delta and returning the aggregated
// Response. A structured request is aggregated then validated as JSON.
func (h HTTP) Stream(ctx context.Context, req Request, onChunk StreamHandler) (Response, error) {
	model := req.Model
	if model == "" {
		model = h.model
	}
	cr := chatRequest{
		Model: model, Messages: buildMessages(req), Tools: buildTools(req.Tools),
		Stream: true, StreamOptions: &streamOptions{IncludeUsage: true},
	}
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
	httpReq.Header.Set("Accept", "text/event-stream")
	if h.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+h.apiKey)
	}
	resp, err := h.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("ai: call %s: %w", h.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("ai: provider status %d", resp.StatusCode)
	}

	var b strings.Builder
	var usage Usage
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // ignore comments/blank lines and non-data SSE fields
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Response{}, fmt.Errorf("ai: decode stream chunk: %w", err)
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = chunk.Usage.usage() // final frame's token accounting
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				onChunk(Chunk{Text: ch.Delta.Content})
				b.WriteString(ch.Delta.Content)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Response{}, fmt.Errorf("ai: read stream: %w", err)
	}
	out := Response{Model: model, Usage: usage}
	if len(req.Schema) > 0 {
		if !json.Valid([]byte(b.String())) {
			return Response{}, fmt.Errorf("ai: structured stream but reply was not JSON")
		}
		out.Structured = json.RawMessage(b.String())
	} else {
		out.Text = b.String()
	}
	return out, nil
}
