// SPDX-License-Identifier: AGPL-3.0-or-later

package ai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/ai"
)

// chatServer is a minimal OpenAI-compatible mock: it echoes a reply, and when the
// request asks for a JSON object it replies with JSON content.
func chatServer(t *testing.T, status int, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("missing/wrong auth header: %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["model"] != "gpt-test" {
			t.Errorf("model = %v, want gpt-test", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"model":"gpt-test","choices":[{"message":{"role":"assistant","content":` +
			mustJSONString(content) + `}}]}`))
	}))
}

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestHTTPProviderText(t *testing.T) {
	srv := chatServer(t, http.StatusOK, "hello from the model")
	defer srv.Close()

	p := ai.NewHTTP("openai", srv.URL, "sk-test", "gpt-test")
	if p.Name() != "openai" {
		t.Fatalf("name = %q", p.Name())
	}
	resp, err := p.Complete(context.Background(), ai.Request{System: "be terse", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello from the model" || resp.Structured != nil {
		t.Fatalf("text response: %+v", resp)
	}
}

func TestHTTPProviderStructured(t *testing.T) {
	srv := chatServer(t, http.StatusOK, `{"risk":"high"}`)
	defer srv.Close()

	p := ai.NewHTTP("openai", srv.URL, "sk-test", "gpt-test")
	resp, err := p.Complete(context.Background(), ai.Request{Prompt: "assess", Schema: []byte(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "" || string(resp.Structured) != `{"risk":"high"}` {
		t.Fatalf("structured response: %+v", resp)
	}
}

func TestHTTPProviderErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	p := ai.NewHTTP("openai", srv.URL, "sk-test", "gpt-test")
	_, err := p.Complete(context.Background(), ai.Request{Prompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("expected the provider error to surface, got: %v", err)
	}
}

// The provider plugs into the registry like any other.
func TestHTTPProviderInRegistry(t *testing.T) {
	r := ai.NewRegistry()
	r.Register(ai.NewHTTP("openai", "http://x", "k", "m"))
	r.Register(ai.Stub{})
	p, err := r.Get("") // first registered is the default
	if err != nil || p.Name() != "openai" {
		t.Fatalf("default provider = %v err=%v", p, err)
	}
}
