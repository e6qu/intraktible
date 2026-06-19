// SPDX-License-Identifier: AGPL-3.0-or-later

package kms

import (
	"context"
	"strings"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// fakeKMS mimics Cloud KMS's CRC32C contract: it verifies the CRC32C the client
// sent and stamps the response checksums. Knobs let a test force a corruption.
type fakeKMS struct {
	skipVerifyPlaintext bool // pretend the request plaintext CRC32C didn't verify
	corruptCiphertext   bool // return a ciphertext CRC32C that won't match
	corruptPlaintext    bool // return a plaintext CRC32C that won't match (decrypt)
}

func (f *fakeKMS) Encrypt(_ context.Context, req *kmspb.EncryptRequest, _ ...gax.CallOption) (*kmspb.EncryptResponse, error) {
	ct := append([]byte("ct:"), req.GetPlaintext()...)
	sum := crc32c(ct)
	if f.corruptCiphertext {
		sum++
	}
	return &kmspb.EncryptResponse{
		Ciphertext:              ct,
		CiphertextCrc32C:        wrapperspb.Int64(sum),
		VerifiedPlaintextCrc32C: !f.skipVerifyPlaintext && req.GetPlaintextCrc32C().GetValue() == crc32c(req.GetPlaintext()),
	}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, req *kmspb.DecryptRequest, _ ...gax.CallOption) (*kmspb.DecryptResponse, error) {
	pt := []byte(strings.TrimPrefix(string(req.GetCiphertext()), "ct:"))
	sum := crc32c(pt)
	if f.corruptPlaintext {
		sum++
	}
	return &kmspb.DecryptResponse{Plaintext: pt, PlaintextCrc32C: wrapperspb.Int64(sum)}, nil
}

func TestGCPKMSRoundTripsWithIntegrity(t *testing.T) {
	g := &gcpKMS{client: &fakeKMS{}, keyName: "k"}
	ct, err := g.Encrypt(context.Background(), []byte("secret"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	pt, err := g.Decrypt(context.Background(), ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(pt) != "secret" {
		t.Fatalf("round trip: got %q", pt)
	}
}

func TestGCPKMSDetectsCorruption(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		client *fakeKMS
		op     string // "encrypt" or "decrypt"
		want   string
	}{
		{"unverified request", &fakeKMS{skipVerifyPlaintext: true}, "encrypt", "not verified"},
		{"corrupt ciphertext", &fakeKMS{corruptCiphertext: true}, "encrypt", "mismatch"},
		{"corrupt plaintext", &fakeKMS{corruptPlaintext: true}, "decrypt", "mismatch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := &gcpKMS{client: c.client, keyName: "k"}
			var err error
			if c.op == "encrypt" {
				_, err = g.Encrypt(ctx, []byte("secret"))
			} else {
				_, err = g.Decrypt(ctx, []byte("ct:secret"))
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}
