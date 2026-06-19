// SPDX-License-Identifier: AGPL-3.0-or-later

package kms

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
)

// awsKMS encrypts/decrypts directly with an AWS KMS key (connector secrets are
// small, well under the 4KB Encrypt limit, so no envelope DEK is needed).
type awsKMS struct {
	client *awskms.Client
	keyID  string
}

// openAWS builds an AWS KMS client from the default credential chain (env,
// shared config, instance/role) — operators supply credentials the standard way.
func openAWS(ctx context.Context, keyID string) (KMS, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("kms: aws config: %w", err)
	}
	return &awsKMS{client: awskms.NewFromConfig(cfg), keyID: keyID}, nil
}

func (a *awsKMS) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	out, err := a.client.Encrypt(ctx, &awskms.EncryptInput{KeyId: &a.keyID, Plaintext: plaintext})
	if err != nil {
		return nil, fmt.Errorf("kms: aws encrypt: %w", err)
	}
	return out.CiphertextBlob, nil
}

func (a *awsKMS) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	out, err := a.client.Decrypt(ctx, &awskms.DecryptInput{KeyId: &a.keyID, CiphertextBlob: ciphertext})
	if err != nil {
		return nil, fmt.Errorf("kms: aws decrypt: %w", err)
	}
	return out.Plaintext, nil
}
