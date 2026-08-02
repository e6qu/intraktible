// SPDX-License-Identifier: AGPL-3.0-or-later

package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/e6qu/intraktible/comms/domain"
	"github.com/e6qu/intraktible/comms/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

type Handler struct {
	log eventlog.Log
	now func() time.Time
	mu  sync.Mutex
}

func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

type channelState struct {
	channel domain.Channel
	exists  bool
	paused  bool
	retired bool
}

func (h *Handler) foldChannels(ctx context.Context, name string) (*channelState, error) {
	envelopes, err := h.log.Read(ctx, 0)
	if err != nil {
		return nil, err
	}
	state := &channelState{}
	for _, e := range envelopes {
		if e.Stream != events.StreamComms {
			continue
		}
		switch e.Type {
		case events.TypeChannelCreated:
			var p events.ChannelCreated
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Channel.Name != name {
				continue
			}
			state.channel = p.Channel
			state.exists = true
			state.paused = false
			state.retired = false
		case events.TypeChannelUpdated:
			var p events.ChannelUpdated
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			state.channel.Config = p.Config
		case events.TypeChannelPaused:
			var p events.ChannelPaused
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			state.paused = true
		case events.TypeChannelResumed:
			var p events.ChannelResumed
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			state.paused = false
		case events.TypeChannelRetired:
			var p events.ChannelRetired
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			state.retired = true
		}
	}
	return state, nil
}

func (h *Handler) CreateChannel(
	ctx context.Context,
	id identity.Identity,
	channel domain.Channel,
) (eventlog.Envelope, error) {
	if err := channel.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldChannels(ctx, channel.Name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if state.exists {
		return eventlog.Envelope{}, fmt.Errorf("comms: channel %q already exists", channel.Name)
	}
	channel.Status = domain.ChannelActive
	channel.CreatedBy = id.Actor
	channel.CreatedAt = h.now()
	channel.UpdatedAt = h.now()
	return h.appendUnique(ctx, id, events.TypeChannelCreated, events.ChannelCreated{
		Channel: channel,
	}, "comms.channel.create\x00"+channel.Name)
}

func (h *Handler) UpdateChannel(
	ctx context.Context,
	id identity.Identity,
	name string,
	config map[string]any,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldChannels(ctx, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !state.exists || state.retired {
		return eventlog.Envelope{}, fmt.Errorf("comms: channel %q is not active", name)
	}
	return h.appendUnique(ctx, id, events.TypeChannelUpdated, events.ChannelUpdated{
		Name: name, Config: config, ChangedBy: id.Actor,
	}, "")
}

func (h *Handler) PauseChannel(
	ctx context.Context,
	id identity.Identity,
	name, reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("comms: pause reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldChannels(ctx, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !state.exists || state.retired {
		return eventlog.Envelope{}, fmt.Errorf("comms: channel %q is not active", name)
	}
	if state.paused {
		return eventlog.Envelope{}, fmt.Errorf("comms: channel %q is already paused", name)
	}
	return h.appendUnique(ctx, id, events.TypeChannelPaused, events.ChannelPaused{
		Name: name, Reason: reason, PausedBy: id.Actor,
	}, "")
}

func (h *Handler) ResumeChannel(
	ctx context.Context,
	id identity.Identity,
	name string,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldChannels(ctx, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !state.exists || state.retired {
		return eventlog.Envelope{}, fmt.Errorf("comms: channel %q is not active", name)
	}
	if !state.paused {
		return eventlog.Envelope{}, fmt.Errorf("comms: channel %q is not paused", name)
	}
	return h.appendUnique(ctx, id, events.TypeChannelResumed, events.ChannelResumed{
		Name: name, ResumedBy: id.Actor,
	}, "")
}

func (h *Handler) RetireChannel(
	ctx context.Context,
	id identity.Identity,
	name, reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("comms: retirement reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldChannels(ctx, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !state.exists || state.retired {
		return eventlog.Envelope{}, fmt.Errorf("comms: channel %q is not active", name)
	}
	return h.appendUnique(ctx, id, events.TypeChannelRetired, events.ChannelRetired{
		Name: name, Reason: reason, RetiredBy: id.Actor,
	}, "comms.channel.retire\x00"+name)
}

func (h *Handler) RecordDelivery(
	ctx context.Context,
	id identity.Identity,
	name string,
	evidence domain.DeliveryEvidence,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	state, err := h.foldChannels(ctx, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !state.exists {
		return eventlog.Envelope{}, fmt.Errorf("comms: channel %q does not exist", name)
	}
	if state.paused {
		return eventlog.Envelope{}, fmt.Errorf("comms: channel %q is paused", name)
	}
	evidence.DeliveredAt = h.now()
	evidence.DeliveredBy = id.Actor
	return h.appendUnique(ctx, id, events.TypeDelivered, events.Delivered{
		Name: name, Evidence: evidence, At: h.now(),
	}, "")
}

func (h *Handler) appendUnique(
	ctx context.Context,
	id identity.Identity,
	typ string,
	payload any,
	claim string,
) (eventlog.Envelope, error) {
	return eventlog.AppendClaim(ctx, h.log, id, events.StreamComms, typ, h.now(), payload, claim)
}

func decode(envelope eventlog.Envelope, payload any) error {
	if err := json.Unmarshal(envelope.Payload, payload); err != nil {
		return fmt.Errorf("comms: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return nil
}
