// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/events"
)

func codeNode(t *testing.T, src string) events.Node {
	t.Helper()
	cfg, err := json.Marshal(map[string]string{"code": src})
	if err != nil {
		t.Fatalf("marshalling the node config: %v", err)
	}
	return events.Node{ID: "c", Type: events.NodeCode, Config: cfg}
}

func runCode(t *testing.T, src string, ctx map[string]any) error {
	t.Helper()
	_, _, err := evalCode(evalContext{}, codeNode(t, src), ctx, []events.Edge{})
	return err
}

// withHeapLimit shrinks the Code-node heap budget for the duration of a test, so a
// bomb trips after a few MiB rather than the production 256.
func withHeapLimit(t *testing.T, limit uint64) {
	t.Helper()
	prev := codeHeapGrowthLimit
	codeHeapGrowthLimit = limit
	t.Cleanup(func() { codeHeapGrowthLimit = prev })
}

// TestCodeSingleOpAllocGuard covers the ops that allocate without bound in ONE
// un-interruptible step. The heap watchdog cannot catch these — it only regains
// control at the next bytecode op — so each is checked before it runs. None of
// these has a literal size for a static check to fold.
func TestCodeSingleOpAllocGuard(t *testing.T) {
	tests := []struct {
		name string
		code string
		want string
	}{
		{
			name: "repeat a string by a variable count",
			code: "n = 200000000\ns = \"x\" * n\n",
			want: "the repeat would allocate 200000000 bytes",
		},
		{
			name: "repeat a list by a variable count",
			code: "n = 900000000\nl = [0] * n\n",
			want: "the repeat would allocate 7200000000 bytes",
		},
		{
			name: "repeat by a bigint count",
			code: "s = \"x\" * 99999999999999999999\n",
			want: "the repeat would allocate",
		},
		{
			name: "repeat in place",
			code: "n = 200000000\ns = \"x\"\ns *= n\n",
			want: "the repeat would allocate 200000000 bytes",
		},
		{
			// The list holds twenty references to one shared 1 MB string, so it costs
			// 160 bytes of live heap and joins to 20 MB. Built by comprehension: a
			// repeat would be caught first, since repeat is sized by its contents.
			name: "join a list of shared references",
			code: "big = \"x\" * 1000000\nl = [big for i in range(20)]\ns = \"\".join(l)\n",
			want: "the join would allocate",
		},
		{
			name: "replace expanding every match",
			code: "s = \"ab\" * 1000000\nbig = \"y\" * 1000\nout = s.replace(\"a\", big)\n",
			want: "the replace would allocate",
		},
		{
			name: "materialise a huge range",
			code: "l = list(range(900000000))\n",
			want: "the range would allocate 900000000 bytes",
		},
		{
			// A two-argument range whose ENDS are each in-bounds but whose SPAN is
			// not: guarding each argument alone would wave this through.
			name: "range with an in-bounds start and stop but a huge span",
			code: "l = list(range(-9000000, 9000000))\n",
			want: "the range would allocate 18000000 bytes",
		},
		{
			// getattr would hand back a guarded method as a plain value, sidestepping
			// the rewrite that only rewrites syntactic s.join(...).
			name: "join reached through getattr",
			code: "big = \"x\" * 1000000\nl = [big for i in range(20)]\ns = getattr(\"\", \"join\")(l)\n",
			want: "getattr is not available",
		},
		{
			name: "replace reached through getattr",
			code: "s = \"a\" * 1000000\nbig = \"y\" * 1000\nout = getattr(s, \"replace\")(\"a\", big)\n",
			want: "getattr is not available",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runCode(t, tt.code, map[string]any{})
			if err == nil {
				t.Fatal("a script that allocates without bound was allowed to finish")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want an error containing %q, got: %v", tt.want, err)
			}
		})
	}
}

// TestCodeHeapGuard covers growth spread across many ops. Each step at most doubles
// what is already live, so no single-op check sees it coming — only the heap
// watchdog, which cancels the VM at the next op.
func TestCodeHeapGuard(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{"doubling a string in a loop", "s = \"x\" * 1000000\nfor i in range(30):\n    s = s + s\n"},
		{"doubling a list in a loop", "l = [0] * 1000000\nfor i in range(30):\n    l = l + l\n"},
		{"extending a list in a loop", "l = [0] * 1000000\nfor i in range(30):\n    l.extend(l)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withHeapLimit(t, 8<<20)
			err := runCode(t, tt.code, map[string]any{})
			if err == nil {
				t.Fatal("a script that allocates without bound was allowed to finish")
			}
			if !strings.Contains(err.Error(), "memory budget") {
				t.Fatalf("want a memory-budget error, got: %v", err)
			}
		})
	}
}

// TestCodeGuardsPreserveSemantics guards against the rewrite changing what an
// ordinary script means: arithmetic, repeat, join and replace must still work, and
// a normal scoring loop must not trip the heap budget.
func TestCodeGuardsPreserveSemantics(t *testing.T) {
	withHeapLimit(t, 8<<20)
	ctx := map[string]any{"fico": 700.0}
	code := strings.Join([]string{
		`score = data["fico"] * 2`,
		`score = score - 400`,
		`for i in range(1000):`,
		`    score = score + 1`,
		`bar = "-" * 3`,
		`joined = ",".join(["a", "b"])`,
		`swapped = "a-b".replace("-", "+")`,
		// A bounded replace of a big string must not be over-rejected on its
		// theoretical worst case: count caps it to one replacement.
		`capped = ("a" * 100000).replace("a", "y" * 1000, 1)`,
		`doubled = [1, 2] * 2`,
		`n = 3`,
		`n *= 4`,
		`ratio = 1.5 * 2`,
	}, "\n") + "\n"
	if err := runCode(t, code, ctx); err != nil {
		t.Fatalf("an ordinary script tripped a guard: %v", err)
	}
	for name, want := range map[string]any{
		"score":   int64(2000),
		"bar":     "---",
		"joined":  "a,b",
		"swapped": "a+b",
		"n":       int64(12),
		"ratio":   3.0,
	} {
		if got := ctx[name]; got != want {
			t.Errorf("%s = %#v, want %#v", name, got, want)
		}
	}
	if got, want := ctx["doubled"], []any{int64(1), int64(2), int64(1), int64(2)}; !equalSlices(got, want) {
		t.Errorf("doubled = %#v, want %#v", got, want)
	}
}

func equalSlices(got any, want []any) bool {
	list, ok := got.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for i := range want {
		if list[i] != want[i] {
			return false
		}
	}
	return true
}

// TestCodeGuardsAreNotReachable keeps the injected builtins out of the script's
// reach: rebinding one, or passing a guarded method around as a value, would let a
// script slip past its budget.
func TestCodeGuardsAreNotReachable(t *testing.T) {
	tests := []struct {
		name string
		code string
		want string
	}{
		{"rebinding a guard", "_intraktible_mul = 1\n", "is reserved"},
		{"referencing a guard", "x = _intraktible_join\n", "is reserved"},
		{"passing join as a value", "f = \",\".join\ns = f([\"a\"])\n", "must be called directly"},
		{"in-place repeat on a subscript", "d = {\"a\": \"x\"}\nd[\"a\"] *= 100\n", "only supported on a plain variable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runCode(t, tt.code, map[string]any{})
			if err == nil {
				t.Fatal("a script reached past its guards")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want an error containing %q, got: %v", tt.want, err)
			}
		})
	}
}
