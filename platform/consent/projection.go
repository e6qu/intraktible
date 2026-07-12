// SPDX-License-Identifier: AGPL-3.0-or-later

package consent

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

// Collection holds one consent record per (subject, purpose), keyed so a subject's
// purposes list by prefix.
const Collection = "consent"

// Record is the current consent state for one (subject, purpose).
type Record struct {
	Org         string      `json:"org"`
	Workspace   string      `json:"workspace"`
	Subject     string      `json:"subject"`
	Purpose     string      `json:"purpose"`
	Granted     bool        `json:"granted"`
	Basis       LawfulBasis `json:"basis,omitempty"`
	GrantedAt   *time.Time  `json:"granted_at,omitempty"`
	WithdrawnAt *time.Time  `json:"withdrawn_at,omitempty"`
	ExpiresAt   *time.Time  `json:"expires_at,omitempty"`
	Evidence    *Evidence   `json:"evidence,omitempty"`
	UpdatedBy   string      `json:"updated_by"`
}

// Active reports whether consent is currently in force as of now: granted and not
// past its expiry.
func (r Record) Active(now time.Time) bool {
	if !r.Granted {
		return false
	}
	return r.ExpiresAt == nil || now.Before(*r.ExpiresAt)
}

// docID keys a record under its subject so List can prefix-scan one subject.
func docID(subject, purpose string) string { return subject + "\x00" + purpose }

func subjectPrefix(subject string) string { return subject + "\x00" }

// Projector folds the consent stream into per-(subject,purpose) records.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return Collection }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains a (subject, purpose) record across grants and withdrawals.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case TypeConsentGranted:
		var p Granted
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("consent: decode granted seq %d: %w", e.Seq, err)
		}
		at := e.Time
		r := Record{
			Org: e.Org, Workspace: e.Workspace, Subject: p.Subject, Purpose: p.Purpose,
			Granted: true, Basis: p.Basis, GrantedAt: &at, ExpiresAt: p.ExpiresAt, Evidence: p.Evidence, UpdatedBy: e.Actor,
		}
		return store.PutDoc(ctx, s, Collection, key(e.Org, e.Workspace, p.Subject, p.Purpose), r)
	case TypeConsentWithdrawn:
		var p Withdrawn
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return fmt.Errorf("consent: decode withdrawn seq %d: %w", e.Seq, err)
		}
		k := key(e.Org, e.Workspace, p.Subject, p.Purpose)
		r, _, err := store.GetDoc[Record](ctx, s, Collection, k)
		if err != nil {
			return err
		}
		at := e.Time
		r.Org, r.Workspace, r.Subject, r.Purpose = e.Org, e.Workspace, p.Subject, p.Purpose
		r.Granted, r.WithdrawnAt, r.UpdatedBy = false, &at, e.Actor
		return store.PutDoc(ctx, s, Collection, k, r)
	default:
		return nil
	}
}

func key(org, workspace, subject, purpose string) string {
	return store.Key(org, workspace, docID(subject, purpose))
}

// Get returns the record for one (subject, purpose), false when none exists.
func Get(ctx context.Context, s store.Store, id identity.Identity, subject, purpose string) (Record, bool, error) {
	return store.GetDoc[Record](ctx, s, Collection, key(id.Org, id.Workspace, subject, purpose))
}

// Has reports whether the subject has active consent for the purpose as of now.
func Has(ctx context.Context, s store.Store, id identity.Identity, subject, purpose string, now time.Time) (bool, error) {
	r, ok, err := Get(ctx, s, id, subject, purpose)
	if err != nil || !ok {
		return false, err
	}
	return r.Active(now), nil
}

// List returns a subject's consent records across all purposes, purpose-sorted.
func List(ctx context.Context, s store.Store, id identity.Identity, subject string) ([]Record, error) {
	out, err := store.ListDocs[Record](ctx, s, Collection, store.Key(id.Org, id.Workspace, subjectPrefix(subject)))
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Purpose < out[j].Purpose })
	return out, nil
}
