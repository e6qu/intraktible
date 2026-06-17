// SPDX-License-Identifier: AGPL-3.0-or-later

package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler is the inbox write side (imperative shell). The only mutation is
// marking a notification read; notifications themselves are derived from comments.
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds a Handler using the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// MarkRead marks one of the caller's notifications read. The recipient is the
// caller, so a user can only touch their own inbox. notificationID is recipient-
// scoped (recipient:source), so it must belong to the caller.
func (h *Handler) MarkRead(ctx context.Context, id identity.Identity, notificationID string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if notificationID == "" {
		return eventlog.Envelope{}, fmt.Errorf("notifications: notification_id is required")
	}
	if !strings.HasPrefix(notificationID, id.Actor+":") {
		return eventlog.Envelope{}, fmt.Errorf("notifications: notification does not belong to the caller")
	}
	b, err := json.Marshal(MarkedRead{NotificationID: notificationID, Recipient: id.Actor})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("notifications: marshal read: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamNotifications, Type: TypeMarkedRead, Time: h.now(), Payload: b,
	})
}
