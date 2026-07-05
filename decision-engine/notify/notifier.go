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

// WithNow overrides the clock (deterministic tests, the demo seeder) and
// returns the notifier.
func (n *Notifier) WithNow(now func() time.Time) *Notifier {
	n.now = now
	return n
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

// DeliverySummary is the outcome of one Deliver call: the per-webhook results plus
// the per-Outcome counts the schedulers branch on. It replaces an earlier
// error-as-control-flow contract (a sentinel error that meant "retry") with explicit
// typed state, so a real failure (store/marshal) is distinct from "every webhook was
// a retryable failure", and callers decide retry/dedup/metric from RetryWorthy() and
// Delivered() rather than re-deriving intent from a nil-vs-error check.
type DeliverySummary struct {
	Results   []DeliveryResult `json:"results"`
	Accepted  int              `json:"accepted"`  // 2xx
	Retryable int              `json:"retryable"` // transport/408/429/5xx — try again
	Permanent int              `json:"permanent"` // other 4xx — will never accept
}

// RetryWorthy reports that nothing was accepted but at least one failure could
// succeed later, so the caller must NOT record the firing-edge alert (retry next
// tick). An all-Permanent total failure is deliberately NOT retry-worthy: the alert
// edge records so a dead endpoint can't trap the monitor in a permanent re-fire loop.
func (s DeliverySummary) RetryWorthy() bool { return s.Accepted == 0 && s.Retryable > 0 }

// Delivered reports whether at least one endpoint accepted the payload (so a scheduler
// counts the alert as delivered only when it actually went somewhere).
func (s DeliverySummary) Delivered() bool { return s.Accepted > 0 }

// Deliver POSTs payload as JSON to every active webhook, recording each attempt (with
// its error) for the audit trail; a failure to record the effect — which would break
// replay — aborts the call with an error. The returned error is reserved for such real
// failures (listing webhooks, marshaling, recording); the retry/dedup decision is read
// from the DeliverySummary, never from the error. Zero configured webhooks is a vacuous
// success (empty summary, not retry-worthy).
func (n *Notifier) Deliver(ctx context.Context, id identity.Identity, reason string, payload any) (DeliverySummary, error) {
	hooks, err := active(ctx, n.store, id)
	if err != nil {
		return DeliverySummary{}, err
	}
	defaultBody, err := json.Marshal(payload)
	if err != nil {
		return DeliverySummary{}, fmt.Errorf("notify: marshal payload: %w", err)
	}
	// Decode once for any per-channel template to render against.
	var data any
	_ = json.Unmarshal(defaultBody, &data)

	sum := DeliverySummary{Results: make([]DeliveryResult, 0, len(hooks))}
	for _, h := range hooks {
		// Routing: skip a webhook whose event filter doesn't match this reason.
		if !wantsReason(h.Events, reason) {
			continue
		}
		body := defaultBody
		if h.Template != "" {
			rendered, terr := renderTemplate(h.Template, data)
			if terr != nil {
				// A template that won't render is a permanent, recorded failure (the
				// subscribe-time check should have caught a parse error; this catches
				// an execution error against this payload) — never silently dropped.
				res := DeliveryResult{WebhookID: h.WebhookID, URL: h.URL, Outcome: OutcomePermanent, Error: "template: " + terr.Error()}
				if rerr := n.record(ctx, id, h, res, reason); rerr != nil {
					return DeliverySummary{}, rerr
				}
				sum.Permanent++
				sum.Results = append(sum.Results, res)
				continue
			}
			body = rendered
		}
		res := n.post(ctx, h, body)
		if err := n.record(ctx, id, h, res, reason); err != nil {
			return DeliverySummary{}, err
		}
		switch res.Outcome {
		case OutcomeAccepted:
			sum.Accepted++
		case OutcomeRetryable:
			sum.Retryable++
		case OutcomePermanent:
			sum.Permanent++
		}
		sum.Results = append(sum.Results, res)
	}
	return sum, nil
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
