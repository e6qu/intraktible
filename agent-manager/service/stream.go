// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/e6qu/intraktible/agent-manager/agents"
	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/httpx"
)

// wsPromptTimeout bounds how long a connected WebSocket client may take to send its
// opening {prompt} message, so a connected-but-silent client cannot pin a goroutine
// and socket indefinitely.
const wsPromptTimeout = 30 * time.Second

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
	// Resolve the agent before committing the 200 + event-stream headers, so an
	// unknown agent is a real 404 (not a 200 carrying an SSE `error` frame, which
	// would also mislead the metrics middleware into counting it as success).
	name := r.PathValue("name")
	if _, found, err := agents.Read(r.Context(), s.store, id, name); err != nil {
		httpx.Error(w, http.StatusInternalServerError, err)
		return
	} else if !found {
		httpx.Error(w, http.StatusNotFound, fmt.Errorf("unknown agent %q", name))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Cancel the run if a chunk write fails (the client disconnected): otherwise the
	// provider stream keeps producing into a dead socket for the rest of the run.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	onChunk := func(c ai.Chunk) {
		if err := writeSSE(w, "chunk", c); err != nil {
			cancel()
			return
		}
		flusher.Flush()
	}
	res, err := s.cmd.StreamRun(ctx, id, name, r.URL.Query().Get("prompt"), onChunk)
	if err != nil {
		_ = writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
		return
	}
	_ = writeSSE(w, "done", map[string]any{
		"run_id": res.RunID, "status": res.Status, "text": res.Text,
		"structured": res.Structured, "error": res.Error,
	})
	flusher.Flush()
}

// writeSSE writes one SSE frame and returns any write error so the caller can stop
// streaming into a disconnected client.
func writeSSE(w io.Writer, event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	return err
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
	// Cancellable so a failed chunk write (client gone) stops the run rather than
	// streaming into a dead socket — mirrors the SSE path.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var req struct {
		Prompt string `json:"prompt"`
	}
	// Bound the opening read so a connected-but-silent client can't hold the
	// goroutine + socket open indefinitely.
	readCtx, cancelRead := context.WithTimeout(ctx, wsPromptTimeout)
	err = wsjson.Read(readCtx, c, &req)
	cancelRead()
	if err != nil {
		_ = c.Close(websocket.StatusUnsupportedData, "expected a {prompt} message")
		return
	}
	onChunk := func(ch ai.Chunk) {
		if err := wsjson.Write(ctx, c, map[string]any{"type": "chunk", "text": ch.Text}); err != nil {
			cancel() // client disconnected — stop the run instead of producing into a dead socket
		}
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
