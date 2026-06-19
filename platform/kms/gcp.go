// SPDX-License-Identifier: AGPL-3.0-or-later

package kms

import (
	"context"
	"fmt"
	"hash/crc32"

	gcpkms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// kmsClient is the subset of the Cloud KMS client used here. It is an interface so
// the CRC32C end-to-end integrity checks can be exercised with a fake in tests.
type kmsClient interface {
	Encrypt(ctx context.Context, req *kmspb.EncryptRequest, opts ...gax.CallOption) (*kmspb.EncryptResponse, error)
	Decrypt(ctx context.Context, req *kmspb.DecryptRequest, opts ...gax.CallOption) (*kmspb.DecryptResponse, error)
}

// gcpKMS encrypts/decrypts directly with a GCP Cloud KMS crypto key.
type gcpKMS struct {
	client  kmsClient
	keyName string
}

// openGCP builds a Cloud KMS client from Application Default Credentials.
func openGCP(ctx context.Context, keyName string) (KMS, error) {
	client, err := gcpkms.NewKeyManagementClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("kms: gcp client: %w", err)
	}
	return &gcpKMS{client: client, keyName: keyName}, nil
}

var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

// crc32c is the checksum Cloud KMS uses for in-transit integrity (Castagnoli).
func crc32c(data []byte) int64 { return int64(crc32.Checksum(data, crc32cTable)) }

func (g *gcpKMS) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	resp, err := g.client.Encrypt(ctx, &kmspb.EncryptRequest{
		Name:            g.keyName,
		Plaintext:       plaintext,
		PlaintextCrc32C: wrapperspb.Int64(crc32c(plaintext)),
	})
	if err != nil {
		return nil, fmt.Errorf("kms: gcp encrypt: %w", err)
	}
	// End-to-end integrity (https://cloud.google.com/kms/docs/data-integrity-guidelines):
	// confirm KMS received our plaintext intact, and that the ciphertext reached us intact.
	if !resp.GetVerifiedPlaintextCrc32C() {
		return nil, fmt.Errorf("kms: gcp encrypt: request corrupted in transit (plaintext CRC32C not verified)")
	}
	if resp.GetCiphertextCrc32C().GetValue() != crc32c(resp.GetCiphertext()) {
		return nil, fmt.Errorf("kms: gcp encrypt: response corrupted in transit (ciphertext CRC32C mismatch)")
	}
	return resp.GetCiphertext(), nil
}

func (g *gcpKMS) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	resp, err := g.client.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:             g.keyName,
		Ciphertext:       ciphertext,
		CiphertextCrc32C: wrapperspb.Int64(crc32c(ciphertext)),
	})
	if err != nil {
		return nil, fmt.Errorf("kms: gcp decrypt: %w", err)
	}
	// KMS rejects a corrupted request (CiphertextCrc32C mismatch) with an RPC error;
	// here we confirm the returned plaintext reached us intact.
	if resp.GetPlaintextCrc32C().GetValue() != crc32c(resp.GetPlaintext()) {
		return nil, fmt.Errorf("kms: gcp decrypt: response corrupted in transit (plaintext CRC32C mismatch)")
	}
	return resp.GetPlaintext(), nil
}
