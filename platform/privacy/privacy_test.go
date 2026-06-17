// SPDX-License-Identifier: AGPL-3.0-or-later

package privacy_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/platform/privacy"
)

func TestMask(t *testing.T) {
	fields := privacy.FieldSet([]string{"SSN", "email", " dob "})
	in := json.RawMessage(`{"ssn":"123-45-6789","amount":100,"applicant":{"Email":"a@b.com","name":"Ann"},"contacts":[{"dob":"1990-01-01"}]}`)

	out := privacy.Mask(in, fields)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["ssn"] != privacy.Redacted {
		t.Fatalf("ssn not masked: %v", m["ssn"])
	}
	if m["amount"] != float64(100) {
		t.Fatalf("non-sensitive field altered: %v", m["amount"])
	}
	app := m["applicant"].(map[string]any)
	if app["Email"] != privacy.Redacted { // case-insensitive key match
		t.Fatalf("nested email not masked: %v", app["Email"])
	}
	if app["name"] != "Ann" {
		t.Fatalf("nested name altered: %v", app["name"])
	}
	contacts := m["contacts"].([]any)
	if contacts[0].(map[string]any)["dob"] != privacy.Redacted {
		t.Fatalf("dob in array not masked: %v", contacts[0])
	}
}

func TestMaskNoOps(t *testing.T) {
	in := json.RawMessage(`{"ssn":"x"}`)
	// Empty field set leaves the payload untouched.
	if got := privacy.Mask(in, privacy.FieldSet(nil)); string(got) != string(in) {
		t.Fatalf("empty field set should not change input: %s", got)
	}
	// Unparseable / empty input is returned as-is.
	if got := privacy.Mask(json.RawMessage(``), privacy.FieldSet([]string{"ssn"})); len(got) != 0 {
		t.Fatalf("empty input should be returned unchanged: %s", got)
	}
}
