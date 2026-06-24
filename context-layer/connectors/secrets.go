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

// SecretLocation names where a sealed credential lives: its tenant (org,
// workspace) and connector. It is the stable prefix of the per-field AAD bound
// into each v2 credential envelope, so a sealed value cannot be replayed into a
// different tenant or connector — Open recomputes the same AAD at fetch time.
type SecretLocation struct {
	Org       string
	Workspace string
	Connector string
}

// aadFor derives the additional-authenticated-data for one credential field:
// org \x00 workspace \x00 connector \x00 field_path. The NUL separators keep the
// components unambiguous, and the field path binds each field to its position so a
// ciphertext cannot be moved between fields of the same connector either.
func (l SecretLocation) aadFor(fieldPath string) []byte {
	return []byte(strings.Join([]string{l.Org, l.Workspace, l.Connector, fieldPath}, "\x00"))
}

// EncryptSecrets returns config with credential fields replaced by encrypted
// envelopes sealed under the keyring's primary key, each bound to loc plus its
// field path. It is intentionally narrow: only recognized credential field names
// are protected, while non-secret routing/query config remains ordinary JSON.
func EncryptSecrets(config json.RawMessage, kr *Keyring, loc SecretLocation) (json.RawMessage, error) {
	return transformSecrets(config, kr, loc, encryptSecretValue)
}

// SealConfigForRecord prepares a connector config for the ConnectorDefined event:
// with a keyring it seals credential fields (recoverable for fetch); WITHOUT one
// it redacts them. Either way no plaintext credential is persisted to the event
// log — so the default (no keyring) configuration is fail-safe, not a plaintext
// leak to the tenant-readable audit surface. A redacted (no-keyring) definition
// can still route, but its connector cannot authenticate until a keyring is
// configured and the definition is re-saved.
func SealConfigForRecord(config json.RawMessage, kr *Keyring, loc SecretLocation) (json.RawMessage, error) {
	if kr == nil {
		return RedactConfig(config), nil
	}
	return EncryptSecrets(config, kr, loc)
}

// DecryptSecrets opens encrypted credential envelopes in config, selecting the
// key that sealed each value and recomputing each field's AAD from loc. Plaintext
// configs continue to work, which keeps dev/test definitions and old logs readable;
// legacy (v1, no-AAD) envelopes open without an AAD.
func DecryptSecrets(config json.RawMessage, kr *Keyring, loc SecretLocation) (json.RawMessage, error) {
	return transformSecrets(config, kr, loc, decryptSecretValue)
}

type secretFn func(v any, kr *Keyring, loc SecretLocation, fieldPath string) (any, error)

func transformSecrets(
	config json.RawMessage,
	kr *Keyring,
	loc SecretLocation,
	fn secretFn,
) (json.RawMessage, error) {
	if len(config) == 0 || kr == nil {
		return config, nil
	}
	var v any
	if err := json.Unmarshal(config, &v); err != nil {
		return nil, fmt.Errorf("context-layer: connector credential config: %w", err)
	}
	out, err := walkSecretFields(v, kr, loc, "", fn)
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
	loc SecretLocation,
	path string,
	fn secretFn,
) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			var err error
			if redactKeys[strings.ToLower(k)] {
				t[k], err = fn(val, kr, loc, joinPath(path, k))
			} else {
				t[k], err = walkSecretFields(val, kr, loc, joinPath(path, k), fn)
			}
			if err != nil {
				return nil, err
			}
		}
		return t, nil
	case []any:
		for i := range t {
			val, err := walkSecretFields(t[i], kr, loc, indexPath(path, i), fn)
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

// joinPath/indexPath build a stable dotted field path (e.g. "auth.token",
// "headers.0.value") so each credential's AAD is unique to its position and stays
// the same across seal and open.
func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func indexPath(prefix string, i int) string {
	return fmt.Sprintf("%s.%d", prefix, i)
}

// encryptSecretValue seals one credential field's value into an inline envelope
// object (kept as a nested object — not standalone bytes — so a connector config
// stays human-inspectable and its on-the-wire shape is unchanged). New envelopes
// carry the v2 marker and bind the field's location AAD.
func encryptSecretValue(v any, kr *Keyring, loc SecretLocation, fieldPath string) (any, error) {
	if isSecretEnvelope(v) {
		return v, nil
	}
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("context-layer: connector secret encode: %w", err)
	}
	keyID, sealed, err := kr.SealValue(plain, loc.aadFor(fieldPath))
	if err != nil {
		return nil, err
	}
	return secretbox.Envelope{
		Version: secretbox.VersionAAD,
		Key:     keyID,
		Value:   base64.StdEncoding.EncodeToString(sealed),
	}, nil
}

func decryptSecretValue(v any, kr *Keyring, loc SecretLocation, fieldPath string) (any, error) {
	m, ok := v.(map[string]any)
	if !ok || !isSealedMarker(m["$intraktible_sealed"]) {
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
	// A v1 (legacy) envelope was sealed with no AAD; only v2 envelopes are bound to
	// the field location. Pass the AAD only for v2 so existing data keeps decrypting.
	var aad []byte
	if m["$intraktible_sealed"] == secretbox.VersionAAD {
		aad = loc.aadFor(fieldPath)
	}
	plain, err := kr.OpenValue(stringValue(m["key"]), sealed, aad)
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

// isSealedMarker reports whether a $intraktible_sealed value is one of the
// recognized envelope versions (v1 legacy or v2 AAD-bound).
func isSealedMarker(v any) bool {
	return v == secretbox.Version || v == secretbox.VersionAAD
}

func isSecretEnvelope(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || !isSealedMarker(m["$intraktible_sealed"]) {
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
