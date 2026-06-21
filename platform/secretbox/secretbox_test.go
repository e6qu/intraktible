// SPDX-License-Identifier: AGPL-3.0-or-later

package secretbox_test

import (
	"bytes"
	"encoding/hex"
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
	ct, err := box.Encrypt([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("hello")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	plain, err := box.Decrypt(ct)
	if err != nil || string(plain) != "hello" {
		t.Fatalf("decrypt = %q, %v", plain, err)
	}
}

func TestAESGCMWrongKeyFails(t *testing.T) {
	a, _ := secretbox.NewAESGCMSecretBox(key(1))
	b, _ := secretbox.NewAESGCMSecretBox(key(2))
	ct, _ := a.Encrypt([]byte("secret"))
	if _, err := b.Decrypt(ct); err == nil {
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
	sealed, err := kr.Seal([]byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !secretbox.IsSealed(sealed) {
		t.Fatal("Seal output should be recognized as sealed")
	}
	plain, err := kr.Open(sealed)
	if err != nil || string(plain) != `{"a":1}` {
		t.Fatalf("open = %q, %v", plain, err)
	}
}

// A value sealed under the old key still opens when the old key is retained in the
// ring (rotation); a ring without it cannot open it.
func TestKeyringRotation(t *testing.T) {
	old, _ := secretbox.NewKeyring(key(1))
	sealed, _ := old.Seal([]byte("v"))

	rotated, _ := secretbox.NewKeyring(key(2), key(1)) // new primary, old retained
	plain, err := rotated.Open(sealed)
	if err != nil || string(plain) != "v" {
		t.Fatalf("rotated ring must open old value: %q, %v", plain, err)
	}
	// A new value seals under the new primary's fingerprint.
	reSealed, _ := rotated.Seal([]byte("w"))
	if !strings.Contains(string(reSealed), secretbox.KeyFingerprint(key(2))) {
		t.Fatalf("re-sealed value should record the new key id: %s", reSealed)
	}

	newOnly, _ := secretbox.NewKeyring(key(2))
	if _, err := newOnly.Open(sealed); err == nil {
		t.Fatal("a ring without the old key must not open the old value")
	}
}

func TestIsSealed(t *testing.T) {
	kr, _ := secretbox.NewKeyring(key(1))
	sealed, _ := kr.Seal([]byte("x"))
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"sealed envelope", string(sealed), true},
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
	sealed, _ := old.Seal([]byte("z"))
	if plain, err := kr.Open(sealed); err != nil || string(plain) != "z" {
		t.Fatalf("ring should open the previous key's value: %q, %v", plain, err)
	}
	// A malformed key is a loud error.
	if _, err := secretbox.KeyringFromKeys("not-a-key"); err == nil {
		t.Fatal("expected an error for a malformed primary key")
	}
}
