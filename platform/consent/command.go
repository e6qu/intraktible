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

// GrantCmd is the input to Grant: a subject's authorization to process their data for
// a purpose under a lawful basis, optionally until ExpiresAt, optionally backed by
// Evidence (the document/audit trail the controller must be able to produce).
type GrantCmd struct {
	Subject   string
	Purpose   string
	Basis     LawfulBasis
	ExpiresAt *time.Time
	Evidence  *Evidence
}

// Grant records a subject's authorization to process their data for a purpose under a
// lawful basis. Basis defaults to consent, but for credit decisioning the correct
// basis is usually contract/legal_obligation/legitimate_interest — consent is rarely
// "freely given" given the power imbalance (GDPR Art. 6; ICO). Evidence, when present,
// must be well formed.
func (h *Handler) Grant(ctx context.Context, id identity.Identity, cmd GrantCmd) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	subject, purpose := strings.TrimSpace(cmd.Subject), strings.TrimSpace(cmd.Purpose)
	if subject == "" || purpose == "" {
		return eventlog.Envelope{}, fmt.Errorf("consent: subject and purpose are required")
	}
	basis := cmd.Basis
	if basis == "" {
		basis = BasisConsent
	}
	if !basis.Valid() {
		return eventlog.Envelope{}, fmt.Errorf("consent: unknown lawful basis %q", basis)
	}
	if cmd.ExpiresAt != nil && !cmd.ExpiresAt.After(h.now()) {
		return eventlog.Envelope{}, fmt.Errorf("consent: expires_at must be in the future")
	}
	ev := cmd.Evidence
	if ev != nil {
		if err := ev.Validate(); err != nil {
			return eventlog.Envelope{}, err
		}
		if ev.Zero() {
			ev = nil
		}
	}
	b, err := json.Marshal(Granted{Subject: subject, Purpose: purpose, Basis: basis, ExpiresAt: cmd.ExpiresAt, Evidence: ev})
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
