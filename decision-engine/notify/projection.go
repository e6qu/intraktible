// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the webhooks read-model collection.
const Collection = "decision_webhooks"

// View is a stored webhook subscription plus its last-delivery state.
type View struct {
	Org            string    `json:"org"`
	Workspace      string    `json:"workspace"`
	WebhookID      string    `json:"webhook_id"`
	URL            string    `json:"url"`
	Note           string    `json:"note,omitempty"`
	Active         bool      `json:"active"`
	DeliveryCount  int       `json:"delivery_count"`
	LastStatus     int       `json:"last_status,omitempty"`
	LastOK         bool      `json:"last_ok"`
	LastError      string    `json:"last_error,omitempty"`
	LastDeliveryAt time.Time `json:"last_delivery_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	CreatedBy      string    `json:"created_by"`
}

// Projector folds the webhook stream into the read model.
type Projector struct{}

func (Projector) Name() string          { return Collection }
func (Projector) Collections() []string { return []string{Collection} }

func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case TypeSubscribed:
		var p Subscribed
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_webhooks: decode subscribed seq %d: %w", e.Seq, err)
		}
		v := View{
			Org: e.Org, Workspace: e.Workspace, WebhookID: p.WebhookID, URL: p.URL,
			Note: p.Note, Active: true, CreatedAt: e.Time, CreatedBy: e.Actor,
		}
		return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.WebhookID), v)
	case TypeUnsubscribed:
		var p Unsubscribed
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_webhooks: decode unsubscribed seq %d: %w", e.Seq, err)
		}
		return s.Delete(ctx, Collection, store.Key(e.Org, e.Workspace, p.WebhookID))
	case TypeDelivered:
		var p Delivered
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("decision_webhooks: decode delivered seq %d: %w", e.Seq, err)
		}
		_, err := store.UpdateDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.WebhookID), func(v *View) {
			v.DeliveryCount++
			v.LastStatus, v.LastOK, v.LastError, v.LastDeliveryAt = p.Status, p.OK, p.Error, p.At
		})
		return err // a delivery record for an unsubscribed webhook is a no-op
	}
	return nil
}

// List returns the tenant's webhooks, newest first.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]View, error) {
	out, err := store.ListDocs[View](ctx, s, Collection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// active returns the tenant's active webhooks (delivery targets).
func active(ctx context.Context, s store.Store, id identity.Identity) ([]View, error) {
	all, err := List(ctx, s, id)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, v := range all {
		if v.Active {
			out = append(out, v)
		}
	}
	return out, nil
}
