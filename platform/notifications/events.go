// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications

// StreamNotifications is the event stream for inbox state changes (read marks).
const StreamNotifications = "platform.notifications"

// TypeMarkedRead records that a recipient marked one of their notifications read.
const TypeMarkedRead = "platform.notification_read"

// MarkedRead marks a single notification read for its recipient.
type MarkedRead struct {
	NotificationID string `json:"notification_id"`
	Recipient      string `json:"recipient"`
}
