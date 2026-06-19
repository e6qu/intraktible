// SPDX-License-Identifier: AGPL-3.0-or-later

// Package kms wraps an externally-managed key service (AWS KMS, GCP Cloud KMS)
// behind a small Encrypt/Decrypt interface. It lets connector credentials be
// sealed by a key that never leaves the provider — the upgrade path from the
// env-supplied local keys, for operators who require a managed vault.
package kms

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/e6qu/intraktible/platform/mo"
)

// KMS encrypts and decrypts small secrets with a managed key. Implementations
// call out to the provider, so callers should treat both methods as I/O.
type KMS interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// FromEnv builds the KMS selected by INTRAKTIBLE_KMS_PROVIDER (aws|gcp) using
// INTRAKTIBLE_KMS_KEY (an AWS key id/ARN, or a GCP key resource name like
// projects/p/locations/l/keyRings/r/cryptoKeys/k). It returns None when no
// provider is configured (callers fall back to local keys) and an error only on
// a misconfigured provider — so "absent" can never be confused with a
// constructed-but-nil KMS.
func FromEnv(ctx context.Context) (mo.Option[KMS], error) {
	provider := strings.TrimSpace(os.Getenv("INTRAKTIBLE_KMS_PROVIDER"))
	if provider == "" {
		return mo.None[KMS](), nil
	}
	key := strings.TrimSpace(os.Getenv("INTRAKTIBLE_KMS_KEY"))
	if key == "" {
		return mo.None[KMS](), fmt.Errorf("kms: INTRAKTIBLE_KMS_KEY is required for provider %q", provider)
	}
	var (
		k   KMS
		err error
	)
	switch provider {
	case "aws":
		k, err = openAWS(ctx, key)
	case "gcp":
		k, err = openGCP(ctx, key)
	default:
		return mo.None[KMS](), fmt.Errorf("kms: unknown provider %q (aws|gcp)", provider)
	}
	if err != nil {
		return mo.None[KMS](), err
	}
	return mo.Some(k), nil
}

// Fake is an in-memory KMS for tests: a reversible byte transform, NOT real
// encryption. It exists so the secret-sealing plumbing can be exercised without
// a cloud provider; never use it in production.
type Fake struct{}

const fakeMarker = "fakekms:"

// Encrypt tags and obfuscates the plaintext (test-only, reversible).
func (Fake) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	return append([]byte(fakeMarker), flip(plaintext)...), nil
}

// Decrypt reverses Encrypt, failing on anything it did not produce.
func (Fake) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	if !strings.HasPrefix(string(ciphertext), fakeMarker) {
		return nil, fmt.Errorf("kms: fake: not a fake-kms ciphertext")
	}
	return flip(ciphertext[len(fakeMarker):]), nil
}

func flip(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = c ^ 0x5a
	}
	return out
}
