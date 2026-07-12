// SPDX-License-Identifier: AGPL-3.0-or-later

// Package reconsideration records a human review of a solely-automated adverse
// decision — the Art. 22 GDPR right to obtain human intervention and contest, and the
// ECOA/Reg B reconsideration a declined applicant may request. A decision engine can
// decline someone end to end with no person in the loop; when that outcome is
// challenged, a human reviewer upholds or overturns it, and that review — who, why it
// was triggered, the outcome, and a meaningful rationale (not a rubber stamp) — must
// be recorded. The original decision stays immutable; this is an overlay that answers
// "was this automated decline reviewed by a person, and what did they conclude?".
package reconsideration

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

// Stream is the event stream for reconsideration reviews.
const Stream = "reconsideration"

// TypeRecorded records a completed human review of an automated decision.
const TypeRecorded = "reconsideration.recorded"

// Collection holds one review record per decision.
const Collection = "reconsideration"

// Basis is what triggered the human review. A named type so an unrecognized basis is
// rejected at the boundary rather than stored as free text.
type Basis string

const (
	BasisApplicantContest Basis = "applicant_contest" // the applicant exercised the right to contest
	BasisProactive        Basis = "proactive"         // the creditor reviewed on its own initiative
	BasisRegulatorInquiry Basis = "regulator_inquiry" // prompted by a supervisor/regulator inquiry
)

// Valid reports whether b is a recognized basis.
func (b Basis) Valid() bool {
	switch b {
	case BasisApplicantContest, BasisProactive, BasisRegulatorInquiry:
		return true
	default:
		return false
	}
}

// Outcome is the reviewer's conclusion.
type Outcome string

const (
	OutcomeUpheld     Outcome = "upheld"     // the automated decision stands
	OutcomeOverturned Outcome = "overturned" // the human reversed the automated decision
)

// Valid reports whether o is a recognized outcome.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeUpheld, OutcomeOverturned:
		return true
	default:
		return false
	}
}

// Recorded is the event: a human reviewed an automated decision and reached an outcome.
type Recorded struct {
	DecisionID string  `json:"decision_id"`
	Subject    string  `json:"subject,omitempty"` // "type/id", the same subject key as consent/PII/erasure
	Basis      Basis   `json:"basis"`
	Outcome    Outcome `json:"outcome"`
	Rationale  string  `json:"rationale"`
}

// Handler is the reconsideration write side.
type Handler struct {
	log eventlog.Log
	now func() time.Time
}

// NewHandler builds a Handler using the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock used to stamp events (tests, the seeder).
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// RecordCmd is the input to Record: an already-validated review to log. The service
// holds the store and checks the decision is an eligible automated decline; Record
// re-checks the essentials so a malformed review never reaches the log.
type RecordCmd struct {
	DecisionID string
	Subject    string
	Basis      Basis
	Outcome    Outcome
	Rationale  string
}

// Record logs a human review. A meaningful rationale is required — a review with no
// stated reasoning is the rubber-stamp Art. 22 and the ICO forbid.
func (h *Handler) Record(ctx context.Context, id identity.Identity, cmd RecordCmd) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	cmd.DecisionID = strings.TrimSpace(cmd.DecisionID)
	cmd.Rationale = strings.TrimSpace(cmd.Rationale)
	if cmd.DecisionID == "" {
		return eventlog.Envelope{}, fmt.Errorf("reconsideration: decision_id is required")
	}
	if !cmd.Basis.Valid() {
		return eventlog.Envelope{}, fmt.Errorf("reconsideration: unknown basis %q", cmd.Basis)
	}
	if !cmd.Outcome.Valid() {
		return eventlog.Envelope{}, fmt.Errorf("reconsideration: unknown outcome %q", cmd.Outcome)
	}
	if cmd.Rationale == "" {
		return eventlog.Envelope{}, fmt.Errorf("reconsideration: a rationale is required (a review without reasoning is not a meaningful human review)")
	}
	b, err := json.Marshal(Recorded(cmd))
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("reconsideration: marshal recorded: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: Stream, Type: TypeRecorded, Time: h.now(), Payload: b,
	})
}

// Review is the stored review with its provenance — what an auditor reads to confirm a
// person, not the model, stood behind (or reversed) an automated decline.
type Review struct {
	Org        string    `json:"org"`
	Workspace  string    `json:"workspace"`
	DecisionID string    `json:"decision_id"`
	Subject    string    `json:"subject,omitempty"`
	Basis      Basis     `json:"basis"`
	Outcome    Outcome   `json:"outcome"`
	Rationale  string    `json:"rationale"`
	ReviewedAt time.Time `json:"reviewed_at"`
	ReviewedBy string    `json:"reviewed_by"`
}

// Projector folds the reconsideration stream into a per-decision review record.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return Collection }

// Collections lists the store collection this projector owns.
func (Projector) Collections() []string { return []string{Collection} }

// Apply maintains the per-decision review record. A re-review overwrites with the
// latest (the log keeps the full trail).
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != TypeRecorded {
		return nil
	}
	var p Recorded
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("reconsideration: decode recorded seq %d: %w", e.Seq, err)
	}
	v := Review{
		Org: e.Org, Workspace: e.Workspace, DecisionID: p.DecisionID, Subject: p.Subject,
		Basis: p.Basis, Outcome: p.Outcome, Rationale: p.Rationale, ReviewedAt: e.Time, ReviewedBy: e.Actor,
	}
	return store.PutDoc(ctx, s, Collection, store.Key(e.Org, e.Workspace, p.DecisionID), v)
}

// Read returns the review for a decision (false when none exists). It reads one doc
// keyed by decision id — a thin GetDoc bind kept as its own call so handlers, the
// seeder, and tests share the collection/key convention in one place.
func Read(ctx context.Context, s store.Store, id identity.Identity, decisionID string) (Review, bool, error) {
	key := store.Key(id.Org, id.Workspace, decisionID)
	return store.GetDoc[Review](ctx, s, Collection, key)
}

// List returns the tenant's reviews, most recently reviewed first.
func List(ctx context.Context, s store.Store, id identity.Identity) ([]Review, error) {
	return store.ListByTime(ctx, s, Collection, store.Key(id.Org, id.Workspace, ""),
		func(r Review) time.Time { return r.ReviewedAt }, func(r Review) string { return r.DecisionID }, true)
}
