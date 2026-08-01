// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events defines immutable solution-pack lifecycle event payloads.
package events

import (
	"github.com/e6qu/intraktible/packs/domain"
)

// StreamPacks is the solution-pack lifecycle stream.
const StreamPacks = "packs"

const (
	TypePackDefined    = "packs.defined"
	TypePackInstalled  = "packs.installed"
	TypePackUpgraded   = "packs.upgraded"
	TypePackRolledBack = "packs.rolled_back"
	TypePackRetired    = "packs.retired"
)

// Defined records a new immutable pack manifest version.
type Defined struct {
	Manifest domain.Manifest `json:"manifest"`
}

// Installed records a pack version installed into the workspace.
type Installed struct {
	Name        string `json:"name"`
	Version     int    `json:"version"`
	InstalledBy string `json:"installed_by"`
}

// Upgraded records an installed pack upgraded to a newer version.
type Upgraded struct {
	Name        string `json:"name"`
	FromVersion int    `json:"from_version"`
	ToVersion   int    `json:"to_version"`
	UpgradedBy  string `json:"upgraded_by"`
}

// RolledBack records an installed pack rolled back to a prior version.
type RolledBack struct {
	Name        string `json:"name"`
	FromVersion int    `json:"from_version"`
	ToVersion   int    `json:"to_version"`
	RolledBy    string `json:"rolled_by"`
}

// Retired records a pack removed from the workspace.
type Retired struct {
	Name      string `json:"name"`
	Version   int    `json:"version"`
	Reason    string `json:"reason"`
	RetiredBy string `json:"retired_by"`
}
