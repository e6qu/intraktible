// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler is the webhook write side (imperative shell).
type Handler struct {
	log   eventlog.Log
	now   func() time.Time
	newID func() string
}

// NewHandler builds a Handler using the system clock and a random id source.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }, newID: newID}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Subscribe registers an http(s) webhook endpoint after validating the URL.
func (h *Handler) Subscribe(ctx context.Context, id identity.Identity, rawURL, note string) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("notify: webhook url must be http(s), got %q", rawURL)
	}
	wid := h.newID()
	e, err := h.append(ctx, id, TypeSubscribed, Subscribed{WebhookID: wid, URL: rawURL, Note: note})
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return wid, e, nil
}

// Unsubscribe removes a webhook endpoint.
func (h *Handler) Unsubscribe(ctx context.Context, id identity.Identity, webhookID string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if webhookID == "" {
		return eventlog.Envelope{}, fmt.Errorf("notify: webhook_id is required")
	}
	return h.append(ctx, id, TypeUnsubscribed, Unsubscribed{WebhookID: webhookID})
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("notify: marshal %s: %w", typ, err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamWebhooks, Type: typ, Time: h.now(), Payload: b,
	})
}
