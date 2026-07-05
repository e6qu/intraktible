// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/ai"
)

// scriptedProvider is a content-addressed ai.Provider: every prompt the seed will
// send has a handcrafted response (or a designated error) registered up front, so
// seeded agent runs and AI-node resolutions record REAL provider round-trips with
// rich, prompt-specific outputs. An unknown prompt is a hard error — the seeder
// fails fast rather than record generic filler.
type scriptedProvider struct {
	clk *scriptedClock

	mu       sync.Mutex
	texts    map[string]string
	objects  map[string]map[string]any
	failures map[string]string
	// gate, when set for a prompt, blocks the completion until the seeder releases
	// it — how the one perpetually-"running" async run is left mid-flight in the
	// exported history.
	gate       map[string]chan struct{}
	gateResult map[string]string // error text returned once released
}

func newScriptedProvider(clk *scriptedClock) *scriptedProvider {
	return &scriptedProvider{
		clk:        clk,
		texts:      map[string]string{},
		objects:    map[string]map[string]any{},
		failures:   map[string]string{},
		gate:       map[string]chan struct{}{},
		gateResult: map[string]string{},
	}
}

func (p *scriptedProvider) Name() string { return "anthropic" }

func (p *scriptedProvider) text(prompt, out string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.texts[prompt] = out
}

func (p *scriptedProvider) object(prompt string, out map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.objects[prompt] = out
}

func (p *scriptedProvider) fail(prompt, errText string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures[prompt] = errText
}

// block registers a prompt whose completion parks until release() is called; the
// released completion returns errText (the run then records as failed — but the
// seed history is exported while it is still running).
func (p *scriptedProvider) block(prompt, errText string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gate[prompt] = make(chan struct{})
	p.gateResult[prompt] = errText
}

func (p *scriptedProvider) release(prompt string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch, ok := p.gate[prompt]; ok {
		close(ch)
		delete(p.gate, prompt)
	}
}

// Complete serves the registered response for the prompt. Token usage is derived
// from the prompt/output lengths so run-cost rollups have real numbers, and the
// scripted clock advances by an LLM-shaped latency so decisions and runs that
// crossed a model carry believable durations.
func (p *scriptedProvider) Complete(_ context.Context, req ai.Request) (ai.Response, error) {
	p.mu.Lock()
	gate, gated := p.gate[req.Prompt]
	gateErr := p.gateResult[req.Prompt]
	failText, failed := p.failures[req.Prompt]
	text, hasText := p.texts[req.Prompt]
	obj, hasObj := p.objects[req.Prompt]
	p.mu.Unlock()

	if gated {
		<-gate
		return ai.Response{}, errors.New(gateErr)
	}
	p.clk.Advance(140*time.Millisecond + time.Duration(len(req.Prompt)%7)*20*time.Millisecond)
	if failed {
		return ai.Response{}, errors.New(failText)
	}
	usage := func(outLen int) ai.Usage {
		return ai.Usage{
			PromptTokens:     (len(req.System) + len(req.Prompt)) / 4,
			CompletionTokens: outLen / 4,
		}
	}
	switch {
	case hasObj:
		b, err := json.Marshal(obj)
		if err != nil {
			return ai.Response{}, fmt.Errorf("scripted provider: marshal output for %q: %w", req.Prompt, err)
		}
		return ai.Response{Structured: b, Model: req.Model, Usage: usage(len(b))}, nil
	case hasText:
		return ai.Response{Text: text, Model: req.Model, Usage: usage(len(text))}, nil
	default:
		return ai.Response{}, fmt.Errorf("scripted provider: no scripted output for prompt %q", req.Prompt)
	}
}
