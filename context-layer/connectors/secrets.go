// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/e6qu/intraktible/platform/kms"
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

// Keyring holds one or more connector secret keys. The primary key seals new
// values; any key can open a value sealed under it. This enables rotation
// without downtime: promote a new key to primary and keep the old key for
// decrypting values it sealed, until everything has been re-sealed.
type Keyring struct {
	primaryID string
	byID      map[string]SecretBox
	order     []string // decrypt-attempt order for legacy (no key id) envelopes
}

// KeyFingerprint derives a short, stable id from a key's bytes. Deriving the id
// from the key means rotation needs no separate id bookkeeping: the same key
// always maps to the same id, so a value records which key sealed it.
func KeyFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:6])
}

// NewKeyring builds a keyring. The first key is the primary (used to seal new
// values); the rest are retained for decryption only. Each key's id is derived
// from its bytes.
func NewKeyring(keys ...[]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("context-layer: connector keyring needs at least one key")
	}
	kr := &Keyring{byID: make(map[string]SecretBox, len(keys))}
	for i, key := range keys {
		box, err := NewAESGCMSecretBox(key)
		if err != nil {
			return nil, err
		}
		id := KeyFingerprint(key)
		if _, dup := kr.byID[id]; dup {
			continue // the same key listed twice — keep one entry
		}
		kr.byID[id] = box
		kr.order = append(kr.order, id)
		if i == 0 {
			kr.primaryID = id
		}
	}
	return kr, nil
}

// NewKMSKeyring builds a keyring whose single key is backed by an external KMS
// (AWS/GCP) — the key material never leaves the provider. keyID labels the
// sealed envelopes (so a later rotation could add more keys for decrypt). The
// rest of the seal/open path is unchanged: KMS is just the SecretBox.
func NewKMSKeyring(keyID string, k kms.KMS) *Keyring {
	return &Keyring{
		primaryID: keyID,
		byID:      map[string]SecretBox{keyID: kmsBox{kms: k}},
		order:     []string{keyID},
	}
}

// kmsBox adapts an external KMS to the local SecretBox interface. SecretBox has
// no context, so KMS calls use a background context (the SDKs carry their own
// timeouts).
type kmsBox struct{ kms kms.KMS }

func (b kmsBox) Encrypt(plain []byte) ([]byte, error) {
	return b.kms.Encrypt(context.Background(), plain)
}

func (b kmsBox) Decrypt(ciphertext []byte) ([]byte, error) {
	return b.kms.Decrypt(context.Background(), ciphertext)
}

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

type secretEnvelope struct {
	Version string `json:"$intraktible_sealed"`
	Key     string `json:"key,omitempty"`
	Value   string `json:"value"`
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

func encryptSecretValue(v any, kr *Keyring) (any, error) {
	if isSecretEnvelope(v) {
		return v, nil
	}
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("context-layer: connector secret encode: %w", err)
	}
	sealed, err := kr.byID[kr.primaryID].Encrypt(plain)
	if err != nil {
		return nil, err
	}
	return secretEnvelope{
		Version: sealedEnvelopeVersion,
		Key:     kr.primaryID,
		Value:   base64.StdEncoding.EncodeToString(sealed),
	}, nil
}

func decryptSecretValue(v any, kr *Keyring) (any, error) {
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
	plain, err := kr.open(stringValue(m["key"]), sealed)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("context-layer: connector secret plaintext decode: %w", err)
	}
	return out, nil
}

// open decrypts a sealed value. With a key id it uses exactly that key (failing
// loudly if the keyring lacks it). Without one — a value sealed before key ids
// were recorded — it tries each key in turn; AEAD authentication rejects the
// wrong key, so only the right one opens it.
func (kr *Keyring) open(keyID string, sealed []byte) ([]byte, error) {
	if keyID != "" {
		box, ok := kr.byID[keyID]
		if !ok {
			return nil, fmt.Errorf("context-layer: connector secret sealed under unknown key %q", keyID)
		}
		return box.Decrypt(sealed)
	}
	for _, id := range kr.order {
		if plain, err := kr.byID[id].Decrypt(sealed); err == nil {
			return plain, nil
		}
	}
	return nil, fmt.Errorf("context-layer: connector secret: no key in the ring could open it")
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func isSecretEnvelope(v any) bool {
	m, ok := v.(map[string]any)
	return ok && m["$intraktible_sealed"] == sealedEnvelopeVersion
}
