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
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Tools          []chatTool    `json:"tools,omitempty"`
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
	msg := cresp.Choices[0].Message
	out := Response{Model: model}
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
