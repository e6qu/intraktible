// SPDX-License-Identifier: AGPL-3.0-or-later

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/time/rate"
)

// Guardrails configures a guarding decorator over an AI provider: a per-provider
// rate limit, prompt/output PII redaction, structured-output field redaction, and
// jailbreak / prompt-injection detection on the input. A zero Guardrails is inert,
// so Guard only adds overhead for the protections an operator actually turns on.
type Guardrails struct {
	// RatePerSec / Burst bound how fast this provider may be called (0 = unlimited).
	// A call blocks (honoring ctx) until the limiter admits it.
	RatePerSec float64
	Burst      int
	// RedactPII scrubs emails / SSNs / phone numbers / card-like numbers from the
	// prompt before it leaves the process and from the model's free-text output.
	RedactPII bool
	// RedactFields names structured-output JSON fields whose values are masked in
	// the model's structured response (case-insensitive).
	RedactFields []string
	// BlockInjection rejects a call whose prompt matches a jailbreak / prompt-
	// injection pattern, failing the run loudly rather than forwarding it.
	BlockInjection bool
}

// Enabled reports whether any protection is configured (else Guard is a passthrough).
func (g Guardrails) Enabled() bool {
	return g.RatePerSec > 0 || g.RedactPII || len(g.RedactFields) > 0 || g.BlockInjection
}

const redacted = "[redacted]"

// piiPatterns match common free-text PII. Deliberately conservative (high-signal
// shapes) so redaction doesn't mangle ordinary prompt text.
var piiPatterns = []*regexp.Regexp{
	regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`), // email
	regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),                            // US SSN
	regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`),                           // card-like
	regexp.MustCompile(`\b\+?\d[\d\s().\-]{8,}\d\b`),                       // phone-like
}

// injectionPatterns match well-known jailbreak / prompt-injection phrasings. A
// guardrail, not a guarantee — it raises the cost of the obvious attacks.
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore (all |the )?(previous|prior|above) (instructions|prompts?)`),
	regexp.MustCompile(`(?i)disregard (all |the )?(previous|prior|above)`),
	regexp.MustCompile(`(?i)you are now (a |an )?(dan|developer mode|jailbroken)`),
	regexp.MustCompile(`(?i)(reveal|print|show|repeat) (your |the )?(system )?prompt`),
	regexp.MustCompile(`(?i)pretend (you are|to be) (not |un)?(restricted|filtered|an ai)`),
}

// ErrBlockedByGuardrail is returned when the input trips a prompt-injection rule.
var ErrBlockedByGuardrail = fmt.Errorf("ai: blocked by guardrail")

// guard wraps a provider with the configured guardrails.
type guard struct {
	inner   Provider
	cfg     Guardrails
	limiter *rate.Limiter
	fields  map[string]bool
}

// Guard wraps p with guardrails. With nothing configured it returns p unchanged,
// so the decorator never sits in the hot path unless a protection is on.
func Guard(p Provider, cfg Guardrails) Provider {
	if !cfg.Enabled() {
		return p
	}
	g := &guard{inner: p, cfg: cfg, fields: map[string]bool{}}
	if cfg.RatePerSec > 0 {
		burst := cfg.Burst
		if burst < 1 {
			burst = 1
		}
		g.limiter = rate.NewLimiter(rate.Limit(cfg.RatePerSec), burst)
	}
	for _, f := range cfg.RedactFields {
		g.fields[strings.ToLower(strings.TrimSpace(f))] = true
	}
	// A guarded streaming provider preserves streaming; a non-streaming one stays so.
	if _, ok := p.(StreamingProvider); ok {
		return &streamingGuard{guard: g}
	}
	return g
}

func (g *guard) Name() string { return g.inner.Name() }

// admit applies the rate limit and input checks/redaction, returning the request to
// forward (with the prompt redacted) or an error when blocked.
func (g *guard) admit(ctx context.Context, req Request) (Request, error) {
	if g.limiter != nil {
		if err := g.limiter.Wait(ctx); err != nil {
			return req, fmt.Errorf("ai: rate limit: %w", err)
		}
	}
	if g.cfg.BlockInjection && matchesAny(injectionPatterns, req.Prompt+"\n"+req.System) {
		return req, ErrBlockedByGuardrail
	}
	if g.cfg.RedactPII {
		req.Prompt = redactText(req.Prompt)
	}
	return req, nil
}

// guardResponse redacts the model's output per config.
func (g *guard) guardResponse(resp Response) Response {
	if g.cfg.RedactPII {
		resp.Text = redactText(resp.Text)
	}
	if len(g.fields) > 0 && len(resp.Structured) > 0 {
		resp.Structured = maskFields(resp.Structured, g.fields)
	}
	return resp
}

func (g *guard) Complete(ctx context.Context, req Request) (Response, error) {
	req, err := g.admit(ctx, req)
	if err != nil {
		return Response{}, err
	}
	resp, err := g.inner.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	return g.guardResponse(resp), nil
}

// streamingGuard preserves the StreamingProvider interface. Input guardrails (rate
// limit, injection block, prompt redaction) apply to streaming; output redaction is
// applied to the aggregated final Response (chunks are forwarded as they arrive —
// per-chunk redaction can't see PII spanning a chunk boundary).
type streamingGuard struct {
	*guard
}

func (g *streamingGuard) Stream(ctx context.Context, req Request, onChunk StreamHandler) (Response, error) {
	req, err := g.admit(ctx, req)
	if err != nil {
		return Response{}, err
	}
	resp, err := g.inner.(StreamingProvider).Stream(ctx, req, onChunk)
	if err != nil {
		return resp, err
	}
	return g.guardResponse(resp), nil
}

func matchesAny(patterns []*regexp.Regexp, s string) bool {
	for _, p := range patterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

// redactText replaces PII-shaped substrings with a placeholder.
func redactText(s string) string {
	for _, p := range piiPatterns {
		s = p.ReplaceAllString(s, redacted)
	}
	return s
}

// maskFields recursively replaces the values of named fields in a JSON object with
// the placeholder, leaving structure and non-secret fields intact. On any decode
// failure it returns the input unchanged (never drops the output).
func maskFields(raw json.RawMessage, fields map[string]bool) json.RawMessage {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	masked := maskValue(v, fields)
	out, err := json.Marshal(masked)
	if err != nil {
		return raw
	}
	return out
}

func maskValue(v any, fields map[string]bool) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if fields[strings.ToLower(k)] {
				t[k] = redacted
			} else {
				t[k] = maskValue(val, fields)
			}
		}
		return t
	case []any:
		for i := range t {
			t[i] = maskValue(t[i], fields)
		}
		return t
	default:
		return v
	}
}
