// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ai is the pluggable LLM-provider boundary used by the Agent Manager
// and the Copilot. Providers (Claude/OpenAI/Gemini/Ollama) are
// swappable behind Provider; a Stub keeps the core buildable and testable
// without network or credentials.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Request is a single completion. When Schema is set, providers must return
// Structured JSON conforming to it (the AI node's structured-output mode). When
// Tools is set the provider may answer with ToolCalls instead of a final reply;
// History carries the prior assistant/tool turns of an in-progress tool-calling
// loop so each call reconstructs the full conversation.
type Request struct {
	Model   string          `json:"model,omitempty"`
	System  string          `json:"system,omitempty"`
	Prompt  string          `json:"prompt"`
	Schema  json.RawMessage `json:"schema,omitempty"`
	Tools   []Tool          `json:"tools,omitempty"`
	History []Message       `json:"history,omitempty"`
}

// Response is the provider's reply. Structured is set when a Schema was given;
// ToolCalls is set when the model wants to call tools before answering.
type Response struct {
	Text       string          `json:"text,omitempty"`
	Structured json.RawMessage `json:"structured,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	Model      string          `json:"model,omitempty"`
}

// Tool is a function the model may call during a completion. Parameters is a JSON
// Schema describing the arguments object.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall is the model's request to invoke a tool with JSON arguments.
type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Message is one prior turn fed back to the model during a tool-calling loop:
// an assistant turn that issued ToolCalls, or a tool turn carrying a call result.
type Message struct {
	Role       string     `json:"role"` // "assistant" or "tool"
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// Provider is one LLM backend.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
}

// Chunk is one streamed delta of a completion's text.
type Chunk struct {
	Text string `json:"text"`
}

// StreamHandler receives streamed deltas as they arrive.
type StreamHandler func(Chunk)

// StreamingProvider streams a completion token-by-token, invoking onChunk for
// each delta, and returns the final aggregated Response so the run is still
// recorded in full. A provider that does not implement this is used
// non-streaming (the caller emits the final text as a single chunk).
type StreamingProvider interface {
	Provider
	Stream(ctx context.Context, req Request, onChunk StreamHandler) (Response, error)
}

// Registry holds the available providers and the default selection.
type Registry struct {
	providers map[string]Provider
	def       string
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry { return &Registry{providers: make(map[string]Provider)} }

// Register adds p; the first registered provider becomes the default.
func (r *Registry) Register(p Provider) {
	r.providers[p.Name()] = p
	if r.def == "" {
		r.def = p.Name()
	}
}

// Get returns the named provider (or the default when name is empty).
func (r *Registry) Get(name string) (Provider, error) {
	if name == "" {
		name = r.def
	}
	if name == "" {
		return nil, errors.New("ai: no providers registered")
	}
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("ai: unknown provider %q", name)
	}
	return p, nil
}

// Stub is a deterministic provider for development and tests.
type Stub struct{}

func (Stub) Name() string { return "stub" }

// Complete echoes a canned response; if a schema is requested it returns an
// empty JSON object so callers can exercise the structured path.
func (Stub) Complete(_ context.Context, req Request) (Response, error) {
	if len(req.Schema) > 0 {
		return Response{Structured: json.RawMessage(`{}`), Model: "stub"}, nil
	}
	return Response{Text: "stub: " + req.Prompt, Model: "stub"}, nil
}

// Stream streams the canned text word-by-word, so the streaming path is testable
// without a network. A structured request is not token-streamed — the whole
// object is the final Response (no chunks).
func (s Stub) Stream(_ context.Context, req Request, onChunk StreamHandler) (Response, error) {
	if len(req.Schema) > 0 {
		return Response{Structured: json.RawMessage(`{}`), Model: "stub"}, nil
	}
	var b strings.Builder
	for i, word := range strings.Fields("stub: " + req.Prompt) {
		delta := word
		if i > 0 {
			delta = " " + word
		}
		onChunk(Chunk{Text: delta})
		b.WriteString(delta)
	}
	return Response{Text: b.String(), Model: "stub"}, nil
}
