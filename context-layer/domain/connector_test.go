// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/context-layer/domain"
)

func TestDefineConnectorValidate(t *testing.T) {
	ok := []domain.DefineConnector{
		{Name: "bureau", Type: "mock_bureau"},
		{Name: "rest", Type: "http", Config: json.RawMessage(`{"url":"https://api.example.com"}`)},
	}
	for i, c := range ok {
		if err := c.Validate(); err != nil {
			t.Fatalf("valid %d rejected: %v", i, err)
		}
	}
	bad := []domain.DefineConnector{
		{Type: "http"},                      // no name
		{Name: "x", Type: "carrier_pigeon"}, // unknown type
		{Name: "x", Type: "http", Config: json.RawMessage(`[1,2]`)}, // non-object config
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("bad %d accepted: %+v", i, c)
		}
	}
}
