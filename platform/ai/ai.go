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
)

// Request is a single completion. When Schema is set, providers must return
// Structured JSON conforming to it (the AI node's structured-output mode).
type Request struct {
	Model  string          `json:"model,omitempty"`
	System string          `json:"system,omitempty"`
	Prompt string          `json:"prompt"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

// Response is the provider's reply. Structured is set when a Schema was given.
type Response struct {
	Text       string          `json:"text,omitempty"`
	Structured json.RawMessage `json:"structured,omitempty"`
	Model      string          `json:"model,omitempty"`
}

// Provider is one LLM backend.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
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
