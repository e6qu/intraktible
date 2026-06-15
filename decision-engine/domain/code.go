// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/e6qu/intraktible/decision-engine/events"
)

// maxCodeSteps bounds Code node execution so a runaway script fails loudly
// instead of hanging the decide path.
const maxCodeSteps = 1_000_000

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
func evalCode(n events.Node, ctx map[string]any, edges []events.Edge) (any, string, error) {
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
	thread := &starlark.Thread{Name: n.ID}
	thread.SetMaxExecutionSteps(maxCodeSteps)
	globals, err := starlark.ExecFileOptions(codeOpts, thread, n.ID+".star", []byte(cfg.Code), starlark.StringDict{"data": data})
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
