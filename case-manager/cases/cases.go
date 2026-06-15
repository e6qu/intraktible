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
	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
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
// at open time. DaysLeft and SLAState are clock-derived: the projector leaves them
// zero (the stored model stays clock-free + replay-stable) and the read layer fills
// them via AnnotateSLA against the current time.
type CaseView struct {
	Org         string          `json:"org"`
	Workspace   string          `json:"workspace"`
	CaseID      string          `json:"case_id"`
	CompanyName string          `json:"company_name"`
	CaseType    string          `json:"case_type"`
	Status      string          `json:"status"`
	Assignee    string          `json:"assignee,omitempty"`
	SLADays     int             `json:"sla_days"`
	DaysLeft    int             `json:"days_left"`
	SLAState    string          `json:"sla_state,omitempty"`
	SLABreached bool            `json:"sla_breached,omitempty"`
	Context     json.RawMessage `json:"context,omitempty"`
	SourceID    string          `json:"source_decision_id,omitempty"`
	Notes       []Note          `json:"notes"`
	Audit       []AuditEntry    `json:"audit"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// AnnotateSLA fills a view's clock-derived SLA fields from now. The read layer
// calls this so the stored projection itself stays clock-free.
func AnnotateSLA(c *CaseView, now time.Time) {
	c.DaysLeft = domain.DaysLeft(c.CreatedAt, c.SLADays, now)
	c.SLAState = domain.SLAState(c.CreatedAt, c.SLADays, now)
}

// Summary is an at-a-glance roll-up of a tenant's case queue.
type Summary struct {
	Total      int            `json:"total"`
	ByStatus   map[string]int `json:"by_status"`
	Unassigned int            `json:"unassigned"`
	DueSoon    int            `json:"due_soon"`
	Overdue    int            `json:"overdue"`
}

// Summarize rolls up cases for the queue dashboard, bucketing SLA state against
// now. Completed cases no longer count against the SLA clock.
func Summarize(views []CaseView, now time.Time) Summary {
	s := Summary{ByStatus: map[string]int{}, Total: len(views)}
	for i := range views {
		c := views[i]
		s.ByStatus[c.Status]++
		if c.Assignee == "" {
			s.Unassigned++
		}
		if c.Status == domain.StatusCompleted {
			continue
		}
		switch domain.SLAState(c.CreatedAt, c.SLADays, now) {
		case domain.SLAOverdue:
			s.Overdue++
		case domain.SLADueSoon:
			s.DueSoon++
		}
	}
	return s
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

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains the case document and its audit log.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeReviewRequested:
		return applyRequested(ctx, e, s)
	case decisionevents.TypeManualReviewRequested:
		return applyEscalated(ctx, e, s)
	case events.TypeCaseAssigned:
		return applyAssigned(ctx, e, s)
	case events.TypeCaseStatusChanged:
		return applyStatus(ctx, e, s)
	case events.TypeCaseNoteAdded:
		return applyNote(ctx, e, s)
	case events.TypeCaseSLABreached:
		return applySLABreached(ctx, e, s)
	default:
		return nil
	}
}

// applySLABreached marks a case breached and audits it. It is idempotent: a case
// already breached is left unchanged, so a re-emitted sweep event is a no-op.
func applySLABreached(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.CaseSLABreached
	if err := decode(e, &p); err != nil {
		return err
	}
	return update(ctx, s, e, p.CaseID, func(c *CaseView) {
		if c.SLABreached {
			return
		}
		c.SLABreached = true
		c.Audit = append(c.Audit, audit(e, "sla_breached", "SLA deadline passed"))
	})
}

// applyEscalated opens a case from a decision flow's manual_review node (the
// escalation hook), linked back to the decision by SourceID.
func applyEscalated(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p decisionevents.ManualReviewRequested
	if err := decode(e, &p); err != nil {
		return err
	}
	return openCase(ctx, e, s, p.CaseID, p.CompanyName, p.CaseType, p.SLADays, p.Context, p.DecisionID, "escalated from decision "+p.DecisionID)
}

func applyRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ReviewRequested
	if err := decode(e, &p); err != nil {
		return err
	}
	return openCase(ctx, e, s, p.CaseID, p.CompanyName, p.CaseType, p.SLADays, p.Context, p.SourceDecisionID, "opened for "+p.CompanyName)
}

// openCase materializes a freshly opened case (status needs_review) with its
// first audit entry. Used by both the manual and flow-escalation open paths.
func openCase(ctx context.Context, e eventlog.Envelope, s store.Store, caseID, company, caseType string, slaDays int, context json.RawMessage, sourceID, detail string) error {
	c := CaseView{
		Org: e.Org, Workspace: e.Workspace, CaseID: caseID,
		CompanyName: company, CaseType: caseType, Status: domain.StatusNeedsReview,
		SLADays: slaDays, Context: context, SourceID: sourceID,
		Notes: []Note{}, Audit: []AuditEntry{}, CreatedAt: e.Time, UpdatedAt: e.Time,
	}
	c.Audit = append(c.Audit, audit(e, "requested", detail))
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, caseID), c)
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
