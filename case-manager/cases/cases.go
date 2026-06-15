// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cases is the Case Manager's read model: a projector that folds the case
// event stream into per-case documents (the queue/detail view plus an audit log
// built entirely from events).
package cases

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collection is the store collection holding case documents.
const Collection = "cases"

// Note is a note added to a case.
type Note struct {
	Author string    `json:"author"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// AuditEntry is one recorded change to a case (audit-ready log, all from events).
type AuditEntry struct {
	Type   string    `json:"type"`
	Actor  string    `json:"actor"`
	At     time.Time `json:"at"`
	Detail string    `json:"detail,omitempty"`
}

// CaseView is the materialized read model for one case. SLADays is the SLA window
// at open time; "days left" is SLADays minus elapsed days (left to the consumer
// so the read stays clock-free).
type CaseView struct {
	Org         string          `json:"org"`
	Workspace   string          `json:"workspace"`
	CaseID      string          `json:"case_id"`
	CompanyName string          `json:"company_name"`
	CaseType    string          `json:"case_type"`
	Status      string          `json:"status"`
	Assignee    string          `json:"assignee,omitempty"`
	SLADays     int             `json:"sla_days"`
	Context     json.RawMessage `json:"context,omitempty"`
	SourceID    string          `json:"source_decision_id,omitempty"`
	Notes       []Note          `json:"notes"`
	Audit       []AuditEntry    `json:"audit"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Filter narrows a case listing; empty fields do not filter.
type Filter struct {
	Status   string
	CaseType string
	Assignee string
}

// Projector folds case events into CaseView documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "cases" }

// Apply maintains the case document and its audit log.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeReviewRequested:
		return applyRequested(ctx, e, s)
	case events.TypeCaseAssigned:
		return applyAssigned(ctx, e, s)
	case events.TypeCaseStatusChanged:
		return applyStatus(ctx, e, s)
	case events.TypeCaseNoteAdded:
		return applyNote(ctx, e, s)
	default:
		return nil
	}
}

func applyRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ReviewRequested
	if err := decode(e, &p); err != nil {
		return err
	}
	c := CaseView{
		Org: e.Org, Workspace: e.Workspace, CaseID: p.CaseID,
		CompanyName: p.CompanyName, CaseType: p.CaseType, Status: domain.StatusNeedsReview,
		SLADays: p.SLADays, Context: p.Context, SourceID: p.SourceDecisionID,
		Notes: []Note{}, Audit: []AuditEntry{}, CreatedAt: e.Time, UpdatedAt: e.Time,
	}
	c.Audit = append(c.Audit, audit(e, "requested", "opened for "+p.CompanyName))
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.CaseID), c)
}

func applyAssigned(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.CaseAssigned
	if err := decode(e, &p); err != nil {
		return err
	}
	return update(ctx, s, e, p.CaseID, func(c *CaseView) {
		c.Assignee = p.Assignee
		c.Audit = append(c.Audit, audit(e, "assigned", "assigned to "+p.Assignee))
	})
}

func applyStatus(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.CaseStatusChanged
	if err := decode(e, &p); err != nil {
		return err
	}
	return update(ctx, s, e, p.CaseID, func(c *CaseView) {
		c.Status = p.Status
		c.Audit = append(c.Audit, audit(e, "status_changed", "status → "+p.Status))
	})
}

func applyNote(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.CaseNoteAdded
	if err := decode(e, &p); err != nil {
		return err
	}
	return update(ctx, s, e, p.CaseID, func(c *CaseView) {
		c.Notes = append(c.Notes, Note{Author: e.Actor, Text: p.Text, At: e.Time})
		c.Audit = append(c.Audit, audit(e, "note_added", "note added"))
	})
}

// Read returns one case for id's tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, caseID string) (CaseView, bool, error) {
	return store.GetDoc[CaseView](ctx, s, Collection, store.Key(id.Org, id.Workspace, caseID))
}

// List returns the tenant's cases matching the filter, most recent first.
func List(ctx context.Context, s store.Store, id identity.Identity, f Filter) ([]CaseView, error) {
	all, err := store.ListDocs[CaseView](ctx, s, Collection, store.Key(id.Org, id.Workspace, ""))
	if err != nil {
		return nil, err
	}
	out := make([]CaseView, 0, len(all))
	for _, c := range all {
		if f.Status != "" && c.Status != f.Status {
			continue
		}
		if f.CaseType != "" && c.CaseType != f.CaseType {
			continue
		}
		if f.Assignee != "" && c.Assignee != f.Assignee {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func audit(e eventlog.Envelope, typ, detail string) AuditEntry {
	return AuditEntry{Type: typ, Actor: e.Actor, At: e.Time, Detail: detail}
}

func decode[T any](e eventlog.Envelope, v *T) error {
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("cases: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	return nil
}

func update(ctx context.Context, s store.Store, e eventlog.Envelope, caseID string, mutate func(*CaseView)) error {
	key := store.Key(e.Org, e.Workspace, caseID)
	c, ok, err := store.GetDoc[CaseView](ctx, s, Collection, key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cases: event seq %d for unknown case %q", e.Seq, caseID)
	}
	mutate(&c)
	c.UpdatedAt = e.Time
	return store.PutDoc(ctx, s, Collection, key, c)
}
