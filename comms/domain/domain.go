// SPDX-License-Identifier: AGPL-3.0-or-later

// Package domain holds the pure communication-channel model: versioned channel
// configs (webhook, email, SMS), their activation status, and delivery evidence.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ChannelKind identifies the delivery mechanism.
type ChannelKind string

const (
	ChannelWebhook ChannelKind = "webhook"
	ChannelEmail   ChannelKind = "email"
	ChannelSMS     ChannelKind = "sms"
	ChannelInApp   ChannelKind = "in_app"
)

func (k ChannelKind) Valid() bool {
	switch k {
	case ChannelWebhook, ChannelEmail, ChannelSMS, ChannelInApp:
		return true
	}
	return false
}

// ChannelStatus is the lifecycle state of a communication channel.
type ChannelStatus string

const (
	ChannelActive  ChannelStatus = "active"
	ChannelPaused  ChannelStatus = "paused"
	ChannelRetired ChannelStatus = "retired"
)

// Channel is one governed communication channel: a versioned config, an owner,
// and the delivery boundaries (which reasons it delivers, whether it is
// admin-only).
type Channel struct {
	Name      string         `json:"name"`
	Kind      ChannelKind    `json:"kind"`
	Config    map[string]any `json:"config"`
	Owner     string         `json:"owner"`
	Status    ChannelStatus  `json:"status"`
	CreatedBy string         `json:"created_by"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Validate checks the channel definition.
func (c Channel) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("comms: channel name is required")
	}
	if !isValidKey(c.Name) {
		return fmt.Errorf("comms: channel name %q must be lowercase alphanumeric with dashes", c.Name)
	}
	if !c.Kind.Valid() {
		return fmt.Errorf("comms: unknown channel kind %q", c.Kind)
	}
	switch c.Kind {
	case ChannelWebhook:
		if _, ok := c.Config["url"]; !ok {
			return errors.New("comms: webhook channel requires a url")
		}
	case ChannelEmail:
		if _, ok := c.Config["to"]; !ok {
			return errors.New("comms: email channel requires a to address")
		}
	case ChannelSMS:
		if _, ok := c.Config["to"]; !ok {
			return errors.New("comms: sms channel requires a to number")
		}
	}
	return nil
}

// DeliveryOutcome classifies one delivery attempt.
type DeliveryOutcome string

const (
	Delivered         DeliveryOutcome = "delivered"
	DeliveryRetryable DeliveryOutcome = "retryable"
	DeliveryFailed    DeliveryOutcome = "failed"
)

// DeliveryEvidence records one delivery attempt to a channel.
type DeliveryEvidence struct {
	Channel     string          `json:"channel"`
	Outcome     DeliveryOutcome `json:"outcome"`
	Details     string          `json:"details"`
	Attempt     int             `json:"attempt"`
	DeliveredAt time.Time       `json:"delivered_at"`
	DeliveredBy string          `json:"delivered_by"`
}

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
