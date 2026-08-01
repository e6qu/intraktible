// SPDX-License-Identifier: AGPL-3.0-or-later

// Package projection contains replayable provider lifecycle read models.
package projection

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/providers/domain"
	"github.com/e6qu/intraktible/providers/events"
)

const (
	// CollectionProviders stores provider manifest versions and lifecycle state.
	CollectionProviders = "providers"
	// CollectionHealth stores per-provider-environment operational health.
	CollectionHealth = "providers_health"
)

// ProviderView is one provider version's read model.
type ProviderView struct {
	Org         string            `json:"org"`
	Workspace   string            `json:"workspace"`
	Name        string            `json:"name"`
	Version     int               `json:"version"`
	Manifest    domain.Manifest   `json:"manifest"`
	Configured  map[string]any    `json:"configured,omitempty"`
	Tested      bool              `json:"tested"`
	Approved    bool              `json:"approved"`
	Deployments map[string]string `json:"deployments,omitempty"` // env -> stage
	UpdatedAt   string            `json:"updated_at"`
}

// HealthView is one provider-environment's operational health.
type HealthView struct {
	Org         string `json:"org"`
	Workspace   string `json:"workspace"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Fetches     int64  `json:"fetches"`
	Errors      int64  `json:"errors"`
	LastSuccess string `json:"last_success,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

// Projector folds provider lifecycle events into read models.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "providers" }

// Collections lists the store collections this projector owns (reset on rebuild).
func (Projector) Collections() []string {
	return []string{CollectionProviders, CollectionHealth}
}

// Apply folds one provider event into the read models.
func (Projector) Apply(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	if envelope.Stream != events.StreamProviders {
		return nil
	}
	switch envelope.Type {
	case events.TypeProviderInstalled:
		var p events.Installed
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionProviders, providerKey(envelope, p.Manifest.Name, p.Manifest.Version),
			ProviderView{
				Org: envelope.Org, Workspace: envelope.Workspace,
				Name: p.Manifest.Name, Version: p.Manifest.Version, Manifest: p.Manifest,
				Deployments: map[string]string{}, UpdatedAt: ts(envelope),
			})
	case events.TypeProviderConfigured:
		var p events.Configured
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, p.Version, func(v *ProviderView) {
			if v.Configured == nil {
				v.Configured = map[string]any{}
			}
			v.Configured[string(p.Configuration.Environment)] = p.Configuration.Config
		})
	case events.TypeProviderTested:
		var p events.Tested
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, p.Version, func(v *ProviderView) {
			if p.Evidence.Passed {
				v.Tested = true
			}
		})
	case events.TypeProviderApproved:
		var p events.Approved
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, p.Version, func(v *ProviderView) {
			v.Approved = true
		})
	case events.TypeProviderDeployed:
		var p events.Deployed
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, p.Version, func(v *ProviderView) {
			if v.Deployments == nil {
				v.Deployments = map[string]string{}
			}
			v.Deployments[string(p.Environment)] = string(domain.StageDeployed)
		})
	case events.TypeProviderPaused:
		var p events.Paused
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, p.Version, func(v *ProviderView) {
			if v.Deployments == nil {
				v.Deployments = map[string]string{}
			}
			v.Deployments[string(p.Environment)] = string(domain.StagePaused)
		})
	case events.TypeProviderResumed:
		var p events.Resumed
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, p.Version, func(v *ProviderView) {
			v.Deployments[string(p.Environment)] = string(domain.StageDeployed)
		})
	case events.TypeProviderUpgraded:
		var p events.Upgraded
		if err := decode(envelope, &p); err != nil {
			return err
		}
		if err := mutate(ctx, st, envelope, p.Name, p.FromVersion, func(v *ProviderView) {
			delete(v.Deployments, string(p.Environment))
		}); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, p.ToVersion, func(v *ProviderView) {
			v.Deployments[string(p.Environment)] = string(domain.StageDeployed)
		})
	case events.TypeProviderRetired:
		var p events.Retired
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, p.Version, func(v *ProviderView) {
			if v.Deployments == nil {
				v.Deployments = map[string]string{}
			}
			v.Deployments[string(p.Environment)] = string(domain.StageRetired)
		})
	default:
		return nil
	}
}

func mutate(
	ctx context.Context,
	st store.Store,
	envelope eventlog.Envelope,
	name string,
	version int,
	mutate func(*ProviderView),
) error {
	k := providerKey(envelope, name, version)
	view, found, err := store.GetDoc[ProviderView](ctx, st, CollectionProviders, k)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"providers projection: provider %q version %d is missing at seq %d", name, version, envelope.Seq,
		)
	}
	mutate(&view)
	view.UpdatedAt = ts(envelope)
	return store.PutDoc(ctx, st, CollectionProviders, k, view)
}

func providerKey(envelope eventlog.Envelope, name string, version int) string {
	return store.Key(envelope.Org, envelope.Workspace, name+"/"+fmt.Sprint(version))
}

func ts(envelope eventlog.Envelope) string {
	return envelope.Time.Format("2006-01-02T15:04:05.999999999Z07:00")
}

func decode(envelope eventlog.Envelope, payload any) error {
	if err := json.Unmarshal(envelope.Payload, payload); err != nil {
		return fmt.Errorf("providers projection: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return nil
}
