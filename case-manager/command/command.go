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
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
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

	// Incremental existence cache (guarded by mu, which every command path holds):
	// the set of opened case ids (tenant-qualified) and the highest log seq folded
	// into it, so caseExists reads only new events instead of re-folding the whole
	// log per mutation. Reading up to head each call preserves read-after-write
	// consistency, including decision-escalated cases on the shared log.
	knownCases     map[string]bool
	casesFoldedSeq uint64
}

// NewHandler builds a Handler using the system clock and a random id source.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{
		log:        log,
		now:        func() time.Time { return time.Now().UTC() },
		newID:      newID,
		knownCases: map[string]bool{},
	}
}

// WithNow overrides the clock used to stamp recorded events (deterministic
// tests, the demo seeder) and returns the handler.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// RequestReview opens a case and returns its id.
func (h *Handler) RequestReview(ctx context.Context, id identity.Identity, cmd domain.RequestReview) (string, eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return "", eventlog.Envelope{}, err
	}
	caseTypeVersion := 0
	initialState := domain.StatusNeedsReview
	priority := cmd.Priority
	if priority == "" {
		priority = domain.PriorityNormal
	}
	deadline := ""
	published, governed, err := h.latestCaseType(ctx, id, cmd.CaseType)
	if err != nil {
		return "", eventlog.Envelope{}, err
	}
	if governed {
		if err := published.Definition.ValidateContext(cmd.Context); err != nil {
			return "", eventlog.Envelope{}, err
		}
		if !published.Definition.AllowsPriority(priority) {
			return "", eventlog.Envelope{}, fmt.Errorf(
				"case-manager: priority %q is not permitted by case type %q version %d",
				priority, cmd.CaseType, published.Version,
			)
		}
		location, err := time.LoadLocation(published.Definition.Calendar.Timezone)
		if err != nil {
			return "", eventlog.Envelope{}, fmt.Errorf(
				"case-manager: load service-calendar timezone %q: %w",
				published.Definition.Calendar.Timezone, err,
			)
		}
		due, err := domain.BusinessDeadline(h.now(), published.Definition.Calendar, location)
		if err != nil {
			return "", eventlog.Envelope{}, err
		}
		caseTypeVersion = published.Version
		initialState = published.Definition.InitialState
		deadline = due.Format(time.RFC3339)
	}
	caseID := h.newID()
	payload, err := json.Marshal(events.ReviewRequested{
		CaseID:           caseID,
		CompanyName:      cmd.CompanyName,
		CaseType:         cmd.CaseType,
		CaseTypeVersion:  caseTypeVersion,
		InitialState:     string(initialState),
		Priority:         string(priority),
		Jurisdiction:     cmd.Jurisdiction,
		Subject:          cmd.Subject,
		Deadline:         deadline,
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

// AssignCase assigns an existing case to a reviewer. Claiming a case is a
// compare-and-swap, not a blind write: two reviewers who both open an unassigned
// case and both click Assign must not both be told they own it. The loser of the
// race is refused, and taking a case off a colleague has to be asked for
// explicitly (cmd.Reassign) rather than happening by accident.
func (h *Handler) AssignCase(ctx context.Context, id identity.Identity, cmd domain.AssignCase) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	b, err := json.Marshal(events.CaseAssigned{CaseID: cmd.CaseID, Assignee: cmd.Assignee})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal %s: %w", events.TypeCaseAssigned, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// The claim pins the append to the state we folded, so the check-then-append is
	// atomic across processes and not just within this Handler's mutex. A loser
	// re-folds and re-checks against the assignee that actually won.
	for attempt := 0; ; attempt++ {
		st, err := h.caseState(ctx, id, cmd.CaseID)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if st.assignee == cmd.Assignee {
			return eventlog.Envelope{}, fmt.Errorf("case-manager: case %q is already assigned to %q", cmd.CaseID, cmd.Assignee)
		}
		if st.assignee != "" && !cmd.Reassign {
			return eventlog.Envelope{}, fmt.Errorf("case-manager: case %q is already assigned to %q — reassign to take it", cmd.CaseID, st.assignee)
		}
		e, err := h.appendUnique(ctx, id, events.TypeCaseAssigned, b, assignClaim(cmd.CaseID, st.assignCount))
		if errors.Is(err, eventlog.ErrConflict) && attempt < maxClaimRetries {
			continue
		}
		return e, err
	}
}

// SetStatus transitions an existing case to a new status, enforcing the CaseStatus
// lifecycle: a completed (terminal) case cannot be reopened, which would otherwise
// silently re-arm the SLA sweep against a legitimately-closed case. The transition
// is appended under a claim on the number of transitions folded, so the check and
// the append are atomic across processes — two nodes both folding `needs_review`
// cannot both commit, one moving the case to `completed` and the other back to
// `in_progress`.
func (h *Handler) SetStatus(ctx context.Context, id identity.Identity, cmd domain.SetStatus) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if err := cmd.Validate(); err != nil {
		return eventlog.Envelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; ; attempt++ {
		st, err := h.caseState(ctx, id, cmd.CaseID)
		if err != nil {
			return eventlog.Envelope{}, err
		}
		if st.sourceDecisionSuspended {
			return eventlog.Envelope{}, fmt.Errorf(
				"case-manager: case %q belongs to suspended decision %q — record its review outcome to change the case lifecycle",
				cmd.CaseID, st.sourceDecisionID,
			)
		}
		terminal := cmd.Status == domain.StatusCompleted
		if st.caseTypeVersion > 0 {
			if st.pendingSecondReview {
				return eventlog.Envelope{}, fmt.Errorf(
					"case-manager: case %q is awaiting required second review",
					cmd.CaseID,
				)
			}
			published, err := h.caseTypeAtVersion(ctx, id, st.caseType, st.caseTypeVersion)
			if err != nil {
				return eventlog.Envelope{}, err
			}
			if published.Definition.IsTerminal(st.status) && !published.Definition.IsTerminal(cmd.Status) {
				return eventlog.Envelope{}, fmt.Errorf(
					"case-manager: terminal governed case %q cannot reopen from %s to %s",
					cmd.CaseID, st.status, cmd.Status,
				)
			}
			if !published.Definition.CanTransition(st.status, cmd.Status, cmd.Role) {
				return eventlog.Envelope{}, fmt.Errorf(
					"case-manager: role %q cannot transition governed case %q from %s to %s",
					cmd.Role, cmd.CaseID, st.status, cmd.Status,
				)
			}
			if published.Definition.IsDispositionTerminal(cmd.Status) {
				return eventlog.Envelope{}, fmt.Errorf(
					"case-manager: governed terminal state %q requires a reasoned disposition",
					cmd.Status,
				)
			}
			terminal = published.Definition.IsTerminal(cmd.Status)
		} else if !st.status.CanTransitionTo(cmd.Status) {
			return eventlog.Envelope{}, fmt.Errorf("case-manager: cannot transition case %q from %s to %s", cmd.CaseID, st.status, cmd.Status)
		}
		b, err := json.Marshal(events.CaseStatusChanged{
			CaseID: cmd.CaseID, Status: string(cmd.Status), Terminal: terminal,
		})
		if err != nil {
			return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal %s: %w", events.TypeCaseStatusChanged, err)
		}
		e, err := h.appendUnique(ctx, id, events.TypeCaseStatusChanged, b, statusClaim(cmd.CaseID, st.statusCount))
		if errors.Is(err, eventlog.ErrConflict) && attempt < maxClaimRetries {
			continue
		}
		return e, err
	}
}

// maxClaimRetries bounds the CAS retry loops: a claim is only lost to a concurrent
// writer, and after a few rounds the caller should see the conflict rather than
// spin.
const maxClaimRetries = 8

// caseState folds one case's current state, failing loudly when the case does not
// exist for the tenant. Callers hold h.mu.
func (h *Handler) caseState(ctx context.Context, id identity.Identity, caseID string) (slaCaseState, error) {
	states, err := h.caseStates(ctx, id)
	if err != nil {
		return slaCaseState{}, err
	}
	st, ok := states[caseID]
	if !ok {
		return slaCaseState{}, fmt.Errorf("case-manager: unknown case %q", caseID)
	}
	return st, nil
}

// The claims below make each fold-then-append atomic across processes. Assign and
// lifecycle carry the count of prior events of their kind, so they are an
// expected-version check. Status changes, dispositions, QA completion, SLA
// reminders, and breaches share the lifecycle claim: a stale sweep can therefore
// never append after a concurrent terminal transition.
func assignClaim(caseID string, seen int) string {
	return "case.assign\x00" + caseID + "\x00" + strconv.Itoa(seen)
}

func statusClaim(caseID string, seen int) string {
	return "case.lifecycle\x00" + caseID + "\x00" + strconv.Itoa(seen)
}

func slaEscalationClaim(caseID string, round int) string {
	return "case.sla_escalation\x00" + caseID + "\x00" + strconv.Itoa(round)
}

func slaDeliveryAttemptClaim(caseID string, round, attempt int) string {
	return "case.sla_delivery\x00" + caseID + "\x00" + strconv.Itoa(round) + "\x00" + strconv.Itoa(attempt)
}

func slaRetryClaim(caseID string, round int) string {
	return "case.sla_retry\x00" + caseID + "\x00" + strconv.Itoa(round)
}

// AddNote appends a note to an existing case.
func (h *Handler) AddNote(ctx context.Context, id identity.Identity, cmd domain.AddNote) (eventlog.Envelope, error) {
	return h.onExisting(ctx, id, cmd.CaseID, cmd.Validate, events.TypeCaseNoteAdded,
		events.CaseNoteAdded{CaseID: cmd.CaseID, Text: cmd.Text})
}

// slaCaseState is the folded state of one case: what the SLA sweep needs, plus the
// current assignee and the per-kind event counts the CAS claims pin an append to.
type slaCaseState struct {
	createdAt        time.Time
	deadline         time.Time
	slaDays          int
	status           domain.CaseStatus
	caseType         string
	caseTypeVersion  int
	priority         domain.Priority
	jurisdiction     string
	context          json.RawMessage
	contextCount     int
	queue            string
	terminal         bool
	breached         bool
	reminded         bool
	escalated        events.SLAEscalationStatus
	deliveryAttempts int
	escalationRound  int

	assignee                 string
	assignCount, statusCount int
	sourceDecisionID         string
	sourceDecisionSuspended  bool
	pendingSecondReview      bool
}

// SweepSLA finds the tenant's open cases whose SLA deadline has passed as of now
// and emits a CaseSLABreached event for each not-yet-breached one, returning the
// breached case ids. It is the push side of SLA tracking (a scheduler calls it):
// the breach is an effect computed against the wall clock and then recorded, so
// replay reads the recorded breaches and stays stable. It is idempotent — a case
// already breached is skipped — so repeated sweeps do not double-emit.
func (h *Handler) SweepSLA(ctx context.Context, id identity.Identity, now time.Time) ([]string, error) {
	breached, _, err := h.SweepSLAWithSeq(ctx, id, now)
	return breached, err
}

// SweepSLAWithSeq is the HTTP-facing form of SweepSLA. It also returns the final
// event sequence emitted by the sweep so an interactive caller can wait until
// every breach/reminder from this run is visible in the case projection.
func (h *Handler) SweepSLAWithSeq(ctx context.Context, id identity.Identity, now time.Time) ([]string, uint64, error) {
	if err := id.Valid(); err != nil {
		return nil, 0, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	states, err := h.caseStates(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, 0, len(states))
	for cid := range states {
		ids = append(ids, cid)
	}
	sort.Strings(ids) // deterministic emission order
	var breached []string
	var finalSeq uint64
	for _, cid := range ids {
		st := states[cid]
		if st.terminal {
			continue
		}
		slaState := domain.SLAState(st.createdAt, st.slaDays, now)
		if !st.deadline.IsZero() {
			switch {
			case !now.Before(st.deadline):
				slaState = domain.SLAOverdue
			case st.deadline.Sub(now) <= 24*time.Hour:
				slaState = domain.SLADueSoon
			default:
				slaState = domain.SLAOnTrack
			}
		}
		switch slaState {
		case domain.SLAOverdue:
			if st.breached {
				continue
			}
			b, err := json.Marshal(events.CaseSLABreached{CaseID: cid})
			if err != nil {
				return breached, finalSeq, fmt.Errorf("case-manager: marshal sla_breached: %w", err)
			}
			// The shared lifecycle claim dedupes scheduler replicas and orders this
			// write against a concurrent status/disposition/QA terminal transition.
			event, err := h.appendUnique(ctx, id, events.TypeCaseSLABreached, b, statusClaim(cid, st.statusCount))
			if err != nil {
				if errors.Is(err, eventlog.ErrConflict) {
					continue
				}
				return breached, finalSeq, err
			}
			finalSeq = event.Seq
			breached = append(breached, cid)
		case domain.SLADueSoon:
			// Nudge once, before breach, so an assignee gets to the task in time.
			if st.reminded {
				continue
			}
			b, err := json.Marshal(events.CaseSLAReminder{CaseID: cid})
			if err != nil {
				return breached, finalSeq, fmt.Errorf("case-manager: marshal sla_reminder: %w", err)
			}
			event, err := h.appendUnique(ctx, id, events.TypeCaseSLAReminder, b, statusClaim(cid, st.statusCount))
			if err != nil {
				if errors.Is(err, eventlog.ErrConflict) {
					continue
				}
				return breached, finalSeq, err
			}
			finalSeq = event.Seq
		}
	}
	return breached, finalSeq, nil
}

// PendingSLAEscalations returns breached cases whose external escalation has no
// terminal outcome yet. It folds the log, rather than the eventually consistent
// projection, so a breach emitted earlier in the same tick is immediately visible
// and retry state survives restart/replay.
func (h *Handler) PendingSLAEscalations(ctx context.Context, id identity.Identity) ([]string, error) {
	if err := id.Valid(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	states, err := h.caseStates(ctx, id)
	if err != nil {
		return nil, err
	}
	var pending []string
	for caseID, st := range states {
		if st.breached && !st.terminal && !st.escalated.Terminal() {
			pending = append(pending, caseID)
		}
	}
	sort.Strings(pending)
	return pending, nil
}

// RecordSLAEscalation records a terminal external-delivery outcome exactly once.
// Retryable outcomes are not passed here: they remain pending for a later tick.
func (h *Handler) RecordSLAEscalation(ctx context.Context, id identity.Identity, caseID string, status events.SLAEscalationStatus) error {
	if err := id.Valid(); err != nil {
		return err
	}
	if !status.Terminal() {
		return fmt.Errorf("case-manager: SLA escalation status %q is not terminal", status)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := h.caseState(ctx, id, caseID)
	if err != nil {
		return err
	}
	if !st.breached {
		return fmt.Errorf("case-manager: case %q has not breached its SLA", caseID)
	}
	if st.escalated.Terminal() {
		return nil
	}
	b, err := json.Marshal(events.CaseSLAEscalated{CaseID: caseID, Status: status})
	if err != nil {
		return fmt.Errorf("case-manager: marshal sla_escalated: %w", err)
	}
	if _, err := h.appendUnique(ctx, id, events.TypeCaseSLAEscalated, b, slaEscalationClaim(caseID, st.escalationRound)); err != nil {
		if errors.Is(err, eventlog.ErrConflict) {
			return nil
		}
		return err
	}
	return nil
}

// RecordSLADeliveryAttempt durably records every external delivery result and
// returns the one-based attempt number.
func (h *Handler) RecordSLADeliveryAttempt(
	ctx context.Context,
	id identity.Identity,
	caseID string,
	outcome events.SLADeliveryOutcome,
) (int, error) {
	if err := id.Valid(); err != nil {
		return 0, err
	}
	if !outcome.Valid() {
		return 0, fmt.Errorf("case-manager: invalid SLA delivery outcome %q", outcome)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := h.caseState(ctx, id, caseID)
	if err != nil {
		return 0, err
	}
	if !st.breached || st.escalated.Terminal() {
		return 0, fmt.Errorf("case-manager: case %q has no pending SLA escalation", caseID)
	}
	attempt := st.deliveryAttempts + 1
	payload, err := json.Marshal(events.CaseSLADeliveryAttempted{
		CaseID: caseID, Round: st.escalationRound, Attempt: attempt, Outcome: outcome,
	})
	if err != nil {
		return 0, fmt.Errorf("case-manager: marshal SLA delivery attempt: %w", err)
	}
	if _, err := h.appendUnique(ctx, id, events.TypeCaseSLADeliveryAttempted, payload, slaDeliveryAttemptClaim(caseID, st.escalationRound, attempt)); err != nil {
		return 0, err
	}
	return attempt, nil
}

// RetrySLAEscalation explicitly requeues a terminal failed/no-channel delivery.
func (h *Handler) RetrySLAEscalation(ctx context.Context, id identity.Identity, caseID, reason string) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return eventlog.Envelope{}, errors.New("case-manager: retry reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := h.caseState(ctx, id, caseID)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if st.escalated != events.SLAEscalationNoChannel && st.escalated != events.SLAEscalationPermanentFailure {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: case %q escalation status %q is not retryable by an operator", caseID, st.escalated)
	}
	round := st.escalationRound + 1
	payload, err := json.Marshal(events.CaseSLAEscalationRetried{CaseID: caseID, Reason: reason, Round: round})
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("case-manager: marshal SLA escalation retry: %w", err)
	}
	return h.appendUnique(ctx, id, events.TypeCaseSLAEscalationRetried, payload, slaRetryClaim(caseID, round))
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
	suspendedDecisions := make(map[string]bool)
	latestTypes := make(map[string]PublishedCaseType)
	type typeVersion struct {
		key     string
		version int
	}
	exactTypes := make(map[typeVersion]domain.CaseTypeDefinition)
	isTerminal := func(state slaCaseState, status domain.CaseStatus) (bool, error) {
		if state.caseTypeVersion == 0 {
			return status == domain.StatusCompleted, nil
		}
		definition, found := exactTypes[typeVersion{key: state.caseType, version: state.caseTypeVersion}]
		if !found {
			return false, fmt.Errorf(
				"case-manager: missing case type %q version %d while folding status",
				state.caseType, state.caseTypeVersion,
			)
		}
		return definition.IsTerminal(status), nil
	}
	for _, e := range evs {
		if e.Org != id.Org || e.Workspace != id.Workspace {
			continue
		}
		switch e.Type {
		case events.TypeCaseTypePublished:
			var p events.CaseTypePublished
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode case type seq %d: %w", e.Seq, err)
			}
			var definition domain.CaseTypeDefinition
			if err := json.Unmarshal(p.Definition, &definition); err != nil {
				return nil, fmt.Errorf("case-manager: decode case type definition seq %d: %w", e.Seq, err)
			}
			if err := definition.Validate(); err != nil {
				return nil, fmt.Errorf("case-manager: invalid case type definition seq %d: %w", e.Seq, err)
			}
			latestTypes[p.Key] = PublishedCaseType{Version: p.Version, Definition: definition}
			exactTypes[typeVersion{key: p.Key, version: p.Version}] = definition
		case events.TypeReviewRequested:
			var p events.ReviewRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode requested seq %d: %w", e.Seq, err)
			}
			status := domain.StatusNeedsReview
			if p.InitialState != "" {
				parsed, valid := domain.ParseStateKey(p.InitialState)
				if !valid {
					return nil, fmt.Errorf("case-manager: case %q has invalid initial state %q at seq %d", p.CaseID, p.InitialState, e.Seq)
				}
				status = parsed
			}
			priority := domain.Priority(p.Priority)
			if priority == "" {
				priority = domain.PriorityNormal
			}
			var deadline time.Time
			if p.Deadline != "" {
				deadline, err = time.Parse(time.RFC3339, p.Deadline)
				if err != nil {
					return nil, fmt.Errorf("case-manager: case %q has invalid deadline at seq %d: %w", p.CaseID, e.Seq, err)
				}
			}
			states[p.CaseID] = slaCaseState{
				createdAt: e.Time, slaDays: domain.NormalizeSLADays(p.SLADays), status: status,
				deadline: deadline, caseType: p.CaseType, caseTypeVersion: p.CaseTypeVersion, priority: priority,
				jurisdiction: p.Jurisdiction, context: p.Context,
			}
		case decisionevents.TypeManualReviewRequested:
			var p decisionevents.ManualReviewRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode escalated seq %d: %w", e.Seq, err)
			}
			status, priority, version := domain.StatusNeedsReview, domain.PriorityNormal, 0
			var deadline time.Time
			if published, governed := latestTypes[p.CaseType]; governed {
				status, version = published.Definition.InitialState, published.Version
				if !published.Definition.AllowsPriority(priority) {
					priority = published.Definition.Priorities[0]
				}
				location, loadErr := time.LoadLocation(published.Definition.Calendar.Timezone)
				if loadErr != nil {
					return nil, fmt.Errorf("case-manager: load case type timezone at seq %d: %w", e.Seq, loadErr)
				}
				deadline, err = domain.BusinessDeadline(e.Time, published.Definition.Calendar, location)
				if err != nil {
					return nil, err
				}
			}
			states[p.CaseID] = slaCaseState{
				createdAt: e.Time, slaDays: domain.NormalizeSLADays(p.SLADays),
				status: status, deadline: deadline, sourceDecisionID: p.DecisionID,
				sourceDecisionSuspended: suspendedDecisions[p.DecisionID],
				caseType:                p.CaseType, caseTypeVersion: version, priority: priority, context: p.Context,
			}
		case decisionevents.TypeDecisionSuspended:
			var p decisionevents.DecisionSuspended
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode decision suspended seq %d: %w", e.Seq, err)
			}
			suspendedDecisions[p.DecisionID] = true
			// New events carry the case id before ManualReviewRequested opens it;
			// this branch also handles a later suspension in an existing decision.
			if st, ok := states[p.CaseID]; ok {
				st.sourceDecisionSuspended = true
				states[p.CaseID] = st
			}
		case decisionevents.TypeDecisionResumed:
			var p decisionevents.DecisionResumed
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode decision resumed seq %d: %w", e.Seq, err)
			}
			suspendedDecisions[p.DecisionID] = false
			if st, ok := states[p.CaseID]; ok {
				if st.sourceDecisionID != p.DecisionID {
					return nil, fmt.Errorf(
						"case-manager: case %q belongs to decision %q, not resumed decision %q at seq %d",
						p.CaseID, st.sourceDecisionID, p.DecisionID, e.Seq,
					)
				}
				st.status = domain.StatusCompleted
				st.terminal = true
				st.sourceDecisionSuspended = false
				states[p.CaseID] = st
			}
		case events.TypeCaseAssigned:
			var p events.CaseAssigned
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode assigned seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok {
				st.assignee = p.Assignee
				st.assignCount++
				states[p.CaseID] = st
			}
		case events.TypeCaseRouted:
			var p events.CaseRouted
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode routed seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok {
				st.queue = p.Queue
				if p.Assignee != "" {
					st.assignee = p.Assignee
					st.assignCount++
				}
				states[p.CaseID] = st
			}
		case events.TypeCaseStatusChanged:
			var p events.CaseStatusChanged
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode status seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok {
				// A status the domain no longer knows must not be silently dropped:
				// keeping the prior status would sweep a case that actually closed.
				status, valid := domain.ParseStateKey(p.Status)
				if !valid {
					return nil, fmt.Errorf("case-manager: case %q has unknown status %q at seq %d", p.CaseID, p.Status, e.Seq)
				}
				st.status = status
				st.terminal, err = isTerminal(st, status)
				if err != nil {
					return nil, err
				}
				st.statusCount++
				states[p.CaseID] = st
			}
		case events.TypeCaseFieldsUpdated:
			var p events.CaseFieldsUpdated
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode field update seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok {
				st.context, err = domain.PatchContext(st.context, p.Fields)
				if err != nil {
					return nil, fmt.Errorf("case-manager: apply field update seq %d: %w", e.Seq, err)
				}
				st.contextCount++
				states[p.CaseID] = st
			}
		case events.TypeCaseQAReviewed:
			var p events.CaseQAReviewed
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode QA review seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok && p.State != "" {
				status, valid := domain.ParseStateKey(p.State)
				if !valid {
					return nil, fmt.Errorf("case-manager: case %q has invalid QA state %q at seq %d", p.CaseID, p.State, e.Seq)
				}
				st.status = status
				st.terminal, err = isTerminal(st, status)
				if err != nil {
					return nil, err
				}
				st.pendingSecondReview = false
				st.statusCount++
				states[p.CaseID] = st
			}
		case events.TypeCaseDispositionRecorded:
			var p events.CaseDispositionRecorded
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode disposition seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok {
				status, valid := domain.ParseStateKey(p.State)
				if !valid {
					return nil, fmt.Errorf("case-manager: case %q has invalid disposition state %q at seq %d", p.CaseID, p.State, e.Seq)
				}
				if !p.RequiresSecondReview {
					st.status = status
					st.terminal = true
				}
				st.pendingSecondReview = p.RequiresSecondReview
				st.statusCount++
				states[p.CaseID] = st
			}
		case events.TypeCaseSLABreached:
			var p events.CaseSLABreached
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode breached seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok {
				st.breached = true
				st.statusCount++
				states[p.CaseID] = st
			}
		case events.TypeCaseSLAReminder:
			var p events.CaseSLAReminder
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode reminder seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok {
				st.reminded = true
				st.statusCount++
				states[p.CaseID] = st
			}
		case events.TypeCaseSLAEscalated:
			var p events.CaseSLAEscalated
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode SLA escalation seq %d: %w", e.Seq, err)
			}
			if !p.Status.Terminal() {
				return nil, fmt.Errorf("case-manager: case %q has invalid SLA escalation status %q at seq %d", p.CaseID, p.Status, e.Seq)
			}
			if st, ok := states[p.CaseID]; ok {
				st.escalated = p.Status
				states[p.CaseID] = st
			}
		case events.TypeCaseSLADeliveryAttempted:
			var p events.CaseSLADeliveryAttempted
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode SLA delivery attempt seq %d: %w", e.Seq, err)
			}
			if !p.Outcome.Valid() {
				return nil, fmt.Errorf("case-manager: case %q has invalid SLA delivery outcome %q at seq %d", p.CaseID, p.Outcome, e.Seq)
			}
			if st, ok := states[p.CaseID]; ok {
				if p.Round != st.escalationRound || p.Attempt != st.deliveryAttempts+1 {
					return nil, fmt.Errorf(
						"case-manager: case %q SLA round/attempt %d/%d is not sequential at seq %d",
						p.CaseID, p.Round, p.Attempt, e.Seq,
					)
				}
				st.deliveryAttempts = p.Attempt
				states[p.CaseID] = st
			}
		case events.TypeCaseSLAEscalationRetried:
			var p events.CaseSLAEscalationRetried
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return nil, fmt.Errorf("case-manager: decode SLA escalation retry seq %d: %w", e.Seq, err)
			}
			if st, ok := states[p.CaseID]; ok {
				if p.Round != st.escalationRound+1 {
					return nil, fmt.Errorf("case-manager: case %q SLA retry round %d is not sequential at seq %d", p.CaseID, p.Round, e.Seq)
				}
				st.escalationRound = p.Round
				st.deliveryAttempts = 0
				st.escalated = events.SLAEscalationPending
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
	if err := h.refreshKnownCases(ctx); err != nil {
		return false, err
	}
	return h.knownCases[caseKey(id.Org, id.Workspace, caseID)], nil
}

// refreshKnownCases folds the log events appended since the last call into the
// opened-case set. Caller holds h.mu. Reading through to head keeps the set
// current (read-after-write), while the incremental fromSeq avoids re-scanning
// the whole log on every mutation.
func (h *Handler) refreshKnownCases(ctx context.Context) error {
	evs, err := h.log.Read(ctx, h.casesFoldedSeq+1)
	if err != nil {
		return fmt.Errorf("case-manager: read log: %w", err)
	}
	for _, e := range evs {
		switch e.Type {
		case events.TypeReviewRequested:
			var p events.ReviewRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return fmt.Errorf("case-manager: decode requested seq %d: %w", e.Seq, err)
			}
			h.knownCases[caseKey(e.Org, e.Workspace, p.CaseID)] = true
		case decisionevents.TypeManualReviewRequested:
			var p decisionevents.ManualReviewRequested
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return fmt.Errorf("case-manager: decode escalated seq %d: %w", e.Seq, err)
			}
			h.knownCases[caseKey(e.Org, e.Workspace, p.CaseID)] = true
		}
		if e.Seq > h.casesFoldedSeq {
			h.casesFoldedSeq = e.Seq
		}
	}
	return nil
}

// caseKey tenant-qualifies a case id for the existence set.
func caseKey(org, workspace, caseID string) string {
	return org + "\x00" + workspace + "\x00" + caseID
}

func (h *Handler) append(ctx context.Context, id identity.Identity, typ string, payload json.RawMessage) (eventlog.Envelope, error) {
	return h.appendUnique(ctx, id, typ, payload, "")
}

// appendUnique appends under a tenant-global claim key: a second append carrying
// the same key fails with ErrConflict, which is how a fold-then-append stays
// atomic across processes. An empty key claims nothing.
func (h *Handler) appendUnique(ctx context.Context, id identity.Identity, typ string, payload json.RawMessage, unique string) (eventlog.Envelope, error) {
	return h.log.Append(ctx, eventlog.Envelope{
		Org:       id.Org,
		Workspace: id.Workspace,
		Actor:     id.Actor,
		Stream:    events.StreamCases,
		Type:      typ,
		Time:      h.now(),
		Payload:   payload,
		Unique:    unique,
	})
}

func newID() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("case-manager: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
