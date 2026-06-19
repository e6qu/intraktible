// SPDX-License-Identifier: AGPL-3.0-or-later

package kms

import (
	"context"
	"fmt"

	gcpkms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
)

// gcpKMS encrypts/decrypts directly with a GCP Cloud KMS crypto key.
type gcpKMS struct {
	client  *gcpkms.KeyManagementClient
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

func (g *gcpKMS) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	resp, err := g.client.Encrypt(ctx, &kmspb.EncryptRequest{Name: g.keyName, Plaintext: plaintext})
	if err != nil {
		return nil, fmt.Errorf("kms: gcp encrypt: %w", err)
	}
	return resp.GetCiphertext(), nil
}

func (g *gcpKMS) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	resp, err := g.client.Decrypt(ctx, &kmspb.DecryptRequest{Name: g.keyName, Ciphertext: ciphertext})
	if err != nil {
		return nil, fmt.Errorf("kms: gcp decrypt: %w", err)
	}
	return resp.GetPlaintext(), nil
}
