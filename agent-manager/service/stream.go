// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/httpx"
)

// runStreamSSE streams an agent run over Server-Sent Events: a `chunk` event per
// text delta, then a terminal `done` event with the recorded run. The prompt is a
// query param because EventSource is GET-only. Auth is the session cookie / API
// key (the /v1 chain), like every other endpoint.
//
//	GET /v1/agents/{name}/run/stream?prompt=...
func (s *Service) runStreamSSE(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	onChunk := func(c ai.Chunk) {
		writeSSE(w, "chunk", c)
		flusher.Flush()
	}
	res, err := s.cmd.StreamRun(r.Context(), id, r.PathValue("name"), r.URL.Query().Get("prompt"), onChunk)
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
		return
	}
	writeSSE(w, "done", map[string]any{
		"run_id": res.RunID, "status": res.Status, "text": res.Text,
		"structured": res.Structured, "error": res.Error,
	})
	flusher.Flush()
}

func writeSSE(w io.Writer, event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

// runStreamWS streams an agent run over a WebSocket: the client sends one
// {"prompt": ...} message, then receives {"type":"chunk","text":...} messages and
// a final {"type":"done", ...}. Origin verification is skipped because the session
// cookie is SameSite=Lax (so a cross-origin WS handshake carries no credentials)
// and the handler still requires authentication.
//
//	GET /v1/agents/{name}/run/ws
func (s *Service) runStreamWS(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.Caller(w, r)
	if !ok {
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return // Accept has already written the failure response
	}
	defer func() { _ = c.CloseNow() }()
	ctx := r.Context()

	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := wsjson.Read(ctx, c, &req); err != nil {
		_ = c.Close(websocket.StatusUnsupportedData, "expected a {prompt} message")
		return
	}
	onChunk := func(ch ai.Chunk) {
		_ = wsjson.Write(ctx, c, map[string]any{"type": "chunk", "text": ch.Text})
	}
	res, err := s.cmd.StreamRun(ctx, id, r.PathValue("name"), req.Prompt, onChunk)
	if err != nil {
		_ = wsjson.Write(ctx, c, map[string]any{"type": "error", "error": err.Error()})
		_ = c.Close(websocket.StatusInternalError, "run error")
		return
	}
	_ = wsjson.Write(ctx, c, map[string]any{
		"type": "done", "run_id": res.RunID, "status": res.Status, "text": res.Text,
		"structured": res.Structured, "error": res.Error,
	})
	_ = c.Close(websocket.StatusNormalClosure, "")
}
