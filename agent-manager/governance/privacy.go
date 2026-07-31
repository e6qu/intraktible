// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"encoding/json"
	"fmt"

	"github.com/e6qu/intraktible/platform/privacy"
)

// rejectTextPII keeps authoring, review, and operational prose free of raw
// high-signal identifiers. Case assistance persists governed evidence ids and a
// suggestion, not a second copy of customer contact/payment identifiers.
func rejectTextPII(label string, values ...string) error {
	for _, value := range values {
		if privacy.ContainsTextPII(value) {
			return fmt.Errorf(
				"agent governance: %s contains raw PII; use a governed evidence or artifact reference",
				label,
			)
		}
	}
	return nil
}

func rejectJSONPII(label string, values ...json.RawMessage) error {
	for _, value := range values {
		if len(value) > 0 && privacy.ContainsTextPII(string(value)) {
			return fmt.Errorf(
				"agent governance: %s contains raw PII; use a governed evidence or artifact reference",
				label,
			)
		}
	}
	return nil
}
