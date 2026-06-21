// SPDX-License-Identifier: AGPL-3.0-or-later

package erasure_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/erasure"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// FuzzSealOpenFields asserts the crypto-shred field sealing is robust and lossless:
// sealing arbitrary JSON then opening it (for a non-erased subject) round-trips
// exactly, sealing never panics and always produces valid JSON, and OpenFields over
// adversarial input (envelope-lookalikes, bad base64) never panics — it errors or
// passes through, but does not crash.
func FuzzSealOpenFields(f *testing.F) {
	f.Add(`{"ssn":"123-45","name":"Ada","nested":{"ssn":"999"}}`, "ssn")
	f.Add(`{"a":[{"email":"x@y"}]}`, "email")
	f.Add(`{"$intraktible_erased":"v1","value":"x","extra":"leak"}`, "ssn")
	f.Add(`{"k":"not-base64!!"}`, "k")
	f.Fuzz(func(t *testing.T, docJSON, fieldsCSV string) {
		if !json.Valid([]byte(docJSON)) {
			return
		}
		// Only objects are sealable subjects; skip arrays/scalars.
		var probe any
		if json.Unmarshal([]byte(docJSON), &probe) != nil {
			return
		}
		if _, ok := probe.(map[string]any); !ok {
			return
		}

		v := erasure.NewVault(store.NewMemory())
		ctx := context.Background()
		id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}
		fields := map[string]bool{}
		for _, fld := range strings.Split(fieldsCSV, ",") {
			if fld = strings.TrimSpace(strings.ToLower(fld)); fld != "" {
				fields[fld] = true
			}
		}

		// Adversarial open of the raw (possibly envelope-lookalike) doc: never panics.
		_, _ = v.OpenFields(ctx, id, "subj", json.RawMessage(docJSON))

		sealed, err := v.SealFields(ctx, id, "subj", json.RawMessage(docJSON), fields)
		if err != nil {
			return // a seal failure is loud, not a crash
		}
		if !json.Valid([]byte(sealed)) {
			t.Fatalf("sealed output is not valid JSON: %s", sealed)
		}
		opened, err := v.OpenFields(ctx, id, "subj", sealed)
		if err != nil {
			t.Fatalf("round-trip open failed: %v (doc=%s)", err, docJSON)
		}
		var want, got any
		if err := json.Unmarshal([]byte(docJSON), &want); err != nil {
			return
		}
		if err := json.Unmarshal(opened, &got); err != nil {
			t.Fatalf("opened doc is not valid JSON: %v", err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("seal/open round-trip mismatch:\n want %#v\n got  %#v", want, got)
		}
	})
}
