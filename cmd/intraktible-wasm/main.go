// SPDX-License-Identifier: AGPL-3.0-or-later
// The browser deployment target: the SAME assembled backend the native binary
// serves (server.New — every module, middleware, and route), hosted in a Web
// Worker and driven over a message port instead of a TCP socket. State is an
// in-memory event log seeded from a pre-recorded history (replayed through the
// same projection runtime a native restart uses) plus the visitor's own delta,
// which the page persists and replays on reload. No demo forks: the only
// difference from production is the transport and the storage medium.
//
//	boot(seedJSON, deltaJSON)              -> replays and mounts the handler
//	handle(method, url, hdrs, body, onHeader, onChunk) -> Promise (streams)
//	exportDelta()                          -> JSON of events past the seed head

//go:build js

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall/js"

	"github.com/e6qu/intraktible/platform/ai"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/server"
)

type host struct {
	log      *eventlog.MemoryLog
	seedHead uint64
	srv      *server.Server
}

// workspaceProvider serves the seeded workspace's provider name with the
// page-backed completion hook. The seed's agents were authored against the
// "anthropic" provider (cmd/intraktible-seed registers its scripted provider
// under that name so the recorded history reads authentically), and an agent
// version pins its provider by name — so the browser shell must answer to the
// same name, exactly as a native operator points INTRAKTIBLE_AI_PROVIDER=
// "anthropic" at a compatible endpoint. Embedding keeps JSProvider's Stream,
// so guarded streaming stays intact.
type workspaceProvider struct{ ai.JSProvider }

func (workspaceProvider) Name() string { return "anthropic" }

func main() {
	h := &host{}
	js.Global().Set("__intraktible", js.ValueOf(map[string]any{
		"boot":        js.FuncOf(h.boot),
		"handle":      js.FuncOf(h.handle),
		"exportDelta": js.FuncOf(h.exportDelta),
	}))
	select {} // serve until the worker is terminated
}

// boot parses the seed and delta histories, rebuilds the event log, and
// assembles the backend — the exact code path of a native restart over a WAL.
func (h *host) boot(_ js.Value, args []js.Value) any {
	seed, delta := args[0].String(), args[1].String()
	events, err := parseHistory(seed, delta)
	if err != nil {
		panic(err) // surfaces as a boot-error in the worker
	}
	log, err := eventlog.NewMemoryFrom(events)
	if err != nil {
		panic(err)
	}
	var seedEvents []eventlog.Envelope
	if err := json.Unmarshal([]byte(seed), &seedEvents); err != nil {
		panic(fmt.Errorf("wasm boot: seed history: %w", err))
	}
	st := store.NewMemory()
	srv, err := server.New(context.Background(), server.Config{
		Modules:   "all",
		DevAPIKey: "dev-sandbox-key",
		StoreKind: "memory",
		// The page registers the completion hook (ai-sim.ts) — a first-class
		// provider, wired here exactly like the native shell wires HTTP/stub.
		AIProvider: workspaceProvider{ai.NewJS()},
	}, log, st)
	if err != nil {
		panic(err)
	}
	h.log, h.seedHead, h.srv = log, uint64(len(seedEvents)), srv
	return nil
}

// parseHistory concatenates the seed and the visitor's delta into one
// contiguous history (the delta was recorded immediately after the seed, so
// its seqs continue where the seed ends — NewMemoryFrom re-validates).
func parseHistory(seed, delta string) ([]eventlog.Envelope, error) {
	var events []eventlog.Envelope
	if err := json.Unmarshal([]byte(seed), &events); err != nil {
		return nil, fmt.Errorf("wasm boot: seed history: %w", err)
	}
	if delta != "" {
		var d []eventlog.Envelope
		if err := json.Unmarshal([]byte(delta), &d); err != nil {
			return nil, fmt.Errorf("wasm boot: delta history: %w", err)
		}
		events = append(events, d...)
	}
	return events, nil
}

// handle serves one request through the assembled handler, streaming the
// response: onHeader fires at WriteHeader, onChunk per Write, and the returned
// Promise resolves when the handler returns.
func (h *host) handle(_ js.Value, args []js.Value) any {
	method, rawURL := args[0].String(), args[1].String()
	headers, body, onHeader, onChunk := args[2], args[3], args[4], args[5]

	var bodyReader *strings.Reader
	var bodyBytes []byte
	if !body.IsNull() && !body.IsUndefined() {
		bodyBytes = make([]byte, body.Get("length").Int())
		js.CopyBytesToGo(bodyBytes, body)
	}
	bodyReader = strings.NewReader(string(bodyBytes))

	req := httptest.NewRequest(method, rawURL, bodyReader)
	for i := 0; i < headers.Get("length").Int(); i++ {
		pair := headers.Index(i)
		req.Header.Add(pair.Index(0).String(), pair.Index(1).String())
	}

	promiseCtor := js.Global().Get("Promise")
	return promiseCtor.New(js.FuncOf(func(_ js.Value, p []js.Value) any {
		resolve, reject := p[0], p[1]
		go func() {
			defer func() {
				if r := recover(); r != nil {
					reject.Invoke(js.Global().Get("Error").New(fmt.Sprint(r)))
				}
			}()
			w := &streamWriter{onHeader: onHeader, onChunk: onChunk, header: http.Header{}}
			h.srv.Handler.ServeHTTP(w, req)
			w.ensureHeader()
			resolve.Invoke(js.Undefined())
		}()
		return nil
	}))
}

func (h *host) exportDelta(js.Value, []js.Value) any {
	all := h.log.Export()
	delta := all[min(int(h.seedHead), len(all)):]
	b, err := json.Marshal(delta)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// streamWriter bridges http.ResponseWriter to the worker's callbacks.
type streamWriter struct {
	onHeader    js.Value
	onChunk     js.Value
	header      http.Header
	wroteHeader bool
}

func (w *streamWriter) Header() http.Header { return w.header }

func (w *streamWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	pairs := make([]any, 0, len(w.header))
	for k, vs := range w.header {
		for _, v := range vs {
			pairs = append(pairs, []any{k, v})
		}
	}
	w.onHeader.Invoke(status, pairs)
}

func (w *streamWriter) Write(b []byte) (int, error) {
	w.ensureHeader()
	u8 := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(u8, b)
	w.onChunk.Invoke(u8)
	return len(b), nil
}

// Flush satisfies http.Flusher so SSE handlers stream instead of buffering.
// Chunks are already pushed eagerly in Write, so there is nothing to do.
func (w *streamWriter) Flush() {}

func (w *streamWriter) ensureHeader() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
}
