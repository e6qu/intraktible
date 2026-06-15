// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/agent-manager/domain"
)

func TestDefineAgentValidate(t *testing.T) {
	ok := []domain.DefineAgent{
		{Name: "triage"},
		{Name: "triage", Model: "stub", System: "You are a triage assistant.", Tools: []string{"lookup"}},
		{Name: "extract", Schema: json.RawMessage(`{"type":"object"}`)},
	}
	for i, c := range ok {
		if err := c.Validate(); err != nil {
			t.Fatalf("valid %d rejected: %v", i, err)
		}
	}
	bad := []domain.DefineAgent{
		{}, // no name
		{Name: "x", Schema: json.RawMessage(`[1,2]`)}, // non-object schema
		{Name: "x", Tools: []string{"ok", "  "}},      // blank tool
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("bad %d accepted: %+v", i, c)
		}
	}
}
