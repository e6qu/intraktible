// SPDX-License-Identifier: AGPL-3.0-or-later

package projection

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/e6qu/intraktible/comms/domain"
	"github.com/e6qu/intraktible/comms/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/store"
)

const CollectionChannels = "comms_channels"

type ChannelView struct {
	Org       string               `json:"org"`
	Workspace string               `json:"workspace"`
	Name      string               `json:"name"`
	Kind      domain.ChannelKind   `json:"kind"`
	Config    map[string]any       `json:"config"`
	Owner     string               `json:"owner"`
	Status    domain.ChannelStatus `json:"status"`
	UpdatedAt string               `json:"updated_at"`
}

type Projector struct{}

func (Projector) Name() string { return "comms" }

func (Projector) Collections() []string { return []string{CollectionChannels} }

func (Projector) Apply(ctx context.Context, envelope eventlog.Envelope, st store.Store) error {
	if envelope.Stream != events.StreamComms {
		return nil
	}
	switch envelope.Type {
	case events.TypeChannelCreated:
		var p events.ChannelCreated
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return store.PutDoc(ctx, st, CollectionChannels,
			store.Key(envelope.Org, envelope.Workspace, p.Channel.Name),
			ChannelView{
				Org: envelope.Org, Workspace: envelope.Workspace,
				Name: p.Channel.Name, Kind: p.Channel.Kind, Config: p.Channel.Config,
				Owner: p.Channel.Owner, Status: p.Channel.Status,
				UpdatedAt: ts(envelope),
			})
	case events.TypeChannelUpdated:
		var p events.ChannelUpdated
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, func(v *ChannelView) {
			v.Config = p.Config
		})
	case events.TypeChannelPaused:
		var p events.ChannelPaused
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, func(v *ChannelView) {
			v.Status = domain.ChannelPaused
		})
	case events.TypeChannelResumed:
		var p events.ChannelResumed
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, func(v *ChannelView) {
			v.Status = domain.ChannelActive
		})
	case events.TypeChannelRetired:
		var p events.ChannelRetired
		if err := decode(envelope, &p); err != nil {
			return err
		}
		return mutate(ctx, st, envelope, p.Name, func(v *ChannelView) {
			v.Status = domain.ChannelRetired
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
	mutate func(*ChannelView),
) error {
	key := store.Key(envelope.Org, envelope.Workspace, name)
	view, found, err := store.GetDoc[ChannelView](ctx, st, CollectionChannels, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("comms projection: channel %q is missing at seq %d", name, envelope.Seq)
	}
	mutate(&view)
	view.UpdatedAt = ts(envelope)
	return store.PutDoc(ctx, st, CollectionChannels, key, view)
}

func ts(envelope eventlog.Envelope) string {
	return envelope.Time.Format("2006-01-02T15:04:05.999999999Z07:00")
}

func decode(envelope eventlog.Envelope, payload any) error {
	if err := json.Unmarshal(envelope.Payload, payload); err != nil {
		return fmt.Errorf("comms projection: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return nil
}
