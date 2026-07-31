// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ArtifactOrigin distinguishes reproducibly trained platform output from
// externally built bytes that the API never loads.
type ArtifactOrigin string

const (
	ArtifactPlatformTrained ArtifactOrigin = "platform_trained"
	ArtifactExternal        ArtifactOrigin = "external"
)

// ArtifactStage is the governed artifact supply-chain state.
type ArtifactStage string

const (
	ArtifactRegistered ArtifactStage = "registered"
	ArtifactValidated  ArtifactStage = "validated"
	ArtifactProduction ArtifactStage = "production"
	ArtifactArchived   ArtifactStage = "archived"
)

// ArtifactDependency is value-free SBOM metadata for one runtime dependency.
type ArtifactDependency struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Hash    string `json:"hash,omitempty"`
	License string `json:"license,omitempty"`
}

// VulnerabilityEvidence pins an external scanner result without embedding the
// potentially sensitive report.
type VulnerabilityEvidence struct {
	Scanner        string    `json:"scanner"`
	ScannerVersion string    `json:"scanner_version"`
	ScannedAt      time.Time `json:"scanned_at"`
	ReportHash     string    `json:"report_hash"`
	Critical       int       `json:"critical"`
	High           int       `json:"high"`
}

// ExplanationContract tells validators what explanation semantics are and are
// not available for this artifact.
type ExplanationContract struct {
	LocalSupported  bool   `json:"local_supported"`
	GlobalSupported bool   `json:"global_supported"`
	Method          string `json:"method,omitempty"`
	Limitations     string `json:"limitations"`
}

// ArtifactRegistration is immutable external artifact metadata. ArtifactHash is
// signed as its decoded SHA-256 bytes. StorageRef is a capability reference,
// never fetched during command handling.
type ArtifactRegistration struct {
	ArtifactID     string                `json:"artifact_id"`
	ModelName      string                `json:"model_name"`
	OwnerTeam      string                `json:"owner_team"`
	Format         string                `json:"format"`
	Runtime        string                `json:"runtime"`
	SizeBytes      int64                 `json:"size_bytes"`
	ArtifactHash   string                `json:"artifact_hash"`
	Signature      string                `json:"signature"`
	PublicKey      string                `json:"public_key"`
	StorageRef     string                `json:"storage_ref"`
	SourceRevision string                `json:"source_revision"`
	BuildID        string                `json:"build_id"`
	SBOMHash       string                `json:"sbom_hash"`
	Dependencies   []ArtifactDependency  `json:"dependencies"`
	Vulnerability  VulnerabilityEvidence `json:"vulnerability"`
	Explanation    ExplanationContract   `json:"explanation"`
	Purpose        string                `json:"purpose"`
	RetentionUntil time.Time             `json:"retention_until"`
}

// Validate verifies external supply-chain evidence without reading artifact
// bytes or executing/deserializing them.
func (registration ArtifactRegistration) Validate(now time.Time) error {
	switch {
	case strings.TrimSpace(registration.ArtifactID) == "" ||
		strings.Contains(registration.ArtifactID, "/"):
		return errors.New("modeling: artifact_id is required and must not contain '/'")
	case strings.TrimSpace(registration.ModelName) == "" ||
		strings.Contains(registration.ModelName, "/"):
		return errors.New("modeling: artifact model_name is required and must not contain '/'")
	case strings.TrimSpace(registration.OwnerTeam) == "":
		return errors.New("modeling: artifact owner_team is required")
	case strings.TrimSpace(registration.Format) == "" ||
		strings.TrimSpace(registration.Runtime) == "":
		return errors.New("modeling: artifact format and runtime are required")
	case registration.SizeBytes <= 0:
		return errors.New("modeling: artifact size_bytes must be positive")
	case len(registration.ArtifactHash) != 64:
		return errors.New("modeling: artifact_hash must be SHA-256 hex")
	case strings.TrimSpace(registration.SourceRevision) == "" ||
		strings.TrimSpace(registration.BuildID) == "":
		return errors.New("modeling: artifact source_revision and build_id are required")
	case len(registration.SBOMHash) != 64:
		return errors.New("modeling: artifact sbom_hash must be SHA-256 hex")
	case strings.TrimSpace(registration.Purpose) == "":
		return errors.New("modeling: artifact purpose is required")
	case registration.RetentionUntil.IsZero() || !registration.RetentionUntil.After(now):
		return errors.New("modeling: artifact retention_until must be in the future")
	case strings.TrimSpace(registration.Explanation.Limitations) == "":
		return errors.New("modeling: artifact explanation limitations are required")
	case registration.Explanation.LocalSupported &&
		strings.TrimSpace(registration.Explanation.Method) == "":
		return errors.New("modeling: supported local explanations require a method")
	case registration.Vulnerability.Critical != 0:
		return errors.New("modeling: artifacts with critical vulnerabilities cannot be registered")
	case registration.Vulnerability.High < 0 || registration.Vulnerability.Critical < 0:
		return errors.New("modeling: vulnerability counts must not be negative")
	case strings.TrimSpace(registration.Vulnerability.Scanner) == "" ||
		strings.TrimSpace(registration.Vulnerability.ScannerVersion) == "" ||
		registration.Vulnerability.ScannedAt.IsZero() ||
		len(registration.Vulnerability.ReportHash) != 64:
		return errors.New("modeling: complete vulnerability scan evidence is required")
	}
	if err := validateStorageCapability(registration.StorageRef); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, dependency := range registration.Dependencies {
		name, version := strings.TrimSpace(dependency.Name), strings.TrimSpace(dependency.Version)
		if name == "" || version == "" {
			return errors.New("modeling: artifact dependency name and version are required")
		}
		key := name + "@" + version
		if seen[key] {
			return fmt.Errorf("modeling: duplicate artifact dependency %q", key)
		}
		seen[key] = true
	}
	hash, err := hex.DecodeString(registration.ArtifactHash)
	if err != nil || len(hash) != 32 {
		return errors.New("modeling: artifact_hash must be SHA-256 hex")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(registration.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("modeling: artifact public_key is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(registration.Signature)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), hash, signature) {
		return errors.New("modeling: artifact signature is invalid")
	}
	return nil
}

func validateStorageCapability(reference string) error {
	if strings.ContainsAny(reference, "?#") {
		return errors.New("modeling: artifact storage_ref must not contain query or fragment secrets")
	}
	parsed, err := url.Parse(reference)
	if err != nil || parsed.User != nil {
		return errors.New("modeling: artifact storage_ref is invalid")
	}
	switch parsed.Scheme {
	case "artifact", "s3", "gs", "azure":
		if parsed.Host == "" && strings.TrimPrefix(parsed.Path, "/") == "" {
			return errors.New("modeling: artifact storage_ref capability is incomplete")
		}
	case "https":
		if parsed.Host == "" {
			return errors.New("modeling: HTTPS artifact storage_ref requires a host")
		}
	default:
		return fmt.Errorf(
			"modeling: unsupported artifact storage capability scheme %q", parsed.Scheme,
		)
	}
	return nil
}

// ValidateArtifactTransition enforces ordered promotion and terminal archive.
func ValidateArtifactTransition(from, to ArtifactStage) error {
	valid := (from == ArtifactRegistered && to == ArtifactValidated) ||
		(from == ArtifactValidated && to == ArtifactProduction) ||
		((from == ArtifactRegistered || from == ArtifactValidated ||
			from == ArtifactProduction) && to == ArtifactArchived)
	if !valid {
		return fmt.Errorf("modeling: artifact cannot move from %s to %s", from, to)
	}
	return nil
}
