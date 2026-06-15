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

func TestStubStream(t *testing.T) {
	var sp ai.StreamingProvider = ai.Stub{}
	var chunks []string
	resp, err := sp.Stream(context.Background(), ai.Request{Prompt: "hello world"}, func(c ai.Chunk) {
		chunks = append(chunks, c.Text)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Word-by-word, concatenating to the same text Complete would return.
	if len(chunks) != 3 {
		t.Fatalf("chunks = %v, want 3", chunks)
	}
	if resp.Text != "stub: hello world" {
		t.Fatalf("aggregated = %q", resp.Text)
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
