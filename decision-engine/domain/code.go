// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// maxCodeSteps bounds Code node execution so a runaway script fails loudly
// instead of hanging the decide path.
const maxCodeSteps = 1_000_000

// maxAllocCount caps the element/byte count of a single bounded-but-huge Starlark
// allocation primitive (sequence repeat `seq * n`, `range(n)`, builtin sizes).
// This Starlark build budgets *steps*, not *bytes*: one bytecode op such as
// [0]*999_999_999 or list(range(2e8)) allocates multiple gigabytes before the
// next cancellation check, blowing the decide's memory and wall-clock budget even
// though it is a single step (the library's own cap is 1<<30 — far too high for a
// synchronous decide). We reject scripts whose allocation count is a constant (or
// constant-foldable) above this cap up front, so a flow author can't ship a memory
// bomb. Loop-built counts are still bounded by maxCodeSteps (building a large count
// costs proportionally many steps).
const maxAllocCount = 10_000_000

// codeOpts allow top-level control flow, sets, and reassignment so scripts read
// naturally, while leaving recursion off (bounded together with maxCodeSteps).
// Starlark has no clock/random/IO, so execution stays deterministic.
var codeOpts = &syntax.FileOptions{
	Set:             true,
	While:           true,
	TopLevelControl: true,
	GlobalReassign:  true,
}

// evalCode runs a Code node's Starlark script with the context predeclared as
// the `data` dict and merges the script's top-level assignments back into the
// context (skipping non-serializable values such as helper functions).
func evalCode(ec evalContext, n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
	var cfg codeConfig
	if err := decodeConfig(n, &cfg); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(cfg.Code) == "" {
		return nil, "", fmt.Errorf("decision-engine: node %q code is empty", n.ID)
	}
	if err := checkAllocBudget(n.ID, cfg.Code); err != nil {
		return nil, "", err
	}
	data, err := toStarlark(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("decision-engine: node %q input: %w", n.ID, err)
	}
	thread := &starlark.Thread{Name: n.ID}
	thread.SetMaxExecutionSteps(maxCodeSteps)
	// Bound wall-clock too: the step limit caps total work, but a deadline cuts off a
	// program that ties up the synchronous decide. A Starlark thread is cancellable
	// mid-execution, so a watchdog cancels it the moment the context expires. stop
	// tears the watchdog down on the normal (fast) path so it never leaks.
	if stop := watchStarlark(ec.ctx, thread); stop != nil {
		defer stop()
	}
	globals, err := runStarlark(ec.ctx, codeOpts, thread, n.ID+".star", []byte(cfg.Code), starlark.StringDict{"data": data})
	if err != nil {
		return nil, "", fmt.Errorf("decision-engine: node %q code: %w", n.ID, err)
	}
	applied := make(map[string]any)
	for _, name := range sortedKeys(globals) {
		v, ok := fromStarlark(globals[name])
		if !ok {
			continue // not a JSON-serializable value (e.g. a function) — not an output
		}
		ctx[name] = v
		applied[name] = v
	}
	return applied, firstEdge(edges), nil
}

// checkAllocBudget rejects a Code script that requests a single, constant-sized
// allocation above maxAllocCount before it runs: a sequence repeat `x * N`, a
// `range(N)`/`bytes(N)` with a literal (or constant-foldable) N. Such a script
// would allocate gigabytes in one un-interruptible bytecode op, so it must fail
// loudly rather than exhaust memory. A syntax error is left for the executor to
// report (this guard returns nil so the real parse error surfaces there).
func checkAllocBudget(nodeID, code string) error {
	// A syntax error is not this guard's to report — ExecFileOptions surfaces the
	// real, positioned parse error — so an unparseable script passes the budget check
	// (f is nil and the walk below is a no-op) on its way to the executor.
	f, _ := codeOpts.Parse(nodeID+".star", code, 0)
	if f == nil {
		return nil
	}
	var bad error
	syntax.Walk(f, func(node syntax.Node) bool {
		if bad != nil {
			return false
		}
		switch e := node.(type) {
		case *syntax.BinaryExpr:
			if e.Op == syntax.STAR {
				if c, ok := constInt(e.X); ok && c > maxAllocCount {
					bad = allocErr(nodeID, c)
				}
				if c, ok := constInt(e.Y); ok && c > maxAllocCount {
					bad = allocErr(nodeID, c)
				}
			}
		case *syntax.CallExpr:
			if id, ok := e.Fn.(*syntax.Ident); ok && (id.Name == "range" || id.Name == "bytes") && len(e.Args) > 0 {
				if c, ok := constInt(e.Args[len(e.Args)-1]); ok && c > maxAllocCount {
					bad = allocErr(nodeID, c)
				}
			}
		}
		return true
	})
	return bad
}

func allocErr(nodeID string, n int64) error {
	return fmt.Errorf("decision-engine: node %q code: allocation of %d elements exceeds the %d limit", nodeID, n, maxAllocCount)
}

// constInt returns the non-negative integer value of a constant-foldable
// expression (an integer literal, optionally negated/parenthesized), so the alloc
// guard sees the operand the VM would. It is deliberately conservative: a value it
// can't fold statically (a variable, a huge bigint) is reported as not-constant and
// left to the VM's own per-op cap and the step/deadline bounds.
func constInt(e syntax.Expr) (int64, bool) {
	switch x := e.(type) {
	case *syntax.ParenExpr:
		return constInt(x.X)
	case *syntax.UnaryExpr:
		if x.Op == syntax.MINUS {
			if v, ok := constInt(x.X); ok {
				return -v, true
			}
		}
		return 0, false
	case *syntax.Literal:
		switch v := x.Value.(type) {
		case int64:
			return v, true
		case *big.Int:
			// A bigint literal is, by construction, larger than any int64 budget.
			if v.Sign() >= 0 {
				return math.MaxInt64, true
			}
		}
	}
	return 0, false
}

// runStarlark executes the script bounded by ctx's deadline. The thread watchdog
// (watchStarlark) cancels the VM mid-execution, but a single bytecode op that
// allocates a very large value (e.g. [0]*1e9) runs to completion before the next
// cancellation check — which can blow the wall-clock budget by seconds. So the
// execution runs on a goroutine and the caller returns a deadline error the moment
// the context expires, keeping the synchronous decide within its budget regardless
// of how slowly the abandoned (already-cancelled) op unwinds. With no deadline (the
// plain Execute path) it runs inline, bounded only by the step limit.
func runStarlark(ctx context.Context, opts *syntax.FileOptions, thread *starlark.Thread, name string, src []byte, predeclared starlark.StringDict) (starlark.StringDict, error) {
	if ctx == nil {
		return starlark.ExecFileOptions(opts, thread, name, src, predeclared)
	}
	if _, ok := ctx.Deadline(); !ok {
		return starlark.ExecFileOptions(opts, thread, name, src, predeclared)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("code skipped past the evaluation deadline: %w", err)
	}
	type result struct {
		globals starlark.StringDict
		err     error
	}
	done := make(chan result, 1)
	go func() {
		globals, err := starlark.ExecFileOptions(opts, thread, name, src, predeclared)
		done <- result{globals, err}
	}()
	select {
	case r := <-done:
		return r.globals, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("code exceeded the evaluation deadline: %w", ctx.Err())
	}
}

// watchStarlark cancels thread when ctx's deadline elapses, returning a stop
// function the caller defers to tear the watchdog down on the fast path. It is a
// no-op (returns nil) when ctx is nil or carries no deadline, so the plain Execute
// path keeps the step bound as its only limit.
func watchStarlark(ctx context.Context, thread *starlark.Thread) func() {
	if ctx == nil {
		return nil
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			thread.Cancel("evaluation deadline exceeded")
		case <-stopped:
		}
	}()
	return func() { close(stopped) }
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// toStarlark converts a JSON-like Go value into a Starlark value. Integral
// numbers become Int (so arithmetic stays integer), others Float.
func toStarlark(v any) (starlark.Value, error) {
	switch x := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(x), nil
	case string:
		return starlark.String(x), nil
	case float64:
		if !math.IsInf(x, 0) && !math.IsNaN(x) && x == math.Trunc(x) {
			return starlark.MakeInt64(int64(x)), nil
		}
		return starlark.Float(x), nil
	case int:
		return starlark.MakeInt(x), nil
	case int64:
		return starlark.MakeInt64(x), nil
	case []any:
		elems := make([]starlark.Value, len(x))
		for i, e := range x {
			ev, err := toStarlark(e)
			if err != nil {
				return nil, err
			}
			elems[i] = ev
		}
		return starlark.NewList(elems), nil
	case map[string]any:
		d := starlark.NewDict(len(x))
		for _, k := range sortedKeys(x) {
			ev, err := toStarlark(x[k])
			if err != nil {
				return nil, err
			}
			if err := d.SetKey(starlark.String(k), ev); err != nil {
				return nil, err
			}
		}
		return d, nil
	default:
		return nil, fmt.Errorf("unsupported input type %T", v)
	}
}

// fromStarlark converts a Starlark value back to a JSON-like Go value. ok is
// false for values with no JSON representation (functions, builtins), which the
// caller skips rather than treating as flow outputs.
func fromStarlark(v starlark.Value) (any, bool) {
	switch x := v.(type) {
	case starlark.NoneType:
		return nil, true
	case starlark.Bool:
		return bool(x), true
	case starlark.String:
		return string(x), true
	case starlark.Int:
		if i, ok := x.Int64(); ok {
			return i, true
		}
		if u, ok := x.Uint64(); ok {
			return u, true
		}
		return x.String(), true // out-of-range integer: preserve value as decimal text
	case starlark.Float:
		return float64(x), true
	case *starlark.List:
		out := make([]any, x.Len())
		for i := 0; i < x.Len(); i++ {
			ev, ok := fromStarlark(x.Index(i))
			if !ok {
				return nil, false
			}
			out[i] = ev
		}
		return out, true
	case *starlark.Dict:
		out := make(map[string]any, x.Len())
		for _, item := range x.Items() {
			k, ok := starlark.AsString(item[0])
			if !ok {
				return nil, false // non-string dict keys have no JSON form
			}
			ev, ok := fromStarlark(item[1])
			if !ok {
				return nil, false
			}
			out[k] = ev
		}
		return out, true
	default:
		return nil, false
	}
}
