// SPDX-License-Identifier: AGPL-3.0-or-later

package ai_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/ai"
)

// FuzzAIStream feeds arbitrary bytes as a streaming provider's SSE response body —
// a compromised or buggy LLM gateway — and asserts the hand-rolled SSE parser is
// robust: it never panics (over-long lines, embedded NULs, truncated/garbage
// `data:` frames, missing [DONE]), and on a clean parse the aggregated text equals
// exactly the concatenation of the deltas handed to onChunk (no drift between the
// streamed and the returned text).
func FuzzAIStream(f *testing.F) {
	f.Add("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	f.Add("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\ndata: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n")
	f.Add(": ping\n\ndata: not-json\n")
	f.Add("garbage with no data prefix\n\x00\n")
	f.Fuzz(func(t *testing.T, body string) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()

		p := ai.NewHTTP("openai", srv.URL, "sk-test", "gpt-test")
		var streamed strings.Builder
		resp, err := p.Stream(context.Background(), ai.Request{Prompt: "x"}, func(c ai.Chunk) {
			streamed.WriteString(c.Text)
		})
		if err != nil {
			return // a malformed frame fails loudly — acceptable, not a crash
		}
		if resp.Text != streamed.String() {
			t.Fatalf("aggregated text %q != streamed deltas %q", resp.Text, streamed.String())
		}
	})
}
