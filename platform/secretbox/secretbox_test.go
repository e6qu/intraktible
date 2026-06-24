// SPDX-License-Identifier: AGPL-3.0-or-later

package secretbox_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/secretbox"
)

func key(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func TestAESGCMRoundTrip(t *testing.T) {
	box, err := secretbox.NewAESGCMSecretBox(key(1))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := box.Encrypt([]byte("hello"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("hello")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	plain, err := box.Decrypt(ct, nil)
	if err != nil || string(plain) != "hello" {
		t.Fatalf("decrypt = %q, %v", plain, err)
	}
}

// A value sealed with an AAD opens only when given the same AAD; a different (or
// absent) AAD fails authentication — the per-location replay defense.
func TestAESGCMAADBinding(t *testing.T) {
	box, _ := secretbox.NewAESGCMSecretBox(key(1))
	ct, err := box.Encrypt([]byte("hello"), []byte("loc-a"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Decrypt(ct, []byte("loc-a"))
	if err != nil || string(plain) != "hello" {
		t.Fatalf("same aad must open: %q, %v", plain, err)
	}
	if _, err := box.Decrypt(ct, []byte("loc-b")); err == nil {
		t.Fatal("a different aad must not open the ciphertext")
	}
	if _, err := box.Decrypt(ct, nil); err == nil {
		t.Fatal("an absent aad must not open an aad-bound ciphertext")
	}
}

func TestAESGCMWrongKeyFails(t *testing.T) {
	a, _ := secretbox.NewAESGCMSecretBox(key(1))
	b, _ := secretbox.NewAESGCMSecretBox(key(2))
	ct, _ := a.Encrypt([]byte("secret"), nil)
	if _, err := b.Decrypt(ct, nil); err == nil {
		t.Fatal("a different key must not open the ciphertext")
	}
}

func TestNewAESGCMRejectsShortKey(t *testing.T) {
	if _, err := secretbox.NewAESGCMSecretBox([]byte("short")); err == nil {
		t.Fatal("expected an error for a non-32-byte key")
	}
}

func TestKeyringSealOpenRoundTrip(t *testing.T) {
	kr, err := secretbox.NewKeyring(key(1))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := kr.Seal([]byte(`{"a":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !secretbox.IsSealed(sealed) {
		t.Fatal("Seal output should be recognized as sealed")
	}
	plain, err := kr.Open(sealed, nil)
	if err != nil || string(plain) != `{"a":1}` {
		t.Fatalf("open = %q, %v", plain, err)
	}
}

// A v2 envelope sealed with an AAD opens with the same AAD and rejects a different
// one; the round trip works through the JSON envelope (Seal/Open), not just the box.
func TestKeyringSealOpenAAD(t *testing.T) {
	kr, _ := secretbox.NewKeyring(key(1))
	aad := []byte("org\x00ws\x00conn\x00token")
	sealed, err := kr.Seal([]byte(`"s3cr3t"`), aad)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sealed), secretbox.VersionAAD) {
		t.Fatalf("an aad seal should carry the v2 marker: %s", sealed)
	}
	plain, err := kr.Open(sealed, aad)
	if err != nil || string(plain) != `"s3cr3t"` {
		t.Fatalf("same aad must open: %q, %v", plain, err)
	}
	if _, err := kr.Open(sealed, []byte("org\x00ws\x00conn\x00other")); err == nil {
		t.Fatal("a different aad must not open the v2 envelope (replay defense)")
	}
}

// A v1 (legacy, no-AAD) envelope must still open under the new Open, regardless of
// any AAD passed — backward compatibility for all pre-existing sealed data. The
// fixture is the exact v1 wire form (built by hand so it does not depend on the
// current seal path).
func TestKeyringOpenV1Legacy(t *testing.T) {
	kr, _ := secretbox.NewKeyring(key(1))
	box, _ := secretbox.NewAESGCMSecretBox(key(1))
	raw, _ := box.Encrypt([]byte(`"legacy"`), nil) // v1 sealed with no aad
	env := secretbox.Envelope{
		Version: secretbox.Version,
		Key:     secretbox.KeyFingerprint(key(1)),
		Value:   base64.StdEncoding.EncodeToString(raw),
	}
	envelope, _ := json.Marshal(env)
	// Opens whether or not an aad is supplied — a v1 envelope ignores it.
	for _, aad := range [][]byte{nil, []byte("some\x00aad")} {
		plain, err := kr.Open(envelope, aad)
		if err != nil || string(plain) != `"legacy"` {
			t.Fatalf("legacy v1 envelope must open (aad=%q): %q, %v", aad, plain, err)
		}
	}
}

// A value sealed under the old key still opens when the old key is retained in the
// ring (rotation); a ring without it cannot open it.
func TestKeyringRotation(t *testing.T) {
	old, _ := secretbox.NewKeyring(key(1))
	sealed, _ := old.Seal([]byte("v"), nil)

	rotated, _ := secretbox.NewKeyring(key(2), key(1)) // new primary, old retained
	plain, err := rotated.Open(sealed, nil)
	if err != nil || string(plain) != "v" {
		t.Fatalf("rotated ring must open old value: %q, %v", plain, err)
	}
	// A new value seals under the new primary's fingerprint.
	reSealed, _ := rotated.Seal([]byte("w"), nil)
	if !strings.Contains(string(reSealed), secretbox.KeyFingerprint(key(2))) {
		t.Fatalf("re-sealed value should record the new key id: %s", reSealed)
	}

	newOnly, _ := secretbox.NewKeyring(key(2))
	if _, err := newOnly.Open(sealed, nil); err == nil {
		t.Fatal("a ring without the old key must not open the old value")
	}
}

func TestIsSealed(t *testing.T) {
	kr, _ := secretbox.NewKeyring(key(1))
	sealed, _ := kr.Seal([]byte("x"), nil)
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"sealed envelope", string(sealed), true},
		{"v2 sealed envelope", `{"$intraktible_sealed":"intraktible.sealed.v2","key":"abc","value":"x"}`, true},
		{"v1 sealed envelope", `{"$intraktible_sealed":"intraktible.sealed.v1","value":"x"}`, true},
		{"plaintext object", `{"score":5}`, false},
		{"plaintext array", `[1,2,3]`, false},
		// A doc that merely carries the marker field but not the exact shape is NOT sealed.
		{"lookalike", `{"$intraktible_sealed":"intraktible.sealed.v1","value":"x","extra":1}`, false},
		{"not json", `nonsense`, false},
	}
	for _, c := range cases {
		if got := secretbox.IsSealed([]byte(c.in)); got != c.want {
			t.Errorf("%s: IsSealed = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDecodeKey(t *testing.T) {
	raw := key(7)
	// hex encoding decodes.
	if got, err := secretbox.DecodeKey(hex.EncodeToString(raw)); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("hex decode: %v", err)
	}
	// wrong length fails.
	if _, err := secretbox.DecodeKey(hex.EncodeToString([]byte("tooshort"))); err == nil {
		t.Fatal("expected a length error")
	}
}

func TestKeyringFromKeys(t *testing.T) {
	// Empty primary -> nil (encryption disabled), no error.
	kr, err := secretbox.KeyringFromKeys("")
	if err != nil || kr != nil {
		t.Fatalf("empty primary = %v, %v", kr, err)
	}
	// Valid primary + previous builds a ring that opens both keys' values.
	kr, err = secretbox.KeyringFromKeys(hex.EncodeToString(key(2)), hex.EncodeToString(key(1)))
	if err != nil || kr == nil {
		t.Fatalf("build: %v", err)
	}
	old, _ := secretbox.NewKeyring(key(1))
	sealed, _ := old.Seal([]byte("z"), nil)
	if plain, err := kr.Open(sealed, nil); err != nil || string(plain) != "z" {
		t.Fatalf("ring should open the previous key's value: %q, %v", plain, err)
	}
	// A malformed key is a loud error.
	if _, err := secretbox.KeyringFromKeys("not-a-key"); err == nil {
		t.Fatal("expected an error for a malformed primary key")
	}
}
