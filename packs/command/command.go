// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command implements solution-pack lifecycle validation and event emission.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/e6qu/intraktible/packs/domain"
	"github.com/e6qu/intraktible/packs/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler validates and emits solution-pack lifecycle events.
type Handler struct {
	log eventlog.Log
	now func() time.Time
	mu  sync.Mutex
}

// NewHandler constructs a Handler with the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the command clock for deterministic tests and seed builds.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// packState folds one pack's lifecycle in the workspace.
type packState struct {
	manifests  map[int]domain.Manifest
	installed  int // the currently installed version (0 = none)
	retired    bool
	upgradeLog []int
}

// foldPacks folds the packs stream into all pack states keyed by pack name.
func (h *Handler) foldPacks(ctx context.Context, name string) (*packState, error) {
	envelopes, err := h.log.Read(ctx, 0)
	if err != nil {
		return nil, err
	}
	state := &packState{manifests: map[int]domain.Manifest{}}
	for _, e := range envelopes {
		if e.Stream != events.StreamPacks {
			continue
		}
		switch e.Type {
		case events.TypePackDefined:
			var p events.Defined
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Manifest.Name != name {
				continue
			}
			state.manifests[p.Manifest.Version] = p.Manifest
		case events.TypePackInstalled:
			var p events.Installed
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			state.installed = p.Version
			state.retired = false
		case events.TypePackUpgraded:
			var p events.Upgraded
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			state.installed = p.ToVersion
			state.upgradeLog = append(state.upgradeLog, p.ToVersion)
		case events.TypePackRolledBack:
			var p events.RolledBack
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			state.installed = p.ToVersion
		case events.TypePackRetired:
			var p events.Retired
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			state.installed = 0
			state.retired = true
		}
	}
	return state, nil
}

// Define registers a new immutable pack manifest version. The version is
// computed (max existing + 1). The manifest's signature, title, description,
// and artifacts are validated at define time.
func (h *Handler) Define(
	ctx context.Context,
	id identity.Identity,
	name string,
	manifest domain.Manifest,
) (int, eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldPacks(ctx, name)
	if err != nil {
		return 0, eventlog.Envelope{}, err
	}
	next := 1
	for v := range state.manifests {
		if v >= next {
			next = v + 1
		}
	}
	manifest.Version = next
	manifest.Name = name
	if err := manifest.Validate(); err != nil {
		return 0, eventlog.Envelope{}, err
	}
	manifest.DefinedBy = id.Actor
	manifest.DefinedAt = h.now()
	env, err := h.appendUnique(ctx, id, events.TypePackDefined, events.Defined{
		Manifest: manifest,
	}, "packs.define\x00"+name+"\x00"+fmt.Sprint(next))
	if err != nil {
		return 0, eventlog.Envelope{}, err
	}
	return next, env, nil
}

// Install installs a defined pack version into the workspace. Installing a
// pack requires the manifest to exist and the pack to not already be installed
// at that version. Dependencies are checked at install time.
func (h *Handler) Install(
	ctx context.Context,
	id identity.Identity,
	name string,
	version int,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldPacks(ctx, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	manifest, ok := state.manifests[version]
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("packs: unknown pack %q version %d", name, version)
	}
	if state.installed == version && !state.retired {
		return eventlog.Envelope{}, fmt.Errorf("packs: pack %q version %d is already installed", name, version)
	}
	if state.installed != 0 && state.installed != version {
		return eventlog.Envelope{}, fmt.Errorf(
			"packs: pack %q is installed at version %d; upgrade or roll back instead", name, state.installed,
		)
	}
	if err := h.checkDependencies(ctx, id, manifest.Dependencies); err != nil {
		return eventlog.Envelope{}, err
	}
	return h.appendUnique(ctx, id, events.TypePackInstalled, events.Installed{
		Name: name, Version: version, InstalledBy: id.Actor,
	}, "packs.install\x00"+name+"\x00"+fmt.Sprint(version))
}

// Upgrade moves an installed pack to a newer version. The target version must
// exist and the pack must declare it can upgrade from the installed version.
func (h *Handler) Upgrade(
	ctx context.Context,
	id identity.Identity,
	name string,
	toVersion int,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldPacks(ctx, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.installed == 0 {
		return eventlog.Envelope{}, fmt.Errorf("packs: pack %q is not installed", name)
	}
	if state.installed >= toVersion {
		return eventlog.Envelope{}, fmt.Errorf(
			"packs: pack %q is at version %d; upgrade target %d must be newer", name, state.installed, toVersion,
		)
	}
	manifest, ok := state.manifests[toVersion]
	if !ok {
		return eventlog.Envelope{}, fmt.Errorf("packs: unknown pack %q version %d", name, toVersion)
	}
	// The target must declare it upgrades from the installed version.
	allowed := false
	for _, v := range manifest.UpgradeFrom {
		if v == state.installed {
			allowed = true
			break
		}
	}
	if len(manifest.UpgradeFrom) > 0 && !allowed {
		return eventlog.Envelope{}, fmt.Errorf(
			"packs: pack %q version %d does not support upgrading from version %d",
			name, toVersion, state.installed,
		)
	}
	return h.appendUnique(ctx, id, events.TypePackUpgraded, events.Upgraded{
		Name: name, FromVersion: state.installed, ToVersion: toVersion, UpgradedBy: id.Actor,
	}, "packs.upgrade\x00"+name+"\x00"+fmt.Sprint(state.installed)+"\x00"+fmt.Sprint(toVersion))
}

// Rollback reverts an installed pack to a prior version.
func (h *Handler) Rollback(
	ctx context.Context,
	id identity.Identity,
	name string,
	toVersion int,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldPacks(ctx, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.installed == 0 {
		return eventlog.Envelope{}, fmt.Errorf("packs: pack %q is not installed", name)
	}
	if _, ok := state.manifests[toVersion]; !ok {
		return eventlog.Envelope{}, fmt.Errorf("packs: unknown pack %q version %d", name, toVersion)
	}
	if toVersion >= state.installed {
		return eventlog.Envelope{}, fmt.Errorf(
			"packs: rollback target %d must be older than installed version %d", toVersion, state.installed,
		)
	}
	return h.appendUnique(ctx, id, events.TypePackRolledBack, events.RolledBack{
		Name: name, FromVersion: state.installed, ToVersion: toVersion, RolledBy: id.Actor,
	}, "")
}

// Retire removes an installed pack from the workspace.
func (h *Handler) Retire(
	ctx context.Context,
	id identity.Identity,
	name string,
	reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("packs: retirement reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldPacks(ctx, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.installed == 0 {
		return eventlog.Envelope{}, fmt.Errorf("packs: pack %q is not installed", name)
	}
	return h.appendUnique(ctx, id, events.TypePackRetired, events.Retired{
		Name: name, Version: state.installed, Reason: reason, RetiredBy: id.Actor,
	}, "packs.retire\x00"+name+"\x00"+fmt.Sprint(state.installed))
}

// checkDependencies verifies every pack dependency is satisfiable. A "pack"
// dependency requires the named pack to be installed at the pinned version; a
// "provider" dependency requires the provider to be deployed in production.
func (h *Handler) checkDependencies(
	ctx context.Context,
	id identity.Identity,
	dependencies []domain.Dependency,
) error {
	// Read the provider lifecycle stream for provider dependencies. Pack
	// dependencies check the packs stream (folded above per name).
	for _, dep := range dependencies {
		switch dep.Kind {
		case "pack":
			other, err := h.foldPacks(ctx, dep.Name)
			if err != nil {
				return err
			}
			if other.installed != dep.Version {
				return fmt.Errorf(
					"packs: dependency pack %q must be installed at version %d (currently %d)",
					dep.Name, dep.Version, other.installed,
				)
			}
		case "provider":
			// Providers deploy into production through the providers lifecycle; a
			// dependency is satisfied when the pinned version is deployed there.
			deployed, err := h.providerDeployed(ctx, dep.Name, dep.Version)
			if err != nil {
				return err
			}
			if !deployed {
				return fmt.Errorf(
					"packs: dependency provider %q version %d is not deployed in production",
					dep.Name, dep.Version,
				)
			}
		}
	}
	return nil
}

// providerDeployed reports whether a provider version is deployed in production.
func (h *Handler) providerDeployed(ctx context.Context, name string, version int) (bool, error) {
	envelopes, err := h.log.Read(ctx, 0)
	if err != nil {
		return false, err
	}
	const streamProviders = "providers"
	const typeDeployed = "providers.deployed"
	const typeRetired = "providers.retired"
	const typeUpgraded = "providers.upgraded"
	type deployedPayload struct {
		Name        string `json:"name"`
		Version     int    `json:"version"`
		Environment string `json:"environment"`
	}
	type retiredPayload struct {
		Name        string `json:"name"`
		Version     int    `json:"version"`
		Environment string `json:"environment"`
	}
	type upgradedPayload struct {
		Name        string `json:"name"`
		FromVersion int    `json:"from_version"`
		ToVersion   int    `json:"to_version"`
		Environment string `json:"environment"`
	}
	deployed := false
	for _, e := range envelopes {
		if e.Stream != streamProviders {
			continue
		}
		switch e.Type {
		case typeDeployed:
			var p deployedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return false, err
			}
			if p.Name == name && p.Version == version && p.Environment == "production" {
				deployed = true
			}
		case typeRetired:
			var p retiredPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return false, err
			}
			if p.Name == name && p.Version == version && p.Environment == "production" {
				deployed = false
			}
		case typeUpgraded:
			var p upgradedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return false, err
			}
			if p.Name == name && p.Environment == "production" {
				if p.FromVersion == version {
					deployed = false
				}
				if p.ToVersion == version {
					deployed = true
				}
			}
		}
	}
	return deployed, nil
}

func (h *Handler) appendUnique(
	ctx context.Context,
	id identity.Identity,
	typ string,
	payload any,
	claim string,
) (eventlog.Envelope, error) {
	return eventlog.AppendClaim(ctx, h.log, id, events.StreamPacks, typ, h.now(), payload, claim)
}

func decode(envelope eventlog.Envelope, payload any) error {
	if err := json.Unmarshal(envelope.Payload, payload); err != nil {
		return fmt.Errorf("packs: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return nil
}
