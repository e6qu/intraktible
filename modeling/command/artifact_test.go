// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	modelingcmd "github.com/e6qu/intraktible/modeling/command"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestExternalArtifactRegistrationAndIndependentPromotionReplay(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	maker := identity.Identity{Org: "demo", Workspace: "main", Actor: "modeler"}
	validator := identity.Identity{Org: "demo", Workspace: "main", Actor: "validator"}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("signed external model"))
	registration := domain.ArtifactRegistration{
		ArtifactID: "external-risk-v1", ModelName: "risk-v1", OwnerTeam: "risk",
		Format: "onnx/v1", Runtime: "isolated-serving/v1", SizeBytes: 2048,
		ArtifactHash:   hex.EncodeToString(hash[:]),
		Signature:      base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, hash[:])),
		PublicKey:      base64.RawURLEncoding.EncodeToString(publicKey),
		StorageRef:     "artifact://registry/external-risk-v1",
		SourceRevision: "git:0123456789ab", BuildID: "build-1",
		SBOMHash: strings.Repeat("a", 64),
		Vulnerability: domain.VulnerabilityEvidence{
			Scanner: "scanner", ScannerVersion: "1.0.0", ScannedAt: now,
			ReportHash: strings.Repeat("b", 64),
		},
		Explanation: domain.ExplanationContract{
			Limitations: "Explanations are supplied by the isolated serving system and are not platform verified.",
		},
		Purpose: "model-development", RetentionUntil: now.Add(365 * 24 * time.Hour),
	}
	handler := modelingcmd.NewHandler(log).WithNow(func() time.Time { return now })
	if _, err := handler.RegisterExternalArtifact(ctx, maker, registration); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ChangeArtifactStage(
		ctx, maker, registration.ArtifactID, domain.ArtifactValidated, "self review",
	); err == nil {
		t.Fatal("artifact maker promoted their own artifact")
	}
	if _, err := handler.ChangeArtifactStage(
		ctx, validator, registration.ArtifactID, domain.ArtifactValidated,
		"signature, SBOM, scan, and limitations reviewed",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ChangeArtifactStage(
		ctx, validator, registration.ArtifactID, domain.ArtifactProduction,
		"production supply-chain gate passed",
	); err != nil {
		t.Fatal(err)
	}

	st := store.NewMemory()
	if _, err := projection.New(log, st, modelprojection.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	artifact, found, err := modelprojection.ReadArtifact(
		ctx, st, validator, registration.ArtifactID,
	)
	if err != nil || !found {
		t.Fatalf("artifact found=%v err=%v", found, err)
	}
	if artifact.Origin != domain.ArtifactExternal ||
		artifact.Stage != domain.ArtifactProduction ||
		artifact.ArtifactHash != registration.ArtifactHash ||
		len(artifact.StageHistory) != 3 {
		t.Fatalf("artifact = %+v", artifact)
	}
}
