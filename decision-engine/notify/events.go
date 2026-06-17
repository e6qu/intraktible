// SPDX-License-Identifier: AGPL-3.0-or-later

// Package notify is the Decision Engine's outbound notification channel: webhook
// subscriptions and SSRF-safe delivery. A monitor "check" evaluates a flow's
// thresholds and pushes the firing ones to every active webhook; each delivery is
// recorded as an event (so it shows in the audit log and updates the webhook's
// last-delivery state). Delivery is an effect performed in the imperative shell.
package notify

import "time"

// StreamWebhooks is the event stream for webhook subscriptions + deliveries.
const StreamWebhooks = "decision.webhooks"

// Event type identifiers.
const (
	TypeSubscribed   = "decision.webhook_subscribed"
	TypeUnsubscribed = "decision.webhook_unsubscribed"
	TypeDelivered    = "decision.webhook_delivered"
)

// Subscribed registers a webhook endpoint.
type Subscribed struct {
	WebhookID string `json:"webhook_id"`
	URL       string `json:"url"`
	Note      string `json:"note,omitempty"`
}

// Unsubscribed removes a webhook endpoint.
type Unsubscribed struct {
	WebhookID string `json:"webhook_id"`
}

// Delivered records the outcome of pushing a payload to a webhook.
type Delivered struct {
	WebhookID string    `json:"webhook_id"`
	URL       string    `json:"url"`
	OK        bool      `json:"ok"`
	Status    int       `json:"status,omitempty"`
	Error     string    `json:"error,omitempty"`
	Reason    string    `json:"reason,omitempty"` // what prompted the delivery (e.g. "monitor check")
	At        time.Time `json:"at"`
}
