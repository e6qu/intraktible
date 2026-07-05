// SPDX-License-Identifier: AGPL-3.0-or-later
// The JS-bridged AI provider for the wasm deployment target: completions are
// produced by a hook the hosting page registers (globalThis.__intraktible_ai),
// exactly as the HTTP provider delegates to an OpenAI-compatible endpoint. The
// hook is a first-class provider choice the host wires explicitly — never a
// silent substitute (a missing hook fails the call loudly).

//go:build js

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"syscall/js"
)

// JSProvider delegates completions to the hosting page.
type JSProvider struct{}

// NewJS returns the provider; the hook is resolved per call so the page may
// register it after wasm boot.
func NewJS() JSProvider { return JSProvider{} }

func (JSProvider) Name() string { return "js" }

// Complete marshals the request to the page hook and decodes its response.
// The hook returns a Promise resolving to a Response-shaped object.
func (JSProvider) Complete(ctx context.Context, req Request) (Response, error) {
	hook := js.Global().Get("__intraktible_ai")
	if hook.IsUndefined() || hook.IsNull() {
		return Response{}, errors.New("ai: js provider has no __intraktible_ai hook registered")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("ai: js provider request: %w", err)
	}
	type outcome struct {
		resp Response
		err  error
	}
	done := make(chan outcome, 1)
	var ok, fail js.Func
	settle := func(o outcome) {
		done <- o
		ok.Release()
		fail.Release()
	}
	ok = js.FuncOf(func(_ js.Value, args []js.Value) any {
		var r Response
		if err := json.Unmarshal([]byte(args[0].String()), &r); err != nil {
			settle(outcome{err: fmt.Errorf("ai: js provider response: %w", err)})
			return nil
		}
		settle(outcome{resp: r})
		return nil
	})
	fail = js.FuncOf(func(_ js.Value, args []js.Value) any {
		settle(outcome{err: fmt.Errorf("ai: js provider: %s", args[0].Call("toString").String())})
		return nil
	})
	// The hook receives the Request JSON and must resolve with Response JSON.
	hook.Invoke(string(payload)).Call("then", ok).Call("catch", fail)
	select {
	case o := <-done:
		return o.resp, o.err
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
}

// Stream produces the completion via the hook, then emits it word-by-word —
// the text is generated locally by the page, so chunking it locally streams
// the same bytes the run records (identical to how Stub streams).
func (p JSProvider) Stream(ctx context.Context, req Request, onChunk StreamHandler) (Response, error) {
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return Response{}, err
	}
	if resp.Text != "" {
		var b strings.Builder
		for _, word := range strings.Fields(resp.Text) {
			delta := word
			if b.Len() > 0 {
				delta = " " + word
			}
			b.WriteString(delta)
			onChunk(Chunk{Text: delta})
		}
	}
	return resp, nil
}
