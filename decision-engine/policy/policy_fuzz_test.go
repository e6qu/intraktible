// SPDX-License-Identifier: AGPL-3.0-or-later

package policy_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/policy"
)

// FuzzPolicyApply asserts the disposition evaluator is robust: it compiles and runs
// attacker-influenced expr conditions against an arbitrary decision-output map. A
// spec that passes Validate must never make Apply panic, and any disposition Apply
// returns (on success) must be a known value — a policy can fail loudly on a
// missing field or non-boolean condition, but never crash or assign a bogus
// disposition.
func FuzzPolicyApply(f *testing.F) {
	f.Add(`{"rules":[{"when":"score >= 0.8","disposition":"approve"}],"default":"refer"}`, `{"score":0.9}`)
	f.Add(`{"rules":[{"when":"flag","disposition":"decline"}]}`, `{"flag":true}`)
	f.Add(`{"rules":[],"default":"approve"}`, `{}`)
	f.Fuzz(func(t *testing.T, specJSON, outputJSON string) {
		if !json.Valid([]byte(specJSON)) || !json.Valid([]byte(outputJSON)) {
			return
		}
		var spec policy.Spec
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			return
		}
		var output map[string]any
		if err := json.Unmarshal([]byte(outputJSON), &output); err != nil {
			return
		}
		// Only validated specs are required to behave; an invalid spec is a publish-time
		// error, not an Apply contract.
		if err := spec.Validate(); err != nil {
			return
		}
		out, err := spec.Apply(output)
		if err != nil {
			return // missing field / non-bool condition — loud, not a crash
		}
		if !out.Disposition.Valid() {
			t.Fatalf("Apply returned an invalid disposition %q", out.Disposition)
		}
	})
}
