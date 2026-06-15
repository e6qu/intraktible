// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
)

// ValidateInput delegates to platform/schema (tested exhaustively there); this
// confirms the decide-input wiring: required enforced, no schema = no contract.
func TestValidateInput(t *testing.T) {
	s := json.RawMessage(`{"type":"object","required":["customer"],"properties":{"customer":{"type":"string"}}}`)
	if err := domain.ValidateInput(s, map[string]any{"customer": "acme"}); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if err := domain.ValidateInput(s, map[string]any{}); err == nil {
		t.Fatal("missing required input should be rejected")
	}
	if err := domain.ValidateInput(nil, map[string]any{}); err != nil {
		t.Fatalf("no schema should be no contract: %v", err)
	}
}
