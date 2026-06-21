// SPDX-License-Identifier: AGPL-3.0-or-later

package secretbox

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// DecodeKey decodes a 32-byte (AES-256) key from a string, trying standard and
// URL base64 (padded or raw) then hex — so operators can supply the key in
// whichever encoding their secret manager emits. It fails loudly on the wrong
// length so a truncated/garbled key is caught at boot, not at first decrypt.
func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	} {
		if b, err := dec(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("secretbox: key must decode (base64 or hex) to exactly 32 bytes")
}

// KeyringFromKeys builds a keyring from a primary key plus any previous keys
// (retained for decrypt during rotation), each a base64/hex-encoded 32-byte key.
// An empty primary returns (nil, nil) — encryption disabled — so callers can treat
// "no key configured" uniformly.
func KeyringFromKeys(primary string, previous ...string) (*Keyring, error) {
	if strings.TrimSpace(primary) == "" {
		return nil, nil
	}
	keys := make([][]byte, 0, 1+len(previous))
	pk, err := DecodeKey(primary)
	if err != nil {
		return nil, fmt.Errorf("secretbox: primary key: %w", err)
	}
	keys = append(keys, pk)
	for _, p := range previous {
		if strings.TrimSpace(p) == "" {
			continue
		}
		k, err := DecodeKey(p)
		if err != nil {
			return nil, fmt.Errorf("secretbox: previous key: %w", err)
		}
		keys = append(keys, k)
	}
	return NewKeyring(keys...)
}
