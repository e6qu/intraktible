// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cases is the Case Manager's read model: a projector that folds the case
// event stream into per-case documents (the queue/detail view plus an audit log
// built entirely from events).
package cases

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
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

// EvidenceLink is one immutable relationship to evidence held elsewhere.
type EvidenceLink struct {
	EvidenceID  string `json:"evidence_id"`
	Requirement string `json:"requirement,omitempty"`
	Kind        string `json:"kind"`
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Label       string `json:"label"`
	ContentHash string `json:"content_hash,omitempty"`
}

// Attachment is immutable metadata for bytes held by the configured artifact
// store; access is audited through a separate event.
type Attachment struct {
	AttachmentID string    `json:"attachment_id"`
	Name         string    `json:"name"`
	MediaType    string    `json:"media_type"`
	Size         int64     `json:"size"`
	SHA256       string    `json:"sha256"`
	StorageRef   string    `json:"storage_ref"`
	Requirement  string    `json:"requirement,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	LawfulBasis  string    `json:"lawful_basis,omitempty"`
	RetainUntil  string    `json:"retain_until,omitempty"`
	LegalHold    bool      `json:"legal_hold,omitempty"`
	Erased       bool      `json:"erased,omitempty"`
	RegisteredBy string    `json:"registered_by"`
	RegisteredAt time.Time `json:"registered_at"`
	AccessCount  int       `json:"access_count"`
	LastAccessed time.Time `json:"last_accessed,omitempty"`
}

// QAReview is the current independent-review state for a sampled case.
type QAReview struct {
	SampleID        string `json:"sample_id"`
	PrimaryActor    string `json:"primary_actor"`
	Reviewer        string `json:"reviewer"`
	Status          string `json:"status"`
	Disposition     string `json:"disposition,omitempty"`
	ReasonCode      string `json:"reason_code,omitempty"`
	Agreement       bool   `json:"agreement,omitempty"`
	Override        bool   `json:"override,omitempty"`
	Note            string `json:"note,omitempty"`
	Feedback        string `json:"feedback,omitempty"`
	FeedbackAuthor  string `json:"feedback_author,omitempty"`
	Validated       bool   `json:"validated,omitempty"`
	Disputed        bool   `json:"disputed,omitempty"`
	Effective       string `json:"effective_disposition,omitempty"`
	EffectiveReason string `json:"effective_reason_code,omitempty"`
}

// SLADeliveryAttempt is one durable external escalation boundary result.
type SLADeliveryAttempt struct {
	Round   int                       `json:"round"`
	Attempt int                       `json:"attempt"`
	Outcome events.SLADeliveryOutcome `json:"outcome"`
	At      time.Time                 `json:"at"`
	Actor   string                    `json:"actor"`
}

// SubjectGovernance is the read-time privacy and lifecycle position for the
// case subject. It is deliberately not projected from case events: legal holds,
// erasure, and statutory windows are independently mutable authoritative state.
type SubjectGovernance struct {
	Subject     string    `json:"subject"`
	Retained    bool      `json:"retained"`
	RetainUntil time.Time `json:"retain_until,omitempty"`
	LegalHold   bool      `json:"legal_hold,omitempty"`
	Erased      bool      `json:"erased,omitempty"`
}

// CaseView is the materialized read model for one case. SLADays is the SLA window
// at open time. DaysLeft and SLAState are clock-derived: the projector leaves them
// zero (the stored model stays clock-free + replay-stable) and the read layer fills
// them via AnnotateSLA against the current time.
type CaseView struct {
	Org                 string                     `json:"org"`
	Workspace           string                     `json:"workspace"`
	CaseID              string                     `json:"case_id"`
	CompanyName         string                     `json:"company_name"`
	CaseType            string                     `json:"case_type"`
	CaseTypeVersion     int                        `json:"case_type_version"`
	Status              domain.CaseStatus          `json:"status"`
	Priority            domain.Priority            `json:"priority"`
	Queue               string                     `json:"queue,omitempty"`
	RoutingExplanation  string                     `json:"routing_explanation,omitempty"`
	Assignee            string                     `json:"assignee,omitempty"`
	Jurisdiction        string                     `json:"jurisdiction,omitempty"`
	Subject             string                     `json:"subject,omitempty"`
	SubjectGovernance   *SubjectGovernance         `json:"subject_governance,omitempty"`
	SLADays             int                        `json:"sla_days"`
	Deadline            time.Time                  `json:"deadline,omitempty"`
	DaysLeft            int                        `json:"days_left"`
	SLAState            domain.SLAStatus           `json:"sla_state,omitempty"`
	SLABreached         bool                       `json:"sla_breached,omitempty"`
	SLAEscalationStatus events.SLAEscalationStatus `json:"sla_escalation_status,omitempty"`
	SLAEscalationRound  int                        `json:"sla_escalation_round,omitempty"`
	SLADeliveryAttempts []SLADeliveryAttempt       `json:"sla_delivery_attempts"`
	Context             json.RawMessage            `json:"context,omitempty"`
	SourceID            string                     `json:"source_decision_id,omitempty"`
	Disposition         string                     `json:"disposition,omitempty"`
	ReasonCode          string                     `json:"reason_code,omitempty"`
	DispositionNote     string                     `json:"disposition_note,omitempty"`
	DispositionOverride bool                       `json:"disposition_override,omitempty"`
	Evidence            []EvidenceLink             `json:"evidence"`
	Attachments         []Attachment               `json:"attachments"`
	QA                  *QAReview                  `json:"qa,omitempty"`
	Notes               []Note                     `json:"notes"`
	Audit               []AuditEntry               `json:"audit"`
	CreatedAt           time.Time                  `json:"created_at"`
	FirstActionAt       time.Time                  `json:"first_action_at,omitempty"`
	ResolvedAt          time.Time                  `json:"resolved_at,omitempty"`
	UpdatedAt           time.Time                  `json:"updated_at"`
}

// MarshalJSON keeps optional timestamps honest on the wire. time.Time is a
// struct, so encoding/json does not apply omitempty to its zero value; without
// this projection documents and API responses contain year-one timestamps that
// clients can mistake for real lifecycle evidence.
func (c CaseView) MarshalJSON() ([]byte, error) {
	type alias CaseView
	var deadline, firstActionAt, resolvedAt *time.Time
	if !c.Deadline.IsZero() {
		deadline = &c.Deadline
	}
	if !c.FirstActionAt.IsZero() {
		firstActionAt = &c.FirstActionAt
	}
	if !c.ResolvedAt.IsZero() {
		resolvedAt = &c.ResolvedAt
	}
	return json.Marshal(struct {
		*alias
		Deadline      *time.Time `json:"deadline,omitempty"`
		FirstActionAt *time.Time `json:"first_action_at,omitempty"`
		ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
	}{
		alias:         (*alias)(&c),
		Deadline:      deadline,
		FirstActionAt: firstActionAt,
		ResolvedAt:    resolvedAt,
	})
}

// MaskPII returns a copy whose governed PII fields are hidden unless role is
// explicitly allowed by the exact pinned definition. The stored projection is
// never modified, so a privileged read cannot inherit a prior masked value.
func MaskPII(view CaseView, definition domain.CaseTypeDefinition, role string) (CaseView, error) {
	if len(view.Context) == 0 {
		return view, nil
	}
	var values map[string]any
	if err := json.Unmarshal(view.Context, &values); err != nil {
		return CaseView{}, fmt.Errorf("cases: decode context for PII masking: %w", err)
	}
	for _, field := range definition.Fields {
		if !field.PII || slices.Contains(field.ReadBy, role) {
			continue
		}
		if _, present := values[field.Key]; present {
			values[field.Key] = "[masked]"
		}
	}
	masked, err := json.Marshal(values)
	if err != nil {
		return CaseView{}, fmt.Errorf("cases: encode masked context: %w", err)
	}
	view.Context = masked
	return view, nil
}

// AnnotateSLA fills a view's clock-derived SLA fields from now. The read layer
// calls this so the stored projection itself stays clock-free.
func AnnotateSLA(c *CaseView, now time.Time) {
	if c.Status == domain.StatusCompleted || !c.ResolvedAt.IsZero() {
		c.DaysLeft = 0
		c.SLAState = ""
		return
	}
	if !c.Deadline.IsZero() {
		remaining := c.Deadline.Sub(now)
		c.DaysLeft = int(remaining.Hours() / 24)
		switch {
		case !now.Before(c.Deadline):
			c.SLAState = domain.SLAOverdue
		case remaining <= 24*time.Hour:
			c.SLAState = domain.SLADueSoon
		default:
			c.SLAState = domain.SLAOnTrack
		}
		return
	}
	c.DaysLeft = domain.DaysLeft(c.CreatedAt, c.SLADays, now)
	c.SLAState = domain.SLAState(c.CreatedAt, c.SLADays, now)
}

// Summary is an at-a-glance roll-up of a tenant's case queue.
type Summary struct {
	Total      int                       `json:"total"`
	ByStatus   map[domain.CaseStatus]int `json:"by_status"`
	Unassigned int                       `json:"unassigned"`
	DueSoon    int                       `json:"due_soon"`
	Overdue    int                       `json:"overdue"`
}

// Summarize rolls up cases for the queue dashboard, bucketing SLA state against
// now. Terminal cases no longer count against the SLA clock.
func Summarize(views []CaseView, now time.Time) Summary {
	s := Summary{ByStatus: map[domain.CaseStatus]int{}, Total: len(views)}
	for i := range views {
		c := views[i]
		s.ByStatus[c.Status]++
		// A terminal case is off the queue — it counts against neither the SLA clock
		// nor the "unassigned" backlog (which tracks OPEN cases waiting for an owner).
		if c.Status == domain.StatusCompleted || !c.ResolvedAt.IsZero() {
			continue
		}
		if c.Assignee == "" {
			s.Unassigned++
		}
		AnnotateSLA(&c, now)
		switch c.SLAState {
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
	Status       string
	CaseType     string
	Assignee     string
	Queue        string
	Priority     string
	Jurisdiction string
	Subject      string
	Query        string
	SLAState     string
	Now          time.Time
}

// Projector folds case events into CaseView documents.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "cases" }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string {
	return []string{
		Collection, CaseTypesCollection, CaseTypeLatestCollection,
		QueuesCollection, ReviewersCollection, SavedViewsCollection, BulkCollection,
	}
}

// Apply maintains the case document and its audit log.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeReviewRequested:
		return applyRequested(ctx, e, s)
	case decisionevents.TypeManualReviewRequested:
		return applyEscalated(ctx, e, s)
	case decisionevents.TypeDecisionResumed:
		return applyDecisionResumed(ctx, e, s)
	case events.TypeCaseAssigned:
		return applyAssigned(ctx, e, s)
	case events.TypeCaseStatusChanged:
		return applyStatus(ctx, e, s)
	case events.TypeCaseNoteAdded:
		return applyNote(ctx, e, s)
	case events.TypeCaseSLABreached:
		return applySLABreached(ctx, e, s)
	case events.TypeCaseSLAEscalated:
		return applySLAEscalated(ctx, e, s)
	case events.TypeCaseSLADeliveryAttempted:
		return applySLADeliveryAttempt(ctx, e, s)
	case events.TypeCaseSLAEscalationRetried:
		return applySLAEscalationRetried(ctx, e, s)
	default:
		return applyEnterpriseEvent(ctx, e, s)
	}
}

// applyDecisionResumed closes the exact human-review case whose recorded outcome
// unpaused the source decision. The decision event is the cross-component contract:
// Case Manager remains decoupled from the Decision Engine command handler while
// replay reconstructs both terminal states from the same human action.
func applyDecisionResumed(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p decisionevents.DecisionResumed
	if err := decode(e, &p); err != nil {
		return err
	}
	if p.CaseID == "" {
		return nil // historical events pre-dating the explicit case link
	}
	c, ok, err := store.GetDoc[CaseView](ctx, s, Collection, store.Key(e.Org, e.Workspace, p.CaseID))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cases: decision_resumed seq %d for unknown case %q", e.Seq, p.CaseID)
	}
	if c.SourceID != p.DecisionID {
		return fmt.Errorf(
			"cases: decision_resumed seq %d case %q belongs to decision %q, not %q",
			e.Seq, p.CaseID, c.SourceID, p.DecisionID,
		)
	}
	return update(ctx, s, e, p.CaseID, func(c *CaseView) {
		c.Status = domain.StatusCompleted
		markAction(c, e.Time)
		c.ResolvedAt = e.Time
		c.Audit = append(c.Audit, audit(e, "decision_resumed", "human outcome recorded; source decision resumed"))
	})
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
		c.SLAEscalationStatus = events.SLAEscalationPending
		c.Audit = append(c.Audit, audit(e, "sla_breached", "SLA deadline passed"))
	})
}

func applySLAEscalated(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.CaseSLAEscalated
	if err := decode(e, &p); err != nil {
		return err
	}
	if !p.Status.Terminal() {
		return fmt.Errorf("cases: SLA escalation seq %d has invalid terminal status %q", e.Seq, p.Status)
	}
	return update(ctx, s, e, p.CaseID, func(c *CaseView) {
		c.SLAEscalationStatus = p.Status
		c.Audit = append(c.Audit, audit(e, "sla_escalated", "external escalation → "+string(p.Status)))
	})
}

func applySLADeliveryAttempt(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.CaseSLADeliveryAttempted
	if err := decode(e, &p); err != nil {
		return err
	}
	if !p.Outcome.Valid() {
		return fmt.Errorf("cases: SLA delivery attempt seq %d has invalid outcome %q", e.Seq, p.Outcome)
	}
	return updateChecked(ctx, s, e, p.CaseID, func(c *CaseView) error {
		if p.Round != c.SLAEscalationRound {
			return fmt.Errorf("SLA delivery round %d does not match current round %d", p.Round, c.SLAEscalationRound)
		}
		expected := 1
		for _, attempt := range c.SLADeliveryAttempts {
			if attempt.Round == p.Round {
				expected++
			}
		}
		if p.Attempt != expected {
			return fmt.Errorf("SLA delivery attempt %d is not next attempt %d", p.Attempt, expected)
		}
		c.SLADeliveryAttempts = append(c.SLADeliveryAttempts, SLADeliveryAttempt{
			Round: p.Round, Attempt: p.Attempt, Outcome: p.Outcome, At: e.Time, Actor: e.Actor,
		})
		c.Audit = append(c.Audit, audit(e, "sla_delivery_attempted", fmt.Sprintf("round %d attempt %d → %s", p.Round, p.Attempt, p.Outcome)))
		return nil
	})
}

func applySLAEscalationRetried(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.CaseSLAEscalationRetried
	if err := decode(e, &p); err != nil {
		return err
	}
	return updateChecked(ctx, s, e, p.CaseID, func(c *CaseView) error {
		if p.Round != c.SLAEscalationRound+1 {
			return fmt.Errorf("SLA retry round %d is not next round %d", p.Round, c.SLAEscalationRound+1)
		}
		if c.SLAEscalationStatus != events.SLAEscalationNoChannel &&
			c.SLAEscalationStatus != events.SLAEscalationPermanentFailure {
			return fmt.Errorf("SLA escalation status %q cannot be retried", c.SLAEscalationStatus)
		}
		c.SLAEscalationRound = p.Round
		c.SLAEscalationStatus = events.SLAEscalationPending
		c.Audit = append(c.Audit, audit(e, "sla_escalation_retried", p.Reason))
		return nil
	})
}

// applyEscalated opens a case from a decision flow's manual_review node (the
// escalation hook), linked back to the decision by SourceID.
func applyEscalated(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p decisionevents.ManualReviewRequested
	if err := decode(e, &p); err != nil {
		return err
	}
	input := openCaseInput{
		CaseID: p.CaseID, Company: p.CompanyName, CaseType: p.CaseType,
		Status: domain.StatusNeedsReview, Priority: domain.PriorityNormal, SLADays: p.SLADays,
		Context: p.Context, SourceID: p.DecisionID, Detail: "escalated from decision " + p.DecisionID,
	}
	latest, found, err := store.GetDoc[CaseTypeView](
		ctx, s, CaseTypeLatestCollection, store.Key(e.Org, e.Workspace, p.CaseType),
	)
	if err != nil {
		return err
	}
	if found {
		if err := latest.Definition.ValidateContext(p.Context); err != nil {
			return fmt.Errorf("cases: governed decision escalation %q: %w", p.CaseID, err)
		}
		location, err := time.LoadLocation(latest.Definition.Calendar.Timezone)
		if err != nil {
			return fmt.Errorf("cases: load service calendar for %q: %w", p.CaseID, err)
		}
		deadline, err := domain.BusinessDeadline(e.Time, latest.Definition.Calendar, location)
		if err != nil {
			return err
		}
		input.CaseTypeVersion = latest.Version
		input.Status = latest.Definition.InitialState
		input.Deadline = deadline
		if !latest.Definition.AllowsPriority(input.Priority) {
			input.Priority = latest.Definition.Priorities[0]
		}
	}
	return openCase(ctx, e, s, input)
}

func applyRequested(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ReviewRequested
	if err := decode(e, &p); err != nil {
		return err
	}
	status := domain.StatusNeedsReview
	if p.InitialState != "" {
		var valid bool
		status, valid = domain.ParseStateKey(p.InitialState)
		if !valid {
			return fmt.Errorf("cases: requested seq %d has invalid initial state %q", e.Seq, p.InitialState)
		}
	}
	priority := domain.Priority(p.Priority)
	if priority == "" {
		priority = domain.PriorityNormal
	}
	var deadline time.Time
	if p.Deadline != "" {
		var err error
		deadline, err = time.Parse(time.RFC3339, p.Deadline)
		if err != nil {
			return fmt.Errorf("cases: requested seq %d has invalid deadline: %w", e.Seq, err)
		}
	}
	return openCase(ctx, e, s, openCaseInput{
		CaseID: p.CaseID, Company: p.CompanyName, CaseType: p.CaseType,
		CaseTypeVersion: p.CaseTypeVersion, Status: status, Priority: priority,
		Jurisdiction: p.Jurisdiction, Subject: p.Subject, Deadline: deadline,
		SLADays: p.SLADays, Context: p.Context, SourceID: p.SourceDecisionID,
		Detail: "opened for " + p.CompanyName,
	})
}

type openCaseInput struct {
	CaseID, Company, CaseType string
	CaseTypeVersion           int
	Status                    domain.CaseStatus
	Priority                  domain.Priority
	Jurisdiction, Subject     string
	Deadline                  time.Time
	SLADays                   int
	Context                   json.RawMessage
	SourceID, Detail          string
}

// openCase materializes a freshly opened case (status needs_review) with its
// first audit entry. Used by both the manual and flow-escalation open paths.
func openCase(ctx context.Context, e eventlog.Envelope, s store.Store, input openCaseInput) error {
	// A case opened without an SLA window would be due the instant it opens
	// (immediately overdue); apply the shared default so the reviewer has time (and so
	// this read model and the SLA sweeper agree on the deadline).
	input.SLADays = domain.NormalizeSLADays(input.SLADays)
	c := CaseView{
		Org: e.Org, Workspace: e.Workspace, CaseID: input.CaseID,
		CompanyName: input.Company, CaseType: input.CaseType, CaseTypeVersion: input.CaseTypeVersion,
		Status: input.Status, Priority: input.Priority, Jurisdiction: input.Jurisdiction,
		Subject: input.Subject, Deadline: input.Deadline, SLADays: input.SLADays,
		Context: input.Context, SourceID: input.SourceID,
		Evidence: []EvidenceLink{}, Attachments: []Attachment{}, Notes: []Note{}, Audit: []AuditEntry{},
		SLADeliveryAttempts: []SLADeliveryAttempt{},
		CreatedAt:           e.Time, UpdatedAt: e.Time,
	}
	c.Audit = append(c.Audit, audit(e, "requested", input.Detail))
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, input.CaseID), c)
}

func applyAssigned(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.CaseAssigned
	if err := decode(e, &p); err != nil {
		return err
	}
	return update(ctx, s, e, p.CaseID, func(c *CaseView) {
		c.Assignee = p.Assignee
		markAction(c, e.Time)
		c.Audit = append(c.Audit, audit(e, "assigned", "assigned to "+p.Assignee))
	})
}

func applyStatus(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.CaseStatusChanged
	if err := decode(e, &p); err != nil {
		return err
	}
	return updateChecked(ctx, s, e, p.CaseID, func(c *CaseView) error {
		var (
			status domain.CaseStatus
			valid  bool
		)
		if c.CaseTypeVersion == 0 {
			status, valid = domain.ParseStatus(p.Status)
		} else {
			status, valid = domain.ParseStateKey(p.Status)
		}
		if !valid {
			return fmt.Errorf("unknown status %q", p.Status)
		}
		c.Status = status
		markAction(c, e.Time)
		terminal := status == domain.StatusCompleted
		if c.CaseTypeVersion > 0 {
			id := identity.Identity{Org: e.Org, Workspace: e.Workspace, Actor: e.Actor}
			published, found, err := CaseTypeVersion(ctx, s, id, c.CaseType, c.CaseTypeVersion)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("missing case type %q version %d", c.CaseType, c.CaseTypeVersion)
			}
			terminal = published.Definition.IsTerminal(status)
		}
		if terminal {
			c.ResolvedAt = e.Time
		}
		c.Audit = append(c.Audit, audit(e, "status_changed", "status → "+p.Status))
		return nil
	})
}

func applyNote(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.CaseNoteAdded
	if err := decode(e, &p); err != nil {
		return err
	}
	return update(ctx, s, e, p.CaseID, func(c *CaseView) {
		c.Notes = append(c.Notes, Note{Author: e.Actor, Text: p.Text, At: e.Time})
		markAction(c, e.Time)
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
		if f.Status != "" && c.Status != domain.CaseStatus(f.Status) {
			continue
		}
		if f.CaseType != "" && c.CaseType != f.CaseType {
			continue
		}
		if f.Assignee != "" && c.Assignee != f.Assignee {
			continue
		}
		if f.Queue != "" && c.Queue != f.Queue {
			continue
		}
		if f.Priority != "" && string(c.Priority) != f.Priority {
			continue
		}
		if f.Jurisdiction != "" && c.Jurisdiction != f.Jurisdiction {
			continue
		}
		if f.Subject != "" && c.Subject != f.Subject {
			continue
		}
		if f.SLAState != "" {
			if f.Now.IsZero() {
				return nil, fmt.Errorf("cases: SLA filter requires a read clock")
			}
			AnnotateSLA(&c, f.Now)
			if string(c.SLAState) != f.SLAState {
				continue
			}
		}
		if f.Query != "" && !caseMatchesQuery(c, f.Query) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func caseMatchesQuery(c CaseView, query string) bool {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return true
	}
	values := []string{
		c.CaseID, c.CompanyName, c.CaseType, c.Assignee, c.Queue, c.Jurisdiction,
		c.Subject, c.SourceID, c.Disposition, c.ReasonCode, string(c.Context),
	}
	for _, evidence := range c.Evidence {
		values = append(values, evidence.Label, evidence.SubjectType, evidence.SubjectID)
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

// DuplicateGroup is a deterministic candidate group for human merge/review.
// It never auto-merges records: same subject/source or same open type+company is
// evidence of possible duplication, not proof.
type DuplicateGroup struct {
	Key     string   `json:"key"`
	Reason  string   `json:"reason"`
	CaseIDs []string `json:"case_ids"`
}

// FindDuplicates identifies possible duplicates from authoritative case views.
func FindDuplicates(views []CaseView) []DuplicateGroup {
	type candidate struct {
		reason string
		ids    []string
	}
	groups := map[string]candidate{}
	for _, view := range views {
		key, reason := "", ""
		switch {
		case view.Subject != "":
			key, reason = "subject:"+view.Subject, "same subject"
		case view.SourceID != "":
			key, reason = "decision:"+view.SourceID, "same source decision"
		case view.Status != domain.StatusCompleted && view.ResolvedAt.IsZero():
			key = "open:" + view.CaseType + ":" + strings.ToLower(strings.TrimSpace(view.CompanyName))
			reason = "same open case type and company"
		}
		if key == "" {
			continue
		}
		group := groups[key]
		group.reason = reason
		group.ids = append(group.ids, view.CaseID)
		groups[key] = group
	}
	out := []DuplicateGroup{}
	for key, group := range groups {
		if len(group.ids) < 2 {
			continue
		}
		sort.Strings(group.ids)
		digest := sha256.Sum256([]byte(key))
		out = append(out, DuplicateGroup{
			Key: "duplicate:" + fmt.Sprintf("%x", digest[:12]), Reason: group.reason, CaseIDs: group.ids,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ListAll returns every case across all tenants (the SLA scheduler's sweep input;
// each view carries its own Org/Workspace).
func ListAll(ctx context.Context, s store.Store) ([]CaseView, error) {
	return store.ListDocs[CaseView](ctx, s, Collection, "")
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

func markAction(view *CaseView, at time.Time) {
	if view.FirstActionAt.IsZero() {
		view.FirstActionAt = at
	}
}
