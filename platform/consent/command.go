// SPDX-License-Identifier: AGPL-3.0-or-later

package consent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler is the consent write side (imperative shell).
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds a Handler using the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock used to stamp recorded events (deterministic tests,
// the demo seeder) and returns the handler.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// Grant records a subject's consent to process their data for a purpose under a
// lawful basis (defaulting to explicit consent), optionally until expiresAt.
func (h *Handler) Grant(ctx context.Context, id identity.Identity, subject, purpose string, basis LawfulBasis, expiresAt *time.Time) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	subject, purpose = strings.TrimSpace(subject), strings.TrimSpace(purpose)
	if subject == "" || purpose == "" {
		return eventlog.Envelope{}, fmt.Errorf("consent: subject and purpose are required")
	}
	if basis == "" {
		basis = BasisConsent
	}
	if !basis.Valid() {
		return eventlog.Envelope{}, fmt.Errorf("consent: unknown lawful basis %q", basis)
	}
	if expiresAt != nil && !expiresAt.After(h.now()) {
		return eventlog.Envelope{}, fmt.Errorf("consent: expires_at must be in the future")
	}
	b, err := json.Marshal(Granted{Subject: subject, Purpose: purpose, Basis: basis, ExpiresAt: expiresAt})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("consent: marshal granted: %w", err)
	}
	return h.append(ctx, id, TypeConsentGranted, b)
}

// Withdraw records a subject withdrawing consent for a purpose. It is deliberately
// idempotent — a withdrawal is always a valid expression of the subject's wish, and
// recording it when none was active simply leaves the purpose not-consented.
func (h *Handler) Withdraw(ctx context.Context, id identity.Identity, subject, purpose, reason string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	subject, purpose = strings.TrimSpace(subject), strings.TrimSpace(purpose)
	if subject == "" || purpose == "" {
		return eventlog.Envelope{}, fmt.Errorf("consent: subject and purpose are required")
	}
	b, err := json.Marshal(Withdrawn{Subject: subject, Purpose: purpose, Reason: strings.TrimSpace(reason)})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("consent: marshal withdrawn: %w", err)
	}
	return h.append(ctx, id, TypeConsentWithdrawn, b)
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload []byte) (eventlog.Envelope, error) {
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamConsent, Type: typ, Time: h.now(), Payload: payload,
	})
}
