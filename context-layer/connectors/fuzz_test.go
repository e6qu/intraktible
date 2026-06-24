// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors_test

import (
	"bytes"
	"encoding/json"
	"reflect"
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

// FuzzSecretsRoundTrip asserts two properties of the AES-GCM secret sealing:
//  1. round-trip — any config that seals must open back to a semantically equal
//     config (no silent loss/mangling of credential fields), and
//  2. adversarial open — DecryptSecrets over arbitrary attacker-shaped JSON (a
//     malformed envelope from a replayed/imported config) never panics; it may
//     error or pass the value through, but it must not crash on a bad base64
//     value, a non-string key, or deep nesting.
func FuzzSecretsRoundTrip(f *testing.F) {
	key := bytes.Repeat([]byte{0x1}, 32)
	kr, err := connectors.NewKeyring(key)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(`{"password":"hunter2","url":"https://x"}`)
	f.Add(`{"auth":{"token":"abc"},"nested":{"client_secret":"s"}}`)
	f.Add(`{"$intraktible_sealed":"v1","value":"not-base64!!","key":"deadbeef"}`)
	f.Add(`{"$intraktible_sealed":"v1","value":123}`)
	f.Add(`{}`)
	f.Fuzz(func(t *testing.T, cfg string) {
		if !json.Valid([]byte(cfg)) {
			return
		}
		raw := json.RawMessage(cfg)
		// Adversarial path: opening arbitrary JSON must never panic.
		_, _ = connectors.DecryptSecrets(raw, kr, loc("fuzz"))
		// Round-trip path: only configs that seal cleanly are required to open back.
		sealed, err := connectors.EncryptSecrets(raw, kr, loc("fuzz"))
		if err != nil {
			return
		}
		opened, err := connectors.DecryptSecrets(sealed, kr, loc("fuzz"))
		if err != nil {
			t.Fatalf("round-trip open failed: %v (cfg=%s)", err, cfg)
		}
		var want, got any
		if err := json.Unmarshal(raw, &want); err != nil {
			return
		}
		if err := json.Unmarshal(opened, &got); err != nil {
			t.Fatalf("opened config is not valid JSON: %v", err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("round-trip mismatch:\n want %#v\n got  %#v", want, got)
		}
	})
}
