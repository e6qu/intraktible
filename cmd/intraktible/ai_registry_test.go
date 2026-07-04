// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/platform/ai"
)

// A server with no AI configuration must register NO provider: the canned Stub
// silently standing in for a model was fake execution (agent runs, AI nodes,
// and the copilot recorded canned output as authentic).
func TestAIRegistryDefaultsToNoProviders(t *testing.T) {
	t.Setenv("INTRAKTIBLE_AI_BASE_URL", "")
	t.Setenv("INTRAKTIBLE_AI_STUB", "")
	reg := buildAIRegistry(ai.Guardrails{}, connectors.EgressPolicy{})
	if _, err := reg.Get(""); err == nil || !strings.Contains(err.Error(), "no providers registered") {
		t.Fatalf("want loud no-provider error, got %v", err)
	}
}

func TestAIRegistryStubIsOptIn(t *testing.T) {
	t.Setenv("INTRAKTIBLE_AI_BASE_URL", "")
	t.Setenv("INTRAKTIBLE_AI_STUB", "1")
	reg := buildAIRegistry(ai.Guardrails{}, connectors.EgressPolicy{})
	p, err := reg.Get("")
	if err != nil || p.Name() != "stub" {
		t.Fatalf("want opted-in stub as default, got %v / %v", p, err)
	}
}

func TestAIRegistryHTTPWithoutStub(t *testing.T) {
	t.Setenv("INTRAKTIBLE_AI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("INTRAKTIBLE_AI_STUB", "")
	reg := buildAIRegistry(ai.Guardrails{}, connectors.EgressPolicy{})
	if _, err := reg.Get("stub"); err == nil {
		t.Fatal("the stub must not ride along with a real provider unless opted in")
	}
	if p, err := reg.Get(""); err != nil || p.Name() != "openai" {
		t.Fatalf("want the HTTP provider as default, got %v / %v", p, err)
	}
}
