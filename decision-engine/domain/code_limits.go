// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"fmt"
	"math"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Bounding a Code node's memory takes two mechanisms, because Starlark's own
// budget counts steps and a single step may allocate without limit.
//
//   - watchStarlark samples the heap and cancels the VM. Cancellation lands at the
//     next bytecode op, so it bounds any script that grows over several ops —
//     `s = s + s` in a loop, `l.extend(l)`, appending in a loop. Each of those ops
//     at most sums or doubles values that are already live, so the overshoot past
//     the budget is at most one more allocation of that size.
//
//   - The guards below bound the ops that break that property: ops whose output
//     can dwarf their live inputs, and so reach terabytes in ONE un-interruptible
//     step that no watchdog can catch mid-flight. There are exactly four:
//     sequence repeat (`"x" * n`), `range(n)` (lazy itself, but `list(range(n))`
//     is not), `sep.join(xs)` (xs may be a million references to one shared
//     string), and `s.replace(old, new)` (each match expands to len(new)).
//
// Everything else — `+`, `%`, `extend`, `sorted`, `list`, `str` — produces a value
// no larger than the sum of its live inputs, so the watchdog bounds it. The static
// pre-flight check this replaced only folded *literal* counts, which left
// `n = 1000000000; s = "x" * n` and every loop-built size completely unguarded.

// guardPrefix namespaces the builtins the rewriter injects. A script that mentions
// the prefix is rejected rather than silently rebinding a guard.
const guardPrefix = "_intraktible_"

const (
	guardMul     = guardPrefix + "mul"
	guardRange   = guardPrefix + "range"
	guardJoin    = guardPrefix + "join"
	guardReplace = guardPrefix + "replace"
)

// guardedMethods are the receiver methods rewritten into guarded builtins. Their
// output size is not bounded by the size of their live inputs.
var guardedMethods = map[string]string{"join": guardJoin, "replace": guardReplace}

// guardBuiltins returns the predeclared environment for a Code node: the caller's
// `data`, the guards the rewriter's call sites resolve to, and rejecting shims over
// the reflection builtins. getattr(s, "join") would hand back a guarded method as a
// plain value — sidestepping the rewrite, which only rewrites syntactic `s.join(…)`
// — so reflection over attributes is refused outright (a decision script has no need
// to reach a method by a computed name). These predeclared names shadow the Universe.
func guardBuiltins(data starlark.Value) starlark.StringDict {
	env := starlark.StringDict{
		"data":       data,
		"range":      starlark.NewBuiltin(guardRange, guardedRange),
		guardMul:     starlark.NewBuiltin(guardMul, guardedMul),
		guardRange:   starlark.NewBuiltin(guardRange, guardedRange),
		guardJoin:    starlark.NewBuiltin(guardJoin, guardedJoin),
		guardReplace: starlark.NewBuiltin(guardReplace, guardedReplace),
	}
	for _, name := range []string{"getattr", "hasattr", "dir"} {
		env[name] = starlark.NewBuiltin(name, rejectReflection)
	}
	return env
}

func rejectReflection(thread *starlark.Thread, b *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
	return nil, fmt.Errorf("node %q: %s is not available in a code node — call methods directly", thread.Name, b.Name())
}

func allocErr(thread *starlark.Thread, what string, size int64) error {
	return fmt.Errorf("node %q: %s would allocate %d bytes, over the %d byte limit", thread.Name, what, size, maxAllocCount)
}

// guardedMul checks a sequence repeat before performing it. Int*Int and Float*Int
// carry no size, so they fall straight through to the interpreter's own operator.
func guardedMul(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	x, y, err := twoPositional(thread, "multiply", args, kwargs)
	if err != nil {
		return nil, err
	}
	for _, pair := range [2][2]starlark.Value{{x, y}, {y, x}} {
		size, ok := repeatSize(pair[0], pair[1])
		if !ok {
			continue
		}
		if size > maxAllocCount {
			return nil, allocErr(thread, "the repeat", size)
		}
	}
	return starlark.Binary(syntax.STAR, x, y)
}

// repeatSize returns the byte size of `seq * count`, and whether the pair is in
// fact a sequence repeat. A count too large for an int64 saturates rather than
// wrapping, so a bigint count is always rejected.
func repeatSize(seq, count starlark.Value) (int64, bool) {
	n, ok := count.(starlark.Int)
	if !ok {
		return 0, false
	}
	unit, ok := valueSize(seq)
	if !ok {
		return 0, false
	}
	i, ok := n.Int64()
	if !ok {
		return maxAllocCount + 1, true // a bigint count: larger than any budget
	}
	if i <= 0 || unit == 0 {
		return 0, true
	}
	if i > math.MaxInt64/unit {
		return math.MaxInt64, true // the product overflows: saturate rather than wrap
	}
	return unit * i, true
}

func guardedRange(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	// range is lazy, so it allocates nothing here — but list(range(…)) allocates one
	// element per step in one op, so the SPAN is bounded at the source. Bounding each
	// argument would miss range(-9e6, 9e6): 18M elements from two in-bounds args.
	span, ok := rangeSpan(args)
	if !ok {
		return callUniverse(thread, "range", args, kwargs) // let the interpreter report a bad call
	}
	if span > maxAllocCount {
		return nil, allocErr(thread, "the range", span)
	}
	return callUniverse(thread, "range", args, kwargs)
}

// rangeSpan returns how many values range(args...) yields, saturating rather than
// overflowing, and whether the args were the int shapes range accepts (start[,
// stop[, step]]). A shape it doesn't recognize is left for the interpreter to
// reject.
func rangeSpan(args starlark.Tuple) (int64, bool) {
	ints := make([]int64, 0, len(args))
	for _, arg := range args {
		n, ok := arg.(starlark.Int)
		if !ok {
			return 0, false
		}
		i, ok := n.Int64()
		if !ok {
			return math.MaxInt64, true // a bigint bound: larger than any budget
		}
		ints = append(ints, i)
	}
	start, step := int64(0), int64(1)
	var stop int64
	switch len(ints) {
	case 1:
		stop = ints[0]
	case 2:
		start, stop = ints[0], ints[1]
	case 3:
		start, stop, step = ints[0], ints[1], ints[2]
	default:
		return 0, false
	}
	if step == 0 {
		return 0, true // an error the interpreter will raise; not our allocation
	}
	diff := stop - start
	if (step > 0 && diff <= 0) || (step < 0 && diff >= 0) {
		return 0, true // empty range
	}
	if step < 0 {
		diff, step = -diff, -step
	}
	return (diff + step - 1) / step, true
}

// guardedJoin bounds `sep.join(xs)`: xs may hold a million references to one
// shared string, so the result can be far larger than anything currently live.
func guardedJoin(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	recv, iterable, err := twoPositional(thread, "join", args, kwargs)
	if err != nil {
		return nil, err
	}
	sep, ok := recv.(starlark.String)
	if !ok {
		return callMethod(thread, recv, "join", args[1:], kwargs)
	}
	seq, ok := iterable.(starlark.Indexable)
	if !ok {
		return callMethod(thread, recv, "join", args[1:], kwargs)
	}
	total := int64(len(sep)) * int64(max(seq.Len()-1, 0))
	for i := range seq.Len() {
		size, _ := valueSize(seq.Index(i))
		total += size
		if total > maxAllocCount {
			return nil, allocErr(thread, "the join", total)
		}
	}
	return callMethod(thread, recv, "join", args[1:], kwargs)
}

// guardedReplace bounds `s.replace(old, new)`: every match expands to len(new), so
// a short string and a long replacement multiply out.
func guardedReplace(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("node %q: replace wants a receiver, old and new", thread.Name)
	}
	recv, ok := args[0].(starlark.String)
	if !ok {
		return callMethod(thread, args[0], "replace", args[1:], kwargs)
	}
	old, oldOK := args[1].(starlark.String)
	repl, replOK := args[2].(starlark.String)
	if !oldOK || !replOK {
		return callMethod(thread, args[0], "replace", args[1:], kwargs)
	}
	// The worst case is every position matching an empty `old`.
	matches := int64(len(recv)) + 1
	if len(old) > 0 {
		matches = int64(len(recv) / len(old))
	}
	// replace(old, new, count) caps the replacements; honor it so a bounded replace
	// isn't over-rejected on the theoretical worst case.
	if len(args) >= 4 {
		if count, ok := args[3].(starlark.Int); ok {
			if c, ok := count.Int64(); ok && c >= 0 && c < matches {
				matches = c
			}
		}
	}
	if grown := int64(len(recv)) + matches*int64(len(repl)); grown > maxAllocCount {
		return nil, allocErr(thread, "the replace", grown)
	}
	return callMethod(thread, args[0], "replace", args[1:], kwargs)
}

// valueSize is the approximate byte size of a Starlark value, and whether it is a
// sized value at all (an Int or a function is not). Nested sequences are summed,
// short-circuiting once the total is past any budget the callers care about.
func valueSize(v starlark.Value) (int64, bool) {
	switch x := v.(type) {
	case starlark.String:
		return int64(len(x)), true
	case starlark.Bytes:
		return int64(len(x)), true
	case *starlark.List:
		return sequenceSize(x), true
	case starlark.Tuple:
		return sequenceSize(x), true
	}
	return 0, false
}

const wordSize = 8 // a Value header, for sizing a sequence's own slots

func sequenceSize(seq starlark.Indexable) int64 {
	total := int64(seq.Len()) * wordSize
	for i := range seq.Len() {
		if total > maxAllocCount {
			return total
		}
		if size, ok := valueSize(seq.Index(i)); ok {
			total += size
		}
	}
	return total
}

func twoPositional(thread *starlark.Thread, what string, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, starlark.Value, error) {
	if len(args) != 2 || len(kwargs) != 0 {
		return nil, nil, fmt.Errorf("node %q: %s wants exactly two positional arguments", thread.Name, what)
	}
	return args[0], args[1], nil
}

func callUniverse(thread *starlark.Thread, name string, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	fn, ok := starlark.Universe[name]
	if !ok {
		return nil, fmt.Errorf("node %q: %s is not a builtin", thread.Name, name)
	}
	return starlark.Call(thread, fn, args, kwargs)
}

func callMethod(thread *starlark.Thread, recv starlark.Value, name string, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	hasAttrs, ok := recv.(starlark.HasAttrs)
	if !ok {
		return nil, fmt.Errorf("node %q: %s has no .%s method", thread.Name, recv.Type(), name)
	}
	fn, err := hasAttrs.Attr(name)
	if err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, fmt.Errorf("node %q: %s has no .%s method", thread.Name, recv.Type(), name)
	}
	return starlark.Call(thread, fn, args, kwargs)
}
