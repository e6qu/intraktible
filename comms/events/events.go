// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import (
	"github.com/e6qu/intraktible/comms/domain"
	"time"
)

const StreamComms = "comms"

const (
	TypeChannelCreated = "comms.channel.created"
	TypeChannelUpdated = "comms.channel.updated"
	TypeChannelPaused  = "comms.channel.paused"
	TypeChannelResumed = "comms.channel.resumed"
	TypeChannelRetired = "comms.channel.retired"
	TypeDelivered      = "comms.delivered"
)

type ChannelCreated struct {
	Channel domain.Channel `json:"channel"`
}

type ChannelUpdated struct {
	Name      string         `json:"name"`
	Config    map[string]any `json:"config"`
	ChangedBy string         `json:"changed_by"`
}

type ChannelPaused struct {
	Name     string `json:"name"`
	Reason   string `json:"reason"`
	PausedBy string `json:"paused_by"`
}

type ChannelResumed struct {
	Name      string `json:"name"`
	ResumedBy string `json:"resumed_by"`
}

type ChannelRetired struct {
	Name      string `json:"name"`
	Reason    string `json:"reason"`
	RetiredBy string `json:"retired_by"`
}

type Delivered struct {
	Name     string                  `json:"name"`
	Evidence domain.DeliveryEvidence `json:"evidence"`
	At       time.Time               `json:"at"`
}
