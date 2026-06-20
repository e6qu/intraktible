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

// Outcome classifies a single webhook delivery attempt. The distinction drives the
// schedulers' dedup decision: a Retryable failure must NOT record the firing-edge
// alert (so the next tick re-tries), whereas a Permanent failure is recorded like a
// success so a dead endpoint (a 4xx that will never accept) cannot make the monitor
// re-deliver forever and never dedup.
type Outcome string

const (
	OutcomeAccepted  Outcome = "accepted"  // 2xx — delivered
	OutcomeRetryable Outcome = "retryable" // transport error, 408, 429, or 5xx — try again
	OutcomePermanent Outcome = "permanent" // other 4xx — the endpoint will never accept this
)

// classifyStatus maps an HTTP status to a delivery Outcome. Transport-level errors
// (no status) are classified by the caller as Retryable.
func classifyStatus(status int) Outcome {
	switch {
	case status >= 200 && status < 300:
		return OutcomeAccepted
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
		return OutcomeRetryable
	default:
		return OutcomePermanent // 4xx other than 408/429 — a client error that will recur
	}
}

// DeliveryResult is one webhook's delivery outcome. OK is retained (== Accepted) for
// the existing check-endpoint JSON and is set in lockstep with Outcome.
type DeliveryResult struct {
	WebhookID string  `json:"webhook_id"`
	URL       string  `json:"url"`
	Outcome   Outcome `json:"outcome"`
	OK        bool    `json:"ok"`
	Status    int     `json:"status,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// Deliver POSTs payload as JSON to every active webhook, recording each outcome.
// reason labels what prompted the push (e.g. "monitor check"). Each attempt is
// recorded (with its error) for the audit trail; a failure to record the effect —
// which would break replay — aborts the call.
//
// It returns an error ONLY when delivery is worth retrying: there are active
// webhooks, none accepted the payload, and at least one failure was Retryable
// (transport/408/429/5xx). The schedulers rely on this — a retryable total failure
// keeps the firing-edge alert unrecorded so the next tick re-delivers. When every
// failure is Permanent (a 4xx the endpoint will never accept), it returns nil so the
// alert edge IS recorded: retrying a dead endpoint forever would never deliver and
// would suppress the alert into a permanent re-fire loop. Each attempt is recorded
// for the audit trail regardless. A partial success (≥1 accepted) is success; zero
// configured webhooks is a vacuous success.
// AnyAccepted reports whether at least one delivery in the set was accepted (2xx).
// A scheduler counts an alert as delivered only when an endpoint actually took it: an
// all-permanent-failure sweep is deduped (Deliver returns nil so the alert edge is
// recorded) but delivered nothing, so it must not inflate the delivered count.
func AnyAccepted(results []DeliveryResult) bool {
	for _, r := range results {
		if r.OK {
			return true
		}
	}
	return false
}

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
	accepted, retryable := 0, 0
	for _, h := range hooks {
		res := n.post(ctx, h, body)
		if err := n.record(ctx, id, h, res, reason); err != nil {
			return nil, err
		}
		switch res.Outcome {
		case OutcomeAccepted:
			accepted++
		case OutcomeRetryable:
			retryable++
		}
		results = append(results, res)
	}
	if len(hooks) > 0 && accepted == 0 && retryable > 0 {
		return results, fmt.Errorf("notify: delivery failed to all %d active webhook(s), retryable", len(hooks))
	}
	return results, nil
}

func (n *Notifier) post(ctx context.Context, h View, body []byte) DeliveryResult {
	res := DeliveryResult{WebhookID: h.WebhookID, URL: h.URL}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body)) // #nosec G107
	if err != nil {
		// A malformed request URL will recur every attempt — permanent.
		res.Outcome, res.Error = OutcomePermanent, err.Error()
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		// Transport failures (DNS, connection refused, timeout) are transient.
		res.Outcome, res.Error = OutcomeRetryable, err.Error()
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	res.Status = resp.StatusCode
	res.Outcome = classifyStatus(resp.StatusCode)
	res.OK = res.Outcome == OutcomeAccepted
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
