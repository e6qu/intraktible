// SPDX-License-Identifier: AGPL-3.0-or-later

package ai_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/platform/ai"
)

func TestStubComplete(t *testing.T) {
	var p ai.Provider = ai.Stub{}
	if p.Name() != "stub" {
		t.Fatalf("name = %q, want stub", p.Name())
	}
	text, err := p.Complete(context.Background(), ai.Request{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if text.Text != "stub: hi" || text.Structured != nil {
		t.Fatalf("text mode: %+v", text)
	}
	structured, err := p.Complete(context.Background(), ai.Request{Prompt: "hi", Schema: []byte(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(structured.Structured) != "{}" || structured.Text != "" {
		t.Fatalf("schema mode: %+v", structured)
	}
}

func TestRegistry(t *testing.T) {
	r := ai.NewRegistry()
	if _, err := r.Get(""); err == nil {
		t.Fatal("empty registry must error on Get")
	}
	r.Register(ai.Stub{})

	if p, err := r.Get(""); err != nil || p.Name() != "stub" {
		t.Fatalf("default provider: %v / %v", p, err)
	}
	if p, err := r.Get("stub"); err != nil || p.Name() != "stub" {
		t.Fatalf("named provider: %v / %v", p, err)
	}
	if _, err := r.Get("nope"); err == nil {
		t.Fatal("unknown provider must error")
	}
}
