// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/e6qu/intraktible/platform/secretbox"
)

// The connector credential sealing rides on the shared platform/secretbox
// primitive (AES-256-GCM + a rotating key ring + a versioned JSON envelope). This
// file keeps only the connector-specific part: walking a connector config and
// sealing just the recognized credential FIELDS, leaving non-secret routing/query
// config as ordinary JSON. The crypto and key management live in secretbox; these
// aliases keep the connectors API stable for existing callers.
type (
	// SecretBox encrypts/decrypts connector credential values. See secretbox.SecretBox.
	SecretBox = secretbox.SecretBox
	// Keyring holds the connector secret keys (primary + rotation). See secretbox.Keyring.
	Keyring = secretbox.Keyring
)

var (
	// NewKeyring builds a connector keyring (first key primary, rest decrypt-only).
	NewKeyring = secretbox.NewKeyring
	// NewKMSKeyring builds a keyring backed by an external KMS.
	NewKMSKeyring = secretbox.NewKMSKeyring
	// NewAESGCMSecretBox builds a local AES-256-GCM secret box from a 32-byte key.
	NewAESGCMSecretBox = secretbox.NewAESGCMSecretBox
	// KeyFingerprint derives a key's stable short id.
	KeyFingerprint = secretbox.KeyFingerprint
)

// EncryptSecrets returns config with credential fields replaced by encrypted
// envelopes sealed under the keyring's primary key. It is intentionally narrow:
// only recognized credential field names are protected, while non-secret
// routing/query config remains ordinary JSON.
func EncryptSecrets(config json.RawMessage, kr *Keyring) (json.RawMessage, error) {
	return transformSecrets(config, kr, encryptSecretValue)
}

// DecryptSecrets opens encrypted credential envelopes in config, selecting the
// key that sealed each value. Plaintext configs continue to work, which keeps
// dev/test definitions and old logs readable.
func DecryptSecrets(config json.RawMessage, kr *Keyring) (json.RawMessage, error) {
	return transformSecrets(config, kr, decryptSecretValue)
}

func transformSecrets(
	config json.RawMessage,
	kr *Keyring,
	fn func(any, *Keyring) (any, error),
) (json.RawMessage, error) {
	if len(config) == 0 || kr == nil {
		return config, nil
	}
	var v any
	if err := json.Unmarshal(config, &v); err != nil {
		return nil, fmt.Errorf("context-layer: connector credential config: %w", err)
	}
	out, err := walkSecretFields(v, kr, fn)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("context-layer: connector secret marshal: %w", err)
	}
	return b, nil
}

func walkSecretFields(
	v any,
	kr *Keyring,
	fn func(any, *Keyring) (any, error),
) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			var err error
			if redactKeys[strings.ToLower(k)] {
				t[k], err = fn(val, kr)
			} else {
				t[k], err = walkSecretFields(val, kr, fn)
			}
			if err != nil {
				return nil, err
			}
		}
		return t, nil
	case []any:
		for i := range t {
			val, err := walkSecretFields(t[i], kr, fn)
			if err != nil {
				return nil, err
			}
			t[i] = val
		}
		return t, nil
	default:
		return v, nil
	}
}

// encryptSecretValue seals one credential field's value into an inline envelope
// object (kept as a nested object — not standalone bytes — so a connector config
// stays human-inspectable and its on-the-wire shape is unchanged).
func encryptSecretValue(v any, kr *Keyring) (any, error) {
	if isSecretEnvelope(v) {
		return v, nil
	}
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("context-layer: connector secret encode: %w", err)
	}
	keyID, sealed, err := kr.SealValue(plain)
	if err != nil {
		return nil, err
	}
	return secretbox.Envelope{
		Version: secretbox.Version,
		Key:     keyID,
		Value:   base64.StdEncoding.EncodeToString(sealed),
	}, nil
}

func decryptSecretValue(v any, kr *Keyring) (any, error) {
	m, ok := v.(map[string]any)
	if !ok || m["$intraktible_sealed"] != secretbox.Version {
		return v, nil
	}
	raw, ok := m["value"].(string)
	if !ok || raw == "" {
		return nil, fmt.Errorf("context-layer: connector secret envelope missing value")
	}
	sealed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("context-layer: connector secret envelope decode: %w", err)
	}
	plain, err := kr.OpenValue(stringValue(m["key"]), sealed)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("context-layer: connector secret plaintext decode: %w", err)
	}
	return out, nil
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func isSecretEnvelope(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || m["$intraktible_sealed"] != secretbox.Version {
		return false
	}
	// Require EXACTLY the sealed-envelope shape ({$intraktible_sealed, value, [key]})
	// — so a real credential object that merely carries a field named
	// $intraktible_sealed isn't mistaken for already-sealed and written through in the
	// clear (the seal step would be skipped). Mirrors secretbox.IsSealed's strictness.
	if _, hasValue := m["value"].(string); !hasValue {
		return false
	}
	for k := range m {
		if k != "$intraktible_sealed" && k != "value" && k != "key" {
			return false
		}
	}
	return true
}
