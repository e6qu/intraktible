// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestExternalArtifactRegistrationVerifiesSupplyChainEvidence(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	contentHash := sha256.Sum256([]byte("external artifact bytes"))
	signature := ed25519.Sign(privateKey, contentHash[:])
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	registration := ArtifactRegistration{
		ArtifactID: "external-risk-v1", ModelName: "risk-v1", OwnerTeam: "risk-science",
		Format: "onnx/v1", Runtime: "isolated-serving/v1", SizeBytes: 4096,
		ArtifactHash:   hex.EncodeToString(contentHash[:]),
		Signature:      base64.RawURLEncoding.EncodeToString(signature),
		PublicKey:      base64.RawURLEncoding.EncodeToString(publicKey),
		StorageRef:     "s3://model-artifacts/risk-v1.onnx",
		SourceRevision: "git:0123456789ab", BuildID: "build-1042",
		SBOMHash: strings.Repeat("a", 64),
		Dependencies: []ArtifactDependency{{
			Name: "onnxruntime", Version: "1.22.0", License: "MIT",
		}},
		Vulnerability: VulnerabilityEvidence{
			Scanner: "grype", ScannerVersion: "0.100.0", ScannedAt: now,
			ReportHash: strings.Repeat("b", 64),
		},
		Explanation: ExplanationContract{
			Limitations: "No platform-verified faithful explanation is available.",
		},
		Purpose: "model-development", RetentionUntil: now.Add(365 * 24 * time.Hour),
	}
	if err := registration.Validate(now); err != nil {
		t.Fatal(err)
	}
	registration.Signature = base64.RawURLEncoding.EncodeToString(
		ed25519.Sign(privateKey, []byte("different hash")),
	)
	if err := registration.Validate(now); err == nil {
		t.Fatal("invalid artifact signature was accepted")
	}
}

func TestArtifactPromotionIsOrdered(t *testing.T) {
	t.Parallel()
	for _, transition := range [][2]ArtifactStage{
		{ArtifactRegistered, ArtifactValidated},
		{ArtifactValidated, ArtifactProduction},
		{ArtifactRegistered, ArtifactArchived},
		{ArtifactValidated, ArtifactArchived},
		{ArtifactProduction, ArtifactArchived},
	} {
		if err := ValidateArtifactTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("%s -> %s: %v", transition[0], transition[1], err)
		}
	}
	if err := ValidateArtifactTransition(ArtifactRegistered, ArtifactProduction); err == nil {
		t.Fatal("artifact skipped independent validation")
	}
}
