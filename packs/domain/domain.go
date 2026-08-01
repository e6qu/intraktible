// SPDX-License-Identifier: AGPL-3.0-or-later

// Package domain holds the pure solution-pack model: signed, versioned,
// dependency-pinned pack manifests bundling governed artifacts, and the
// install/upgrade/rollback/retire lifecycle. No I/O.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// PackStatus is the lifecycle state of an installed pack in a workspace.
type PackStatus string

const (
	PackInstalled PackStatus = "installed"
	PackUpgrading PackStatus = "upgrading"
	PackRetired   PackStatus = "retired"
)

// ArtifactKind identifies one bundled governed artifact.
type ArtifactKind string

const (
	ArtifactFlow        ArtifactKind = "flow"
	ArtifactPolicy      ArtifactKind = "policy"
	ArtifactModel       ArtifactKind = "model"
	ArtifactCaseType    ArtifactKind = "case_type"
	ArtifactExperiment  ArtifactKind = "experiment"
	ArtifactReasonCode  ArtifactKind = "reason_code"
	ArtifactRetention   ArtifactKind = "retention"
	ArtifactProviderMap ArtifactKind = "provider_map"
)

// Valid reports whether k is a known artifact kind.
func (k ArtifactKind) Valid() bool {
	switch k {
	case ArtifactFlow, ArtifactPolicy, ArtifactModel, ArtifactCaseType,
		ArtifactExperiment, ArtifactReasonCode, ArtifactRetention, ArtifactProviderMap:
		return true
	}
	return false
}

// Artifact is one bundled governed artifact: its kind, a stable identifier, and
// the content to install (a flow graph, a policy spec, etc.).
type Artifact struct {
	Kind    ArtifactKind   `json:"kind"`
	ID      string         `json:"id"`
	Content map[string]any `json:"content"`
}

// Dependency pins a required provider or another pack version.
type Dependency struct {
	Kind    string `json:"kind"` // "provider" or "pack"
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// Manifest is one immutable, signed, dependency-pinned solution-pack version.
type Manifest struct {
	Name         string       `json:"name"`
	Version      int          `json:"version"`
	Title        string       `json:"title"`
	Description  string       `json:"description"`
	Signature    string       `json:"signature"`
	Artifacts    []Artifact   `json:"artifacts"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
	SampleData   bool         `json:"sample_data"`
	UpgradeFrom  []int        `json:"upgrade_from,omitempty"` // versions this upgrades from
	DefinedBy    string       `json:"defined_by"`
	DefinedAt    time.Time    `json:"defined_at"`
}

// Validate checks the manifest is complete, well-formed, and internally
// consistent. A signature, title, description, and at least one artifact are
// required; every artifact must have a valid kind, id, and content; every
// dependency must name a kind, name, and positive version.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("packs: manifest name is required")
	}
	if !isValidKey(m.Name) {
		return fmt.Errorf("packs: manifest name %q must be lowercase alphanumeric with dashes", m.Name)
	}
	if m.Version < 1 {
		return fmt.Errorf("packs: manifest version %d must be positive", m.Version)
	}
	if strings.TrimSpace(m.Title) == "" {
		return errors.New("packs: manifest title is required")
	}
	if strings.TrimSpace(m.Description) == "" {
		return errors.New("packs: manifest description is required")
	}
	if strings.TrimSpace(m.Signature) == "" {
		return errors.New("packs: manifest signature is required")
	}
	if len(m.Artifacts) == 0 {
		return errors.New("packs: a pack must bundle at least one artifact")
	}
	seen := map[string]bool{}
	for _, artifact := range m.Artifacts {
		if !artifact.Kind.Valid() {
			return fmt.Errorf("packs: unknown artifact kind %q", artifact.Kind)
		}
		if strings.TrimSpace(artifact.ID) == "" {
			return fmt.Errorf("packs: artifact of kind %q has no id", artifact.Kind)
		}
		key := string(artifact.Kind) + "\x00" + artifact.ID
		if seen[key] {
			return fmt.Errorf("packs: duplicate artifact %s %q", artifact.Kind, artifact.ID)
		}
		seen[key] = true
		if len(artifact.Content) == 0 {
			return fmt.Errorf("packs: artifact %s %q has no content", artifact.Kind, artifact.ID)
		}
	}
	for _, dep := range m.Dependencies {
		if dep.Kind != "provider" && dep.Kind != "pack" {
			return fmt.Errorf("packs: unknown dependency kind %q", dep.Kind)
		}
		if strings.TrimSpace(dep.Name) == "" || dep.Version < 1 {
			return fmt.Errorf("packs: dependency %q needs a name and a positive version", dep.Kind)
		}
	}
	return nil
}

// isValidKey mirrors tenancy's slug rule: lowercase alphanumerics and dashes.
func isValidKey(key string) bool {
	if len(key) < 2 || len(key) > 64 {
		return false
	}
	for _, r := range key {
		if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
