// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
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

// WithNow overrides the clock used to stamp recorded events (deterministic
// tests, the demo seeder) and returns the handler.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

func newID() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("decision-engine: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Subscribe registers an http(s) webhook endpoint after validating the URL and any
// message template. template (optional) formats the body per channel; events
// (optional) route only matching delivery reasons to this webhook.
func (h *Handler) Subscribe(ctx context.Context, id identity.Identity, rawURL, note, template string, events []string) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", eventlog.Envelope{}, fmt.Errorf("notify: webhook url must be http(s), got %q", rawURL)
	}
	// Validate the template up front so a malformed one is rejected at subscribe time,
	// not silently at every delivery.
	if err := validateTemplate(template); err != nil {
		return "", eventlog.Envelope{}, err
	}
	wid := h.newID()
	e, err := h.append(ctx, id, TypeSubscribed, Subscribed{WebhookID: wid, URL: rawURL, Note: note, Template: template, Events: events})
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
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, StreamWebhooks, typ, h.now(), payload)
}
