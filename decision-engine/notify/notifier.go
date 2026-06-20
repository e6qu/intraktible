// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Notifier delivers payloads to a tenant's active webhooks over an injected
// http.Client — in production the SSRF-guarded egress client, in tests a plain
// one. Each delivery is recorded as a Delivered event (audit + last-delivery state).
type Notifier struct {
	log    eventlog.Log
	store  store.Store
	client *http.Client
	now    func() time.Time
}

// NewNotifier builds a Notifier. The client carries the egress policy and timeout.
func NewNotifier(log eventlog.Log, st store.Store, client *http.Client) *Notifier {
	return &Notifier{log: log, store: st, client: client, now: func() time.Time { return time.Now().UTC() }}
}

// DeliveryResult is one webhook's delivery outcome.
type DeliveryResult struct {
	WebhookID string `json:"webhook_id"`
	URL       string `json:"url"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Deliver POSTs payload as JSON to every active webhook, recording each outcome.
// reason labels what prompted the push (e.g. "monitor check"). Each attempt is
// recorded (with its error) for the audit trail; a failure to record the effect —
// which would break replay — aborts the call.
//
// It returns an error when there are active webhooks but NONE accepted the payload
// (every one failed/5xx). The schedulers rely on this: a total delivery failure
// must not let them record the firing-edge alert (which would dedup the alert into
// silence) — returning an error keeps the transition unrecorded so the next tick
// re-delivers. A partial success (≥1 webhook accepted) counts as delivered; zero
// configured webhooks is a vacuous success.
func (n *Notifier) Deliver(ctx context.Context, id identity.Identity, reason string, payload any) ([]DeliveryResult, error) {
	hooks, err := active(ctx, n.store, id)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("notify: marshal payload: %w", err)
	}
	results := make([]DeliveryResult, 0, len(hooks))
	delivered := 0
	for _, h := range hooks {
		res := n.post(ctx, h, body)
		if err := n.record(ctx, id, h, res, reason); err != nil {
			return nil, err
		}
		if res.OK {
			delivered++
		}
		results = append(results, res)
	}
	if len(hooks) > 0 && delivered == 0 {
		return results, fmt.Errorf("notify: delivery failed to all %d active webhook(s)", len(hooks))
	}
	return results, nil
}

func (n *Notifier) post(ctx context.Context, h View, body []byte) DeliveryResult {
	res := DeliveryResult{WebhookID: h.WebhookID, URL: h.URL}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body)) // #nosec G107
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	res.Status = resp.StatusCode
	res.OK = resp.StatusCode >= 200 && resp.StatusCode < 300
	if !res.OK {
		res.Error = fmt.Sprintf("non-2xx response: %d", resp.StatusCode)
	}
	return res
}

func (n *Notifier) record(ctx context.Context, id identity.Identity, h View, res DeliveryResult, reason string) error {
	payload, err := json.Marshal(Delivered{
		WebhookID: h.WebhookID, URL: h.URL, OK: res.OK, Status: res.Status,
		Error: res.Error, Reason: reason, At: n.now(),
	})
	if err != nil {
		return fmt.Errorf("notify: marshal delivered: %w", err)
	}
	_, err = n.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamWebhooks, Type: TypeDelivered, Time: n.now(), Payload: payload,
	})
	return err
}
