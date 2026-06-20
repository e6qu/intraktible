// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors_test

import (
	"encoding/json"
	"testing"

	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/domain"
)

// FuzzValidateConfig asserts define-time connector-config validation never panics
// on arbitrary config for any known type — it constructs the connector (parse +
// validate, no I/O), so a malformed config must error, never crash.
func FuzzValidateConfig(f *testing.F) {
	f.Add(`{"url":"https://x","method":"POST","auth":{"type":"bearer","token":"t"}}`)
	f.Add(`{"url":"https://x","auth":{"type":"oauth2","token_url":"https://idp/t","client_id":"c","client_secret":"s"}}`)
	f.Add(`{"driver":"sqlite","dsn":"file:x.db","query":"SELECT 1","args":["a"]}`)
	f.Add(`{"env":"sandbox","client_id":"c","secret":"s","path":"/x"}`)
	f.Add(`{"data":{"k":1}}`)
	f.Add(`{}`)

	types := []domain.ConnectorType{
		domain.ConnectorHTTP, domain.ConnectorGraphQL, domain.ConnectorSQL,
		domain.ConnectorStatic, domain.ConnectorPlaid, domain.ConnectorStripe, domain.ConnectorMockBureau,
	}
	f.Fuzz(func(t *testing.T, cfg string) {
		if !json.Valid([]byte(cfg)) {
			return
		}
		for _, typ := range types {
			_ = connectors.ValidateConfig(string(typ), json.RawMessage(cfg)) // must return, never panic
		}
	})
}
