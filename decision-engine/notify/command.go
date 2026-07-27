// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler is the webhook write side (imperative shell).
type Handler struct {
	log   eventlog.Log
	now   func() time.Time
	newID func() string
	mu    sync.Mutex
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
	h.mu.Lock()
	defer h.mu.Unlock()
	active, err := h.isActive(ctx, id, webhookID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !active {
		return eventlog.Envelope{}, fmt.Errorf("notify: active webhook %q not found", webhookID)
	}
	raw, err := json.Marshal(Unsubscribed{WebhookID: webhookID})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("notify: marshal unsubscribe: %w", err)
	}
	e, err := h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamWebhooks, Type: TypeUnsubscribed, Time: h.now(), Payload: raw,
		Unique: "webhook.unsubscribe\x00" + id.Org + "\x00" + id.Workspace + "\x00" + webhookID,
	})
	if errors.Is(err, eventlog.ErrConflict) {
		return eventlog.Envelope{}, fmt.Errorf("notify: active webhook %q not found", webhookID)
	}
	return e, err
}

func (h *Handler) isActive(
	ctx context.Context,
	id identity.Identity,
	webhookID string,
) (bool, error) {
	evs, err := h.log.ReadTenantStream(ctx, id.Org, id.Workspace, StreamWebhooks, 0)
	if err != nil {
		return false, fmt.Errorf("notify: read webhook lifecycle: %w", err)
	}
	active := false
	found := false
	for _, e := range evs {
		switch e.Type {
		case TypeSubscribed:
			var p Subscribed
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return false, fmt.Errorf("notify: decode subscribed seq %d: %w", e.Seq, err)
			}
			if p.WebhookID == webhookID {
				found, active = true, true
			}
		case TypeUnsubscribed:
			var p Unsubscribed
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return false, fmt.Errorf("notify: decode unsubscribed seq %d: %w", e.Seq, err)
			}
			if p.WebhookID == webhookID {
				found, active = true, false
			}
		case TypeDelivered:
			var p Delivered
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return false, fmt.Errorf("notify: decode delivered seq %d: %w", e.Seq, err)
			}
		}
	}
	return found && active, nil
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload any) (eventlog.Envelope, error) {
	return eventlog.AppendJSON(ctx, h.log, id.Org, id.Workspace, id.Actor, StreamWebhooks, typ, h.now(), payload)
}
