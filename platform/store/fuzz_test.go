// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// FuzzMemoryListPrefix checks the end-to-end prefix-scan contract: for arbitrary
// keys and an arbitrary prefix, List(prefix) must return EXACTLY the keys that start
// with the prefix — no dropped matches, no leaked non-matches. This guards the whole
// List path (not just prefixUpperBound) against a filtering regression.
func FuzzMemoryListPrefix(f *testing.F) {
	f.Add("a\nb\nc", "a")
	f.Add("o/w/x\no/w/y\no2/w/z", "o/w/")
	f.Add("t:i:1\nt:i:2\nt:j:9", "t:i:")
	f.Fuzz(func(t *testing.T, keysBlob, prefix string) {
		ctx := context.Background()
		m := NewMemory()
		want := map[string]bool{}
		for _, k := range strings.Split(keysBlob, "\n") {
			if k == "" {
				continue
			}
			if err := m.Put(ctx, "c", k, json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(k, prefix) {
				want[k] = true
			}
		}
		got, err := m.List(ctx, "c", prefix)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("List(%q) returned %d rows, want %d", prefix, len(got), len(want))
		}
		for _, r := range got {
			if !want[r.Key] {
				t.Fatalf("List(%q) leaked non-matching key %q", prefix, r.Key)
			}
		}
	})
}

// FuzzPrefixUpperBound asserts the key invariant the durable backends' prefix scan
// depends on: for any prefix P and any key K that starts with P, K falls in the
// half-open range [P, upper) — i.e. P <= K and (upper == "" OR K < upper). If this
// held loosely, a tenant-scoped List range could drop or leak rows.
func FuzzPrefixUpperBound(f *testing.F) {
	for _, p := range []string{"", "a", "orgA/main/", "z", "\xff", "a\xff", "\xff\xff"} {
		f.Add(p, p+"x")
		f.Add(p, "")
	}
	f.Fuzz(func(t *testing.T, prefix, suffix string) {
		key := prefix + suffix // key is guaranteed to start with prefix
		upper := prefixUpperBound(prefix)
		if !strings.HasPrefix(key, prefix) {
			return // defensive; construction guarantees it
		}
		if key < prefix {
			t.Fatalf("key %q < prefix %q", key, prefix)
		}
		if upper != "" && !(key < upper) {
			t.Fatalf("key %q with prefix %q not below upper bound %q", key, prefix, upper)
		}
		// A string that does NOT start with prefix and sorts at/after upper must not
		// be in range — spot-check that upper truly bounds the prefix family: prefix
		// itself is in range, and upper is strictly greater than prefix.
		if upper != "" && !(prefix < upper) {
			t.Fatalf("upper %q is not strictly greater than prefix %q", upper, prefix)
		}
	})
}
