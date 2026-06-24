// SPDX-License-Identifier: AGPL-3.0-or-later

// Package secretbox is the shared symmetric-sealing primitive: AES-256-GCM with a
// key ring that supports rotation, plus a versioned, self-describing JSON envelope.
// It is the one place the AES-GCM construction lives, used by connector-credential
// sealing, crypto-shred erasure, and encryption-at-rest of the projection store and
// event log. Keys are the caller's responsibility — losing one makes the values it
// sealed permanently unreadable, by design.
package secretbox

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/e6qu/intraktible/platform/kms"
)

// Version marks a sealed envelope (the JSON wire form a sealed value takes). The
// marker doubles as the field name so a sealed value is recognizable on sight.
// Version is the legacy marker: its envelopes were sealed with no additional
// authenticated data (AAD), so they open without one. VersionAAD (v2) is the
// current marker: its envelopes bind an AAD into the AEAD, so a ciphertext cannot
// be replayed into a different record/tenant/field — Open authenticates the same
// AAD the caller supplies. Both are recognized on read so existing v1 data keeps
// decrypting; new seals always use VersionAAD.
const (
	Version    = "intraktible.sealed.v1"
	VersionAAD = "intraktible.sealed.v2"
)

// SecretBox encrypts and decrypts opaque byte blobs, binding aad into the
// ciphertext's authentication tag so a value sealed for one location cannot be
// opened as another. A nil aad means "no binding" (used by callers whose values
// are not location-scoped, and to reproduce the v1 construction).
type SecretBox interface {
	Encrypt(plain, aad []byte) ([]byte, error)
	Decrypt(ciphertext, aad []byte) ([]byte, error)
}

// AESGCMSecretBox seals values with AES-256-GCM. The caller supplies and retains
// the 32-byte key; losing it makes everything sealed under it unreadable.
type AESGCMSecretBox struct {
	aead cipher.AEAD
}

// NewAESGCMSecretBox builds a secret box from a 32-byte key.
func NewAESGCMSecretBox(key []byte) (*AESGCMSecretBox, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secretbox: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretbox: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: gcm: %w", err)
	}
	return &AESGCMSecretBox{aead: aead}, nil
}

// Encrypt seals plain with a fresh nonce, prepended to the returned blob so
// Decrypt can recover it. aad is bound into GCM's authentication tag (not stored),
// so Decrypt must be given the same aad; a nil aad reproduces the v1 construction.
func (b *AESGCMSecretBox) Encrypt(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secretbox: nonce: %w", err)
	}
	out := append([]byte{}, nonce...)
	return b.aead.Seal(out, nonce, plain, aad), nil
}

// Decrypt opens a blob produced by Encrypt. It must be given the same aad that
// sealed it; a mismatched (or absent) aad fails authentication.
func (b *AESGCMSecretBox) Decrypt(ciphertext, aad []byte) ([]byte, error) {
	if len(ciphertext) < b.aead.NonceSize() {
		return nil, fmt.Errorf("secretbox: ciphertext too short")
	}
	nonce := ciphertext[:b.aead.NonceSize()]
	sealed := ciphertext[b.aead.NonceSize():]
	plain, err := b.aead.Open(nil, nonce, sealed, aad)
	if err != nil {
		return nil, fmt.Errorf("secretbox: decrypt: %w", err)
	}
	return plain, nil
}

// KeyFingerprint derives a short, stable id from a key's bytes. Deriving the id
// from the key means rotation needs no separate id bookkeeping: the same key
// always maps to the same id, so a sealed value can record which key sealed it.
func KeyFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:6])
}

// Keyring holds one or more keys. The primary key seals new values; any key can
// open a value sealed under it. This enables rotation without downtime: promote a
// new key to primary and keep old keys for decrypting values they sealed, until
// everything has been re-sealed.
type Keyring struct {
	primaryID string
	byID      map[string]SecretBox
	order     []string // decrypt-attempt order for legacy (no key id) envelopes
}

// NewKeyring builds a keyring. The first key is the primary (used to seal new
// values); the rest are retained for decryption only. Each key's id is derived
// from its bytes, and duplicate keys collapse to one entry.
func NewKeyring(keys ...[]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("secretbox: keyring needs at least one key")
	}
	kr := &Keyring{byID: make(map[string]SecretBox, len(keys))}
	for i, key := range keys {
		box, err := NewAESGCMSecretBox(key)
		if err != nil {
			return nil, err
		}
		id := KeyFingerprint(key)
		if _, dup := kr.byID[id]; dup {
			continue
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
// (AWS/GCP) — the key material never leaves the provider. keyID labels the sealed
// envelopes (so a later rotation could add more keys for decrypt). The rest of the
// seal/open path is unchanged: KMS is just the SecretBox.
func NewKMSKeyring(keyID string, k kms.KMS) *Keyring {
	return &Keyring{
		primaryID: keyID,
		byID:      map[string]SecretBox{keyID: kmsBox{kms: k}},
		order:     []string{keyID},
	}
}

// kmsBox adapts an external KMS to SecretBox. SecretBox has no context, so KMS
// calls use a background context (the SDKs carry their own timeouts). The provider
// Encrypt/Decrypt take no AAD, so the binding is carried inside the plaintext: aad
// is length-prefixed ahead of the value before sealing and verified on open, which
// is authenticated because the whole blob is. A nil aad prefixes a zero length, so
// it round-trips with no binding (matching the AES-GCM box's nil-aad behavior).
type kmsBox struct{ kms kms.KMS }

func (b kmsBox) Encrypt(plain, aad []byte) ([]byte, error) {
	return b.kms.Encrypt(context.Background(), prefixAAD(aad, plain))
}

func (b kmsBox) Decrypt(ciphertext, aad []byte) ([]byte, error) {
	framed, err := b.kms.Decrypt(context.Background(), ciphertext)
	if err != nil {
		return nil, err
	}
	return stripAAD(aad, framed)
}

// prefixAAD frames a blob as uvarint(len(aad))||aad||plain so the AAD travels with
// the (authenticated) ciphertext for providers that cannot take AAD natively. The
// uvarint length avoids any fixed-width integer conversion.
func prefixAAD(aad, plain []byte) []byte {
	var hdr [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(hdr[:], uint64(len(aad)))
	out := make([]byte, 0, n+len(aad)+len(plain))
	out = append(out, hdr[:n]...)
	out = append(out, aad...)
	out = append(out, plain...)
	return out
}

// stripAAD reverses prefixAAD, failing if the recovered AAD does not match the
// one the caller expects — the replay defense for the KMS path.
func stripAAD(aad, framed []byte) ([]byte, error) {
	u, hdr := binary.Uvarint(framed)
	if hdr <= 0 || u > maxFramedAAD {
		return nil, fmt.Errorf("secretbox: kms aad length unreadable")
	}
	n := int(u)
	if n > len(framed)-hdr {
		return nil, fmt.Errorf("secretbox: kms aad length out of range")
	}
	got := framed[hdr : hdr+n]
	if !bytesEqual(got, aad) {
		return nil, fmt.Errorf("secretbox: kms aad mismatch")
	}
	return framed[hdr+n:], nil
}

// maxFramedAAD caps a decoded AAD length so the uvarint-to-int conversion cannot
// overflow on any platform; real AADs are short identifier strings, far below it.
const maxFramedAAD = 1 << 20

// bytesEqual compares two byte slices, treating nil and empty as equal so a
// nil-aad seal opens with an empty (or nil) aad.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SealValue seals plain under the primary key and returns the key id alongside the
// raw sealed bytes — the low-level entry point for callers that embed the result in
// their own structure (e.g. the connector-config field walker). Most callers want
// Seal, which wraps this in a JSON envelope.
func (kr *Keyring) SealValue(plain, aad []byte) (keyID string, sealed []byte, err error) {
	sealed, err = kr.byID[kr.primaryID].Encrypt(plain, aad)
	if err != nil {
		return "", nil, err
	}
	return kr.primaryID, sealed, nil
}

// OpenValue decrypts raw sealed bytes. With a key id it uses exactly that key
// (failing loudly if the keyring lacks it). Without one — a value sealed before key
// ids were recorded — it tries each key in turn; AEAD authentication rejects the
// wrong key, so only the right one opens it.
func (kr *Keyring) OpenValue(keyID string, sealed, aad []byte) ([]byte, error) {
	if keyID != "" {
		box, ok := kr.byID[keyID]
		if !ok {
			return nil, fmt.Errorf("secretbox: value sealed under unknown key %q", keyID)
		}
		return box.Decrypt(sealed, aad)
	}
	for _, id := range kr.order {
		if plain, err := kr.byID[id].Decrypt(sealed, aad); err == nil {
			return plain, nil
		}
	}
	return nil, fmt.Errorf("secretbox: no key in the ring could open the value")
}

// Envelope is the JSON wire form of a sealed value: a self-describing object whose
// presence of the Version-tagged field marks it sealed, recording which key sealed
// it and the base64 of the nonce-prefixed ciphertext.
type Envelope struct {
	Version string `json:"$intraktible_sealed"`
	Key     string `json:"key,omitempty"`
	Value   string `json:"value"`
}

// Seal seals plain and returns it as a self-describing JSON envelope (valid JSON,
// so it can be stored anywhere the plaintext could — a JSONB column, an event
// payload), binding aad into the authentication tag. New envelopes carry the
// VersionAAD (v2) marker. Open reverses it. A nil aad is allowed (no binding).
func (kr *Keyring) Seal(plain, aad []byte) ([]byte, error) {
	keyID, sealed, err := kr.SealValue(plain, aad)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{
		Version: VersionAAD,
		Key:     keyID,
		Value:   base64.StdEncoding.EncodeToString(sealed),
	})
}

// Open reverses Seal, recovering the plaintext from a JSON envelope. It reads the
// envelope version: a v2 (VersionAAD) envelope is opened with the supplied aad, so
// a ciphertext transplanted to a different location fails authentication; a v1
// (legacy) envelope was sealed with no aad and is opened with none, so all existing
// sealed data keeps decrypting regardless of the aad passed here.
func (kr *Keyring) Open(envelope, aad []byte) ([]byte, error) {
	var e Envelope
	if err := json.Unmarshal(envelope, &e); err != nil {
		return nil, fmt.Errorf("secretbox: decode envelope: %w", err)
	}
	if e.Value == "" || (e.Version != Version && e.Version != VersionAAD) {
		return nil, fmt.Errorf("secretbox: not a sealed envelope")
	}
	sealed, err := base64.StdEncoding.DecodeString(e.Value)
	if err != nil {
		return nil, fmt.Errorf("secretbox: decode envelope value: %w", err)
	}
	if e.Version == Version {
		aad = nil
	}
	return kr.OpenValue(e.Key, sealed, aad)
}

// IsSealed reports whether b is a sealed envelope (vs plaintext). It requires
// EXACTLY the envelope shape so a plaintext document that merely carries a field
// named $intraktible_sealed is not mistaken for sealed (which would skip decryption
// or re-sealing). This is what lets sealed and plaintext values coexist, so enabling
// encryption needs no migration pass: old plaintext reads through, new writes seal.
func IsSealed(b []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	var ver string
	if err := json.Unmarshal(m["$intraktible_sealed"], &ver); err != nil || (ver != Version && ver != VersionAAD) {
		return false
	}
	var val string
	if err := json.Unmarshal(m["value"], &val); err != nil || val == "" {
		return false
	}
	for k := range m {
		if k != "$intraktible_sealed" && k != "value" && k != "key" {
			return false
		}
	}
	return true
}
