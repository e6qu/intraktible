// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const sealedEnvelopeVersion = "intraktible.sealed.v1"

// SecretBox encrypts and decrypts connector credential values before they are
// recorded in the event log or used by a connector at fetch time.
type SecretBox interface {
	Encrypt(plain []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// AESGCMSecretBox protects connector credentials with AES-256-GCM. The caller is
// responsible for supplying and retaining the key; losing it makes encrypted
// connector configs unreadable, by design.
type AESGCMSecretBox struct {
	aead cipher.AEAD
}

// NewAESGCMSecretBox builds a connector secret box from a 32-byte key.
func NewAESGCMSecretBox(key []byte) (*AESGCMSecretBox, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("context-layer: connector secret key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("context-layer: connector secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("context-layer: connector secret gcm: %w", err)
	}
	return &AESGCMSecretBox{aead: aead}, nil
}

// Encrypt seals plain with a fresh nonce. The nonce is prepended to the returned
// blob so Decrypt can open it later.
func (b *AESGCMSecretBox) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("context-layer: connector secret nonce: %w", err)
	}
	out := append([]byte{}, nonce...)
	out = b.aead.Seal(out, nonce, plain, nil)
	return out, nil
}

// Decrypt opens a blob produced by Encrypt.
func (b *AESGCMSecretBox) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < b.aead.NonceSize() {
		return nil, fmt.Errorf("context-layer: connector secret ciphertext too short")
	}
	nonce := ciphertext[:b.aead.NonceSize()]
	sealed := ciphertext[b.aead.NonceSize():]
	plain, err := b.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("context-layer: connector secret decrypt: %w", err)
	}
	return plain, nil
}

// EncryptSecrets returns config with credential fields replaced by encrypted
// envelopes. It is intentionally narrow: only recognized credential field names
// are protected, while non-secret routing/query config remains ordinary JSON.
func EncryptSecrets(config json.RawMessage, box SecretBox) (json.RawMessage, error) {
	return transformSecrets(config, box, encryptSecretValue)
}

// DecryptSecrets opens encrypted credential envelopes in config. Plaintext
// configs continue to work, which keeps dev/test definitions and old logs
// readable.
func DecryptSecrets(config json.RawMessage, box SecretBox) (json.RawMessage, error) {
	return transformSecrets(config, box, decryptSecretValue)
}

type secretEnvelope struct {
	Version string `json:"$intraktible_sealed"`
	Value   string `json:"value"`
}

func transformSecrets(
	config json.RawMessage,
	box SecretBox,
	fn func(any, SecretBox) (any, error),
) (json.RawMessage, error) {
	if len(config) == 0 || box == nil {
		return config, nil
	}
	var v any
	if err := json.Unmarshal(config, &v); err != nil {
		return nil, fmt.Errorf("context-layer: connector credential config: %w", err)
	}
	out, err := walkSecretFields(v, box, fn)
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
	box SecretBox,
	fn func(any, SecretBox) (any, error),
) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			var err error
			if redactKeys[strings.ToLower(k)] {
				t[k], err = fn(val, box)
			} else {
				t[k], err = walkSecretFields(val, box, fn)
			}
			if err != nil {
				return nil, err
			}
		}
		return t, nil
	case []any:
		for i := range t {
			val, err := walkSecretFields(t[i], box, fn)
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

func encryptSecretValue(v any, box SecretBox) (any, error) {
	if isSecretEnvelope(v) {
		return v, nil
	}
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("context-layer: connector secret encode: %w", err)
	}
	sealed, err := box.Encrypt(plain)
	if err != nil {
		return nil, err
	}
	return secretEnvelope{
		Version: sealedEnvelopeVersion,
		Value:   base64.StdEncoding.EncodeToString(sealed),
	}, nil
}

func decryptSecretValue(v any, box SecretBox) (any, error) {
	m, ok := v.(map[string]any)
	if !ok || m["$intraktible_sealed"] != sealedEnvelopeVersion {
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
	plain, err := box.Decrypt(sealed)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("context-layer: connector secret plaintext decode: %w", err)
	}
	return out, nil
}

func isSecretEnvelope(v any) bool {
	m, ok := v.(map[string]any)
	return ok && m["$intraktible_sealed"] == sealedEnvelopeVersion
}
