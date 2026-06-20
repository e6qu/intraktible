// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"strings"
	"testing"
)

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
