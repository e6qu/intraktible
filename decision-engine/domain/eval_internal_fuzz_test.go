// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// FuzzEvalAny fuzzes OUR expr-lang wrapper directly: an arbitrary expression
// string against an arbitrary input env must never panic, must respect the
// per-node wall-clock deadline (no hang), and must return an error rather than
// crash on malformed/cyclic/huge input. It exercises both the inline (no
// deadline) and goroutine (deadline) paths of runProgram, which the public
// Execute fuzz never reaches with a budget.
func FuzzEvalAny(f *testing.F) {
	seeds := []struct {
		code string
		env  string
	}{
		{"score > 1", `{"score":5}`},
		{"a + b * c", `{"a":1,"b":2,"c":3}`},
		{"all(items, # > 0)", `{"items":[1,2,3]}`},
		{"len(s) > 0 ? s : 'x'", `{"s":"hi"}`},
		{"1..1000000 | filter(# > 0) | len()", `{}`},
		{"", `{}`},
	}
	for _, s := range seeds {
		f.Add(s.code, s.env)
	}
	f.Fuzz(func(t *testing.T, code, envJSON string) {
		if !json.Valid([]byte(envJSON)) {
			return
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(envJSON), &env); err != nil {
			return
		}
		if env == nil {
			env = map[string]any{}
		}
		// Inline (no-deadline) path.
		_, _ = evalAny(evalContext{ctx: context.Background()}, code, env)

		// Deadline path: a pathological expression must be cut off and return an
		// error, not hang the test past the budget. A generous outer timeout
		// guards the test process itself against a regression in the watchdog.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		out := make(chan struct{}, 1)
		go func() {
			_, _ = evalAny(evalContext{ctx: ctx}, code, env)
			out <- struct{}{}
		}()
		select {
		case <-out:
		case <-time.After(5 * time.Second):
			t.Fatalf("evalAny did not return within budget for code %q", code)
		}
	})
}

// FuzzEvalCode fuzzes the Starlark Code node directly: arbitrary source against
// arbitrary inputs must be sandboxed, bounded (step + wall-clock), and panic-free.
// Bad code returns an error; a runaway loop is cut off, not allowed to hang.
func FuzzEvalCode(f *testing.F) {
	seeds := []struct {
		code string
		env  string
	}{
		{"y = data['n'] + 1", `{"n":3}`},
		{"total = 0\nfor i in range(10):\n  total = total + i", `{}`},
		{"def f(x):\n  return x*2\ny = f(21)", `{}`},
		{"while True:\n  pass", `{}`},
		{"x = [0]*999999999", `{}`},
		{"", `{}`},
	}
	for _, s := range seeds {
		f.Add(s.code, s.env)
	}
	f.Fuzz(func(t *testing.T, code, envJSON string) {
		if !json.Valid([]byte(envJSON)) {
			return
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(envJSON), &env); err != nil {
			return
		}
		if env == nil {
			env = map[string]any{}
		}
		cfg, err := json.Marshal(map[string]any{"code": code})
		if err != nil {
			return
		}
		n := events.Node{ID: "c", Type: events.NodeCode, Config: cfg}

		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		out := make(chan struct{}, 1)
		go func() {
			_, _, _ = evalCode(evalContext{ctx: ctx}, n, cloneContext(env), nil)
			out <- struct{}{}
		}()
		select {
		case <-out:
		case <-time.After(10 * time.Second):
			t.Fatalf("evalCode did not return within budget for code %q", code)
		}
	})
}
