// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command is the Case Manager's write side (imperative shell): it
// validates via the functional core, then appends events to the log. Commands
// that target an existing case verify it exists by folding the case stream.
package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	decisionevents "github.com/e6qu/intraktible/decision-engine/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
)

// Handler records case lifecycle events.
type Handler struct {
	log   eventlog.Log
	mu    sync.Mutex
	now   func() time.Time
	newID func() string
}

// NewHandler builds a Handler using the system clock and a random id source.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{
		log:   log,
		now:   func() time.Time { return time.Now().UTC() },
		newID: newID,
	}
}

// RequestReview opens a case and returns its id.
func (h *Handler) RequestReview(ctx context.Context, id identity.Identity, cmd domain.RequestReview) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	caseID := h.newID()
	payload, err := json.Marshal(events.ReviewRequested{
		CaseID:           caseID,
		CompanyName:      cmd.CompanyName,
		CaseType:         cmd.CaseType,
		SLADays:          cmd.SLADays,
		Context:          cmd.Context,
		SourceDecisionID: cmd.SourceDecisionID,
	})
	if err != nil {
		return "", eventlog.Envelope{}, fmt.Errorf("case-manager: marshal requested: %w", err)
	}
	e, err := h.append(ctx, id, events.TypeReviewRequested, payload)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	return caseID, e, nil
}

// AssignCase assigns an existing case to a reviewer.
func (h *Handler) AssignCase(ctx context.Context, id identity.Identity, cmd domain.AssignCase) (eventlog.Envelope, error) {
	return h.onExisting(ctx, id, cmd.CaseID, cmd.Validate, events.TypeCaseAssigned,
		events.CaseAssigned{CaseID: cmd.CaseID, Assignee: cmd.Assignee})
}

// SetStatus transitions an existing case to a new status.
func (h *Handler) SetStatus(ctx context.Context, id identity.Identity, cmd domain.SetStatus) (eventlog.Envelope, error) {
	return h.onExisting(ctx, id, cmd.CaseID, cmd.Validate, events.TypeCaseStatusChanged,
		events.CaseStatusChanged{CaseID: cmd.CaseID, Status: cmd.Status})
}

// AddNote appends a note to an existing case.
func (h *Handler) AddNote(ctx context.Context, id identity.Identity, cmd domain.AddNote) (eventlog.Envelope, error) {
	return h.onExisting(ctx, id, cmd.CaseID, cmd.Validate, events.TypeCaseNoteAdded,
		events.CaseNoteAdded{CaseID: cmd.CaseID, Text: cmd.Text})
}

// slaCaseState is the folded state the SLA sweep needs per case.
type slaCaseState struct {
	createdAt time.Time
	slaDays   int
	status    string
	breached  bool
}

// SweepSLA finds the tenant's open cases whose SLA deadline has passed as of now
// and emits a CaseSLABreached event for each not-yet-breached one, returning the
// breached case ids. It is the push side of SLA tracking (a scheduler calls it):
// the breach is an effect computed against the wall clock and then recorded, so
// replay reads the recorded breaches and stays stable. It is idempotent — a case
// already breached is skipped — so repeated sweeps do not double-emit.
func (h *Handler) SweepSLA(ctx context.Context, id identity.Identity, now time.Time) ([]string, error) {
	if err := id.Valid(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	states, err := h.caseStates(ctx, id)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(states))
	for cid := range states {
		ids = append(ids, cid)
	}
	sort.Strings(ids) // deterministic emission order
	var breached []string
	for _, cid := range ids {
		st := states[cid]
		if st.breached || st.status == domain.StatusCompleted {
			continue
		}
		if domain.SLAState(st.createdAt, st.slaDays, now) != domain.SLAOverdue {
			continue
		}
		b, err := json.Marshal(events.CaseSLABreached{CaseID: cid})
		if err != nil {
			return breached, fmt.Errorf("case-manager: marshal sla_breached: %w", err)
		}
		if _, err := h.append(ctx, id, events.TypeCaseSLABreached, b); err != nil {
			return breached, err
		}
		breached = append(breached, cid)
	}
	return breached, nil
}

// caseStates folds the tenant's case stream into current per-case SLA state,
// covering both open paths (manual ReviewRequested and decision-escalated
// ManualReviewRequested), status changes, and prior breaches.
func (h *Handler) caseStates(ctx context.Context, id identity.Identity) (map[string]slaCaseState, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("case-manager: read log: %w", err)
	}
	states := make(map[string]slaCaseState)
	for _, e := range evs {
		if e.Org != id.Org || e.Workspace != id.Workspace {
			continue
		}
		switch e.Type {
		case events.TypeReviewRequested:
			var p events.ReviewRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode requested seq %d: %w", e.Seq, err)
			}
			states[p.CaseID] = slaCaseState{createdAt: e.Time, slaDays: p.SLADays, status: domain.StatusNeedsReview}
		case decisionevents.TypeManualReviewRequested:
			var p decisionevents.ManualReviewRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode escalated seq %d: %w", e.Seq, err)
			}
			states[p.CaseID] = slaCaseState{createdAt: e.Time, slaDays: p.SLADays, status: domain.StatusNeedsReview}
		case events.TypeCaseStatusChanged:
			var p events.CaseStatusChanged
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode status seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok {
				st.status = p.Status
				states[p.CaseID] = st
			}
		case events.TypeCaseSLABreached:
			var p events.CaseSLABreached
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode breached seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok {
				st.breached = true
				states[p.CaseID] = st
			}
		}
	}
	return states, nil
}

// onExisting validates the command, verifies the case exists for the tenant, and
// appends the event — serialized so existence and append are linearizable.
func (h *Handler) onExisting(ctx context.Context, id identity.Identity, caseID string, validate func() error, typ string, payload any) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal %s: %w", typ, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	exists, err := h.caseExists(ctx, id, caseID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !exists {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: unknown case %q", caseID)
	}
	return h.append(ctx, id, typ, b)
}

// caseExists reports whether the tenant has opened the given case — by either
// path: a manual ReviewRequested or a decision-escalated ManualReviewRequested.
// (Matching only the manual path left escalated cases un-actionable: visible in
// the queue but rejected as "unknown" by assign/status/note.)
func (h *Handler) caseExists(ctx context.Context, id identity.Identity, caseID string) (bool, error) {
	evs, err := h.log.Read(ctx, 0)
	if err != nil {
		return false, fmt.Errorf("case-manager: read log: %w", err)
	}
	for _, e := range evs {
		if e.Org != id.Org || e.Workspace != id.Workspace {
			continue
		}
		switch e.Type {
		case events.TypeReviewRequested:
			var p events.ReviewRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return false, fmt.Errorf("case-manager: decode requested seq %d: %w", e.Seq, err)
			}
			if p.CaseID == caseID {
				return true, nil
			}
		case decisionevents.TypeManualReviewRequested:
			var p decisionevents.ManualReviewRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return false, fmt.Errorf("case-manager: decode escalated seq %d: %w", e.Seq, err)
			}
			if p.CaseID == caseID {
				return true, nil
			}
		}
	}
	return false, nil
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload json.RawMessage) (eventlog.Envelope, error) {
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamCases,
		Type:      typ,
		Time:      h.now(),
		Payload:   payload,
	})
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
