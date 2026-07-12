// SPDX-License-Identifier: AGPL-3.0-or-later

// Package retention computes statutory record-retention status for a data subject: how
// long the compliance records about them (adverse-action notices, credit decisions)
// must be kept, and therefore whether an erasure request must be refused. ECOA / Reg B
// (12 CFR 1002.12) requires a creditor to retain records of a credit decision for 25
// months; GDPR Art. 17(3)(b) in turn exempts erasure where processing (retention) is
// required to comply with a legal obligation. So a subject with a record still inside
// that window is protected from erasure until it lapses — the automatic, rule-driven
// counterpart to a manual legal hold.
package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/e6qu/intraktible/decision-engine/history"
	"github.com/e6qu/intraktible/fairlending"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// ECOAMonths is the Reg B (12 CFR 1002.12) retention period for a consumer-credit
// decision record: 25 months from the date of the decision / adverse action.
const ECOAMonths = 25

// Item is one retained record about a subject and the date it may be disposed of.
type Item struct {
	Kind        string    `json:"kind"` // "adverse_action" | "decision"
	RecordID    string    `json:"record_id"`
	Basis       string    `json:"basis"` // the statutory basis for the period
	RecordedAt  time.Time `json:"recorded_at"`
	RetainUntil time.Time `json:"retain_until"`
}

// Retained reports whether the record may still not be disposed of as of now.
func (i Item) Retained(now time.Time) bool { return now.Before(i.RetainUntil) }

// Status is a subject's overall retention position.
type Status struct {
	Subject     string    `json:"subject"`
	Retained    bool      `json:"retained"`
	RetainUntil time.Time `json:"retain_until,omitempty"` // the latest RetainUntil across retained items
	Items       []Item    `json:"items"`
}

// SubjectItems collects the retention items for a subject from its adverse-action
// issuances and its credit decisions. Both are scanned tenant-wide and filtered by
// subject — the same read pattern the compliance registers use.
func SubjectItems(ctx context.Context, s store.Store, id identity.Identity, subject string) ([]Item, error) {
	var items []Item
	issuances, err := fairlending.ListIssuances(ctx, s, id)
	if err != nil {
		return nil, err
	}
	for _, iv := range issuances {
		if iv.Subject != subject {
			continue
		}
		items = append(items, Item{
			Kind: "adverse_action", RecordID: iv.DecisionID, Basis: "ECOA Reg B §1002.12 (25 months)",
			RecordedAt: iv.IssuedAt, RetainUntil: iv.IssuedAt.AddDate(0, ECOAMonths, 0),
		})
	}
	decisions, err := history.List(ctx, s, id)
	if err != nil {
		return nil, err
	}
	for _, rec := range decisions {
		if rec.EntityType == "" || rec.EntityID == "" || rec.EntityType+"/"+rec.EntityID != subject {
			continue
		}
		at := rec.EndedAt
		if at.IsZero() {
			at = rec.StartedAt
		}
		items = append(items, Item{
			Kind: "decision", RecordID: rec.DecisionID, Basis: "ECOA Reg B §1002.12 (25 months)",
			RecordedAt: at, RetainUntil: at.AddDate(0, ECOAMonths, 0),
		})
	}
	return items, nil
}

// StatusFor returns the subject's retention status as of now.
func StatusFor(ctx context.Context, s store.Store, id identity.Identity, subject string, now time.Time) (Status, error) {
	items, err := SubjectItems(ctx, s, id, subject)
	if err != nil {
		return Status{}, err
	}
	st := Status{Subject: subject, Items: items}
	for _, it := range items {
		if it.Retained(now) {
			st.Retained = true
			if it.RetainUntil.After(st.RetainUntil) {
				st.RetainUntil = it.RetainUntil
			}
		}
	}
	return st, nil
}

// Retained reports whether the subject must be retained (any record still in its
// window), with a human-readable reason — the shape the erasure gate consults.
func Retained(ctx context.Context, s store.Store, id identity.Identity, subject string, now time.Time) (bool, string, error) {
	st, err := StatusFor(ctx, s, id, subject, now)
	if err != nil {
		return false, "", err
	}
	if !st.Retained {
		return false, "", nil
	}
	return true, fmt.Sprintf("a record about this subject must be retained until %s (ECOA Reg B, 25 months); erasure is exempt under GDPR Art. 17(3)(b)", st.RetainUntil.Format("2006-01-02")), nil
}
