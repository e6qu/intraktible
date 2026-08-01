// SPDX-License-Identifier: AGPL-3.0-or-later

// Package projection contains replayable solution-pack read models.
package projection

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/e6qu/intraktible/packs/domain"
	"github.com/e6qu/intraktible/packs/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
)

// CollectionPacks stores pack manifests and install state.
const CollectionPacks = "packs"

// PackView is one pack's read model: its manifests and current install state.
type PackView struct {
	Org       string                  `json:"org"`
	Workspace string                  `json:"workspace"`
	Name      string                  `json:"name"`
	Manifests map[int]domain.Manifest `json:"manifests"`
	Installed int                     `json:"installed"`
	Retired   bool                    `json:"retired"`
	UpdatedAt string                  `json:"updated_at"`
}

// Projector folds solution-pack lifecycle events into read models.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "packs" }

// Collections lists the store collections this projector owns (reset on rebuild).
func (Projector) Collections() []string { return []string{CollectionPacks} }

// Apply folds one pack event into the read models.
func (Projector) Apply(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	if envelope.Stream != events.StreamPacks {
		return nil
	}
	switch envelope.Type {
	case events.TypePackDefined:
		var p events.Defined
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Manifest.Name, func(v *PackView) {
			if v.Manifests == nil {
				v.Manifests = map[int]domain.Manifest{}
			}
			v.Manifests[p.Manifest.Version] = p.Manifest
		})
	case events.TypePackInstalled:
		var p events.Installed
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, func(v *PackView) {
			v.Installed = p.Version
			v.Retired = false
		})
	case events.TypePackUpgraded:
		var p events.Upgraded
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, func(v *PackView) {
			v.Installed = p.ToVersion
		})
	case events.TypePackRolledBack:
		var p events.RolledBack
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, func(v *PackView) {
			v.Installed = p.ToVersion
		})
	case events.TypePackRetired:
		var p events.Retired
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, func(v *PackView) {
			v.Installed = 0
			v.Retired = true
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
	mutate func(*PackView),
) error {
	key := store.Key(envelope.Org, envelope.Workspace, name)
	view, found, err := store.GetDoc[PackView](ctx, st, CollectionPacks, key)
	if err != nil {
		return err
	}
	if !found {
		view = PackView{Org: envelope.Org, Workspace: envelope.Workspace, Name: name}
	}
	mutate(&view)
	view.UpdatedAt = ts(envelope)
	return store.PutDoc(ctx, st, CollectionPacks, key, view)
}

func ts(envelope eventlog.Envelope) string {
	return envelope.Time.Format("2006-01-02T15:04:05.999999999Z07:00")
}

func decode(envelope eventlog.Envelope, payload any) error {
	if err := json.Unmarshal(envelope.Payload, payload); err != nil {
		return fmt.Errorf("packs projection: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return nil
}
