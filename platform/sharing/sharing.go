// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sharing is a GLBA opt-out ledger: a consumer's election to stop their
// nonpublic personal information (NPI) being shared with nonaffiliated third parties
// (GLBA §6802, Reg P). It is the opt-out mirror of the consent ledger — consent is
// opt-in and gates an inbound data pull; this is opt-out and gates an outbound share.
// A financial institution records the election here; the decide path consults it so a
// flow node that shares NPI outward is blocked once the subject has opted out. The
// grant/rescind history is event-sourced so the record is durable and auditable.
package sharing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Stream is the event stream for sharing opt-out elections.
const Stream = "platform.sharing"

// Event types.
const (
	TypeOptedOut  = "platform.sharing_opted_out"
	TypeRescinded = "platform.sharing_rescinded"
)

// Collection holds one opt-out record per subject.
const Collection = "sharing_optout"

// OptedOut records a subject electing to stop NPI sharing with nonaffiliated third parties.
type OptedOut struct {
	Subject string `json:"subject"`
	Reason  string `json:"reason,omitempty"`
}

// Rescinded records a subject withdrawing a prior opt-out (opting back in to sharing).
type Rescinded struct {
	Subject string `json:"subject"`
}

// Handler is the sharing write side.
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds a Handler using the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock stamped into recorded events (tests, the seeder).
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// OptOut records a subject's election to stop NPI sharing.
func (h *Handler) OptOut(ctx context.Context, id identity.Identity, subject, reason string) (eventlog.Envelope, error) {
	subject = strings.TrimSpace(subject)
	return h.append(ctx, id, TypeOptedOut, subject, OptedOut{Subject: subject, Reason: strings.TrimSpace(reason)})
}

// Rescind records a subject withdrawing a prior opt-out. It is idempotent: recording it
// when none was active simply leaves the subject not-opted-out.
func (h *Handler) Rescind(ctx context.Context, id identity.Identity, subject string) (eventlog.Envelope, error) {
	subject = strings.TrimSpace(subject)
	return h.append(ctx, id, TypeRescinded, subject, Rescinded{Subject: subject})
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ, subject string, payload any) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if subject == "" {
		return eventlog.Envelope{}, fmt.Errorf("sharing: subject is required")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("sharing: marshal %s: %w", typ, err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: Stream, Type: typ, Time: h.now(), Payload: b,
	})
}

// Record is the current sharing-election state for one subject.
type Record struct {
	Org        string     `json:"org"`
	Workspace  string     `json:"workspace"`
	Subject    string     `json:"subject"`
	OptedOut   bool       `json:"opted_out"`
	Reason     string     `json:"reason,omitempty"`
	OptedOutAt *time.Time `json:"opted_out_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
	UpdatedBy  string     `json:"updated_by"`
}

// Projector folds the sharing stream into a per-subject record.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return Collection }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains a subject's opt-out record across opt-out and rescind events.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case TypeOptedOut:
		var p OptedOut
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("sharing: decode opted_out seq %d: %w", e.Seq, err)
		}
		at := e.Time
		r := Record{
			Org: e.Org, Workspace: e.Workspace, Subject: p.Subject,
			OptedOut: true, Reason: p.Reason, OptedOutAt: &at, UpdatedAt: e.Time, UpdatedBy: e.Actor,
		}
		return store.PutDoc(ctx, s, Collection, key(e.Org, e.Workspace, p.Subject), r)
	case TypeRescinded:
		var p Rescinded
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("sharing: decode rescinded seq %d: %w", e.Seq, err)
		}
		r, _, err := store.GetDoc[Record](ctx, s, Collection, key(e.Org, e.Workspace, p.Subject))
		if err != nil {
			return err
		}
		r.Org, r.Workspace, r.Subject = e.Org, e.Workspace, p.Subject
		r.OptedOut, r.OptedOutAt, r.Reason = false, nil, ""
		r.UpdatedAt, r.UpdatedBy = e.Time, e.Actor
		return store.PutDoc(ctx, s, Collection, key(e.Org, e.Workspace, p.Subject), r)
	default:
		return nil
	}
}

func key(org, workspace, subject string) string { return store.Key(org, workspace, subject) }

// Get returns the record for a subject (false when none exists).
func Get(ctx context.Context, s store.Store, id identity.Identity, subject string) (Record, bool, error) {
	return store.GetDoc[Record](ctx, s, Collection, key(id.Org, id.Workspace, subject))
}

// HasOptedOut reports whether the subject has an active sharing opt-out.
func HasOptedOut(ctx context.Context, s store.Store, id identity.Identity, subject string) (bool, error) {
	r, ok, err := Get(ctx, s, id, subject)
	if err != nil || !ok {
		return false, err
	}
	return r.OptedOut, nil
}

// ListAll returns every sharing record in the tenant, subject-sorted — the compliance
// surface's cross-subject view.
func ListAll(ctx context.Context, s store.Store, id identity.Identity) ([]Record, error) {
	return store.ListByTime(ctx, s, Collection, store.Key(id.Org, id.Workspace, ""),
		func(r Record) time.Time { return r.UpdatedAt }, func(r Record) string { return r.Subject }, true)
}
