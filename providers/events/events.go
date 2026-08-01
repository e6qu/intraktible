// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events defines immutable provider lifecycle event payloads.
package events

import (
	"github.com/e6qu/intraktible/providers/domain"
)

// StreamProviders is the provider lifecycle stream.
const StreamProviders = "providers"

const (
	TypeProviderInstalled  = "providers.installed"
	TypeProviderConfigured = "providers.configured"
	TypeProviderTested     = "providers.tested"
	TypeProviderApproved   = "providers.approved"
	TypeProviderDeployed   = "providers.deployed"
	TypeProviderPaused     = "providers.paused"
	TypeProviderResumed    = "providers.resumed"
	TypeProviderUpgraded   = "providers.upgraded"
	TypeProviderRetired    = "providers.retired"
)

// Installed records a new immutable provider manifest version.
type Installed struct {
	Manifest domain.Manifest `json:"manifest"`
}

// Configured records a per-environment configuration for a provider version.
type Configured struct {
	Name          string               `json:"name"`
	Version       int                  `json:"version"`
	Configuration domain.Configuration `json:"configuration"`
}

// Tested records a conformance/validation run against a provider version.
type Tested struct {
	Name     string              `json:"name"`
	Version  int                 `json:"version"`
	Evidence domain.TestEvidence `json:"evidence"`
}

// Approved records the four-eyes approval of a provider version for an environment.
type Approved struct {
	Name     string          `json:"name"`
	Version  int             `json:"version"`
	Approval domain.Approval `json:"approval"`
}

// Deployed records a provider version going live in an environment.
type Deployed struct {
	Name        string             `json:"name"`
	Version     int                `json:"version"`
	Environment domain.Environment `json:"environment"`
	DeployedBy  string             `json:"deployed_by"`
}

// Paused records a provider version paused in an environment.
type Paused struct {
	Name        string             `json:"name"`
	Version     int                `json:"version"`
	Environment domain.Environment `json:"environment"`
	Reason      string             `json:"reason"`
	PausedBy    string             `json:"paused_by"`
}

// Resumed records a provider version resumed from paused.
type Resumed struct {
	Name        string             `json:"name"`
	Version     int                `json:"version"`
	Environment domain.Environment `json:"environment"`
	ResumedBy   string             `json:"resumed_by"`
}

// Upgraded records a provider's environment moving to a newer approved version.
type Upgraded struct {
	Name        string             `json:"name"`
	FromVersion int                `json:"from_version"`
	ToVersion   int                `json:"to_version"`
	Environment domain.Environment `json:"environment"`
	UpgradedBy  string             `json:"upgraded_by"`
}

// Retired records a provider version removed from service in an environment.
type Retired struct {
	Name        string             `json:"name"`
	Version     int                `json:"version"`
	Environment domain.Environment `json:"environment"`
	Reason      string             `json:"reason"`
	RetiredBy   string             `json:"retired_by"`
}
