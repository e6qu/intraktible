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

// A non-2xx with a NON-JSON body (a gateway/proxy 502 returning HTML) must report
// the status, not a misleading JSON decode error — status is checked before decode.
func TestHTTPProviderNonJSONErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer srv.Close()

	p := ai.NewHTTP("openai", srv.URL, "sk-test", "gpt-test")
	_, err := p.Complete(context.Background(), ai.Request{Prompt: "hi"})
	if err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("expected a clean 'status 502', got: %v", err)
	}
	if strings.Contains(err.Error(), "decode") || strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("status should be checked before decode; got a decode error: %v", err)
	}
}

func TestHTTPProviderToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		// The request must advertise the tool to the model.
		toolsArr, ok := req["tools"].([]any)
		if !ok || len(toolsArr) != 1 {
			t.Errorf("request tools = %v", req["tools"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-test","choices":[{"message":{"role":"assistant","content":null,` +
			`"tool_calls":[{"id":"c1","type":"function","function":{"name":"bureau","arguments":"{\"subject\":\"acme\"}"}}]}}]}`))
	}))
	defer srv.Close()

	p := ai.NewHTTP("openai", srv.URL, "sk-test", "gpt-test")
	resp, err := p.Complete(context.Background(), ai.Request{
		Prompt: "assess acme",
		Tools:  []ai.Tool{{Name: "bureau", Description: "credit bureau", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "bureau" {
		t.Fatalf("tool calls: %+v", resp.ToolCalls)
	}
	if string(resp.ToolCalls[0].Arguments) != `{"subject":"acme"}` {
		t.Fatalf("tool args: %s", resp.ToolCalls[0].Arguments)
	}
}

func TestHTTPProviderToolResultRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role       string `json:"role"`
				ToolCallID string `json:"tool_call_id"`
				ToolCalls  []any  `json:"tool_calls"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		// Expect: user, assistant(with tool_calls), tool(with tool_call_id).
		var sawAssistantCall, sawToolResult bool
		for _, m := range req.Messages {
			if m.Role == "assistant" && len(m.ToolCalls) == 1 {
				sawAssistantCall = true
			}
			if m.Role == "tool" && m.ToolCallID == "c1" {
				sawToolResult = true
			}
		}
		if !sawAssistantCall || !sawToolResult {
			t.Errorf("conversation not reconstructed: %+v", req.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-test","choices":[{"message":{"role":"assistant","content":"risk is 42"}}]}`))
	}))
	defer srv.Close()

	p := ai.NewHTTP("openai", srv.URL, "sk-test", "gpt-test")
	resp, err := p.Complete(context.Background(), ai.Request{
		Prompt: "assess acme",
		Tools:  []ai.Tool{{Name: "bureau"}},
		History: []ai.Message{
			{Role: "assistant", ToolCalls: []ai.ToolCall{{ID: "c1", Name: "bureau", Arguments: json.RawMessage(`{"subject":"acme"}`)}}},
			{Role: "tool", ToolCallID: "c1", Content: `{"risk":42}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "risk is 42" {
		t.Fatalf("final answer: %q", resp.Text)
	}
}

func TestHTTPProviderStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["stream"] != true {
			t.Errorf("expected stream=true, got %v", req["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range []string{
			`{"choices":[{"delta":{"content":"Hel"}}],"model":"gpt-test"}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"content":" world"}}]}`,
			"[DONE]",
		} {
			_, _ = w.Write([]byte("data: " + frame + "\n\n"))
		}
	}))
	defer srv.Close()

	p := ai.NewHTTP("openai", srv.URL, "sk-test", "gpt-test")
	var got []string
	resp, err := p.Stream(context.Background(), ai.Request{Prompt: "hi"}, func(c ai.Chunk) {
		got = append(got, c.Text)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "Hel|lo| world" {
		t.Fatalf("chunks = %v", got)
	}
	if resp.Text != "Hello world" {
		t.Fatalf("aggregated text = %q", resp.Text)
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
