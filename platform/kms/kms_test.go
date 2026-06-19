// SPDX-License-Identifier: AGPL-3.0-or-later

package kms_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/platform/kms"
)

func TestFakeRoundTrips(t *testing.T) {
	ctx := context.Background()
	f := kms.Fake{}
	ct, err := f.Encrypt(ctx, []byte("super-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ct) == "super-secret" {
		t.Fatal("ciphertext must not equal plaintext")
	}
	pt, err := f.Decrypt(ctx, ct)
	if err != nil || string(pt) != "super-secret" {
		t.Fatalf("decrypt = %q err=%v", pt, err)
	}
	if _, err := f.Decrypt(ctx, []byte("not-a-fake-ciphertext")); err == nil {
		t.Fatal("decrypt should reject foreign ciphertext")
	}
}

func TestFromEnvSelectsProvider(t *testing.T) {
	ctx := context.Background()

	t.Setenv("INTRAKTIBLE_KMS_PROVIDER", "")
	if k, err := kms.FromEnv(ctx); k != nil || err != nil {
		t.Fatalf("no provider should yield (nil,nil), got (%v,%v)", k, err)
	}

	// A configured provider with no key is an error.
	t.Setenv("INTRAKTIBLE_KMS_PROVIDER", "aws")
	t.Setenv("INTRAKTIBLE_KMS_KEY", "")
	if _, err := kms.FromEnv(ctx); err == nil {
		t.Fatal("missing key should error")
	}

	// An unknown provider is an error.
	t.Setenv("INTRAKTIBLE_KMS_PROVIDER", "azure")
	t.Setenv("INTRAKTIBLE_KMS_KEY", "some-key")
	if _, err := kms.FromEnv(ctx); err == nil {
		t.Fatal("unknown provider should error")
	}
}
