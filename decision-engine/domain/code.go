// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// maxCodeSteps bounds Code node execution so a runaway script fails loudly
// instead of hanging the decide path.
const maxCodeSteps = 1_000_000

// maxAllocCount caps the bytes a single Starlark op may produce out of thin air —
// a sequence repeat, a range, a join, a replace. See code_limits.go for why those
// four, and why every other op is left to codeHeapGrowthLimit.
const maxAllocCount = 10_000_000

// codeHeapGrowthLimit bounds how much heap one Code node may add before its VM is
// cancelled. This Starlark build budgets *steps*, not bytes, and a single step may
// allocate without limit: `s = s + s` twenty times costs a few hundred steps and
// asks for a terabyte. Neither the step cap nor the wall-clock deadline stops that
// — the deadline hands an error back to the caller while the abandoned goroutine
// keeps allocating — so memory has to be bounded on its own terms.
//
// It is a var so tests can pick a limit small enough to trip cheaply. Kept well
// under the process's likely headroom because a pathological op can transiently
// reach ~3× this before cancellation (see codeHeapSampleInterval).
var codeHeapGrowthLimit uint64 = 128 << 20

// codeHeapSampleInterval is how often the guard samples the heap while a Code node
// runs. Cancellation lands at the next bytecode op, so the op already in flight runs
// to completion — and for the pathological doubling case (`s = s + s`) that op both
// keeps its ~limit-sized operand live and allocates a 2×-sized result, so the
// transient peak is roughly baseline + 3×limit before the next op cancels. Sizing
// the limit to a third of the container's headroom keeps that peak safe; sampling
// frequently does not help a single un-interruptible op, but bounds everything else.
const codeHeapSampleInterval = 5 * time.Millisecond

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
	data, err := toStarlark(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("decision-engine: node %q input: %w", n.ID, err)
	}
	predeclared := guardBuiltins(data)
	prog, err := compileCode(n.ID+".star", cfg.Code, predeclared)
	if err != nil {
		return nil, "", fmt.Errorf("decision-engine: node %q code: %w", n.ID, err)
	}
	thread := &starlark.Thread{Name: n.ID}
	thread.SetMaxExecutionSteps(maxCodeSteps)
	// Bound wall-clock and heap too: the step limit caps neither. A Starlark thread
	// is cancellable mid-execution, so a watchdog cancels it the moment the context
	// expires or the script outgrows its memory budget. stop tears the watchdog down
	// on the normal (fast) path so it never leaks.
	defer watchStarlark(ec.ctx, thread)()
	globals, err := runStarlark(ec.ctx, thread, prog, predeclared)
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

// compileCode parses a Code script, rewrites its unbounded-growth ops onto the
// guarded builtins (see code_rewrite.go), and compiles the result. Parse errors
// surface here, positioned, before anything runs.
func compileCode(name, code string, predeclared starlark.StringDict) (*starlark.Program, error) {
	f, err := codeOpts.Parse(name, code, 0)
	if err != nil {
		return nil, err
	}
	if err := rewriteGuards(f); err != nil {
		return nil, err
	}
	return starlark.FileProgram(f, predeclared.Has)
}

// runStarlark executes the compiled program bounded by ctx's deadline. The thread
// watchdog (watchStarlark) cancels the VM mid-execution, but a single bytecode op
// that allocates a large value runs to completion before the next cancellation
// check — which can blow the wall-clock budget by seconds. So the execution runs on
// a goroutine and the caller returns a deadline error the moment the context
// expires, keeping the synchronous decide within its budget regardless of how
// slowly the abandoned (already-cancelled) op unwinds. With no deadline (the plain
// Execute path) it runs inline, bounded by the step and heap limits.
func runStarlark(ctx context.Context, thread *starlark.Thread, prog *starlark.Program, predeclared starlark.StringDict) (starlark.StringDict, error) {
	if ctx == nil {
		return prog.Init(thread, predeclared)
	}
	if _, ok := ctx.Deadline(); !ok {
		return prog.Init(thread, predeclared)
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
		globals, err := prog.Init(thread, predeclared)
		done <- result{globals, err}
	}()
	select {
	case r := <-done:
		return r.globals, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("code exceeded the evaluation deadline: %w", ctx.Err())
	}
}

// watchStarlark cancels thread when it outgrows its heap budget or (when ctx
// carries one) when the evaluation deadline elapses, returning a stop function the
// caller defers to tear the watchdog down on the fast path.
//
// The heap guard runs on every path, deadline or not: a script that doubles a
// value in a loop reaches terabytes in a few hundred steps, so the step bound and
// the deadline both leave the process to be OOM-killed. Sampled heap is the whole
// process's, not this script's — under concurrency the guard trips a little early,
// which is the right way to be wrong when the alternative is an OOM that takes
// every tenant down with it.
func watchStarlark(ctx context.Context, thread *starlark.Thread) func() {
	var deadline <-chan struct{}
	if ctx != nil {
		if _, ok := ctx.Deadline(); ok {
			deadline = ctx.Done()
		}
	}
	ceiling := heapInUse() + codeHeapGrowthLimit
	stopped := make(chan struct{})
	go func() {
		ticker := time.NewTicker(codeHeapSampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-deadline: // nil channel when there is none: blocks forever
				thread.Cancel("evaluation deadline exceeded")
				return
			case <-stopped:
				return
			case <-ticker.C:
				if heapInUse() > ceiling {
					thread.Cancel(fmt.Sprintf("memory budget of %d MiB exceeded", codeHeapGrowthLimit>>20))
					return
				}
			}
		}
	}()
	return func() { close(stopped) }
}

func heapInUse() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
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
