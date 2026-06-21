// SPDX-License-Identifier: AGPL-3.0-or-later

// Package domain is the Case Manager's functional core: pure command validation,
// no I/O.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CaseStatus names a case's lifecycle state. It is a named type (not a bare
// string) so an invalid status is caught at the boundary rather than flowing
// into the read model. JSON marshaling is identical to a plain string.
type CaseStatus string

// Case statuses (PLAN.md §4.2 / data-models §4).
const (
	StatusNeedsReview CaseStatus = "needs_review"
	StatusInProgress  CaseStatus = "in_progress"
	StatusCompleted   CaseStatus = "completed"
)

var statuses = map[CaseStatus]bool{
	StatusNeedsReview: true,
	StatusInProgress:  true,
	StatusCompleted:   true,
}

// Valid reports whether s is a known case status.
func (s CaseStatus) Valid() bool { return statuses[s] }

// ParseStatus converts a raw string (from an event payload) into a CaseStatus,
// reporting ok=false for an unknown value. Projectors parse at the decode boundary
// rather than casting, so a hand-crafted/legacy/future event carrying an unknown
// status cannot land an invalid CaseStatus in the read model.
func ParseStatus(s string) (CaseStatus, bool) {
	cs := CaseStatus(s)
	return cs, statuses[cs]
}

// Terminal reports whether s is an end state no transition may leave. A completed
// case is closed; reopening it would silently re-arm the SLA sweep (SweepSLA and
// Summarize both stop the SLA clock at completed), so the command layer treats
// completed as a one-way door.
func (s CaseStatus) Terminal() bool { return s == StatusCompleted }

// CanTransitionTo reports whether a case currently in status s may move to next.
// next must be a known status, and a terminal status may only stay itself (an
// idempotent no-op) — it can never reopen. Modeling the lifecycle as a method on
// the type, rather than re-checking ad hoc per call site, makes "mutate a closed
// case" non-representable wherever a transition is applied.
func (s CaseStatus) CanTransitionTo(next CaseStatus) bool {
	if !next.Valid() {
		return false
	}
	if s.Terminal() {
		return next == s
	}
	return true
}

// RequestReview opens a case for human review.
type RequestReview struct {
	CompanyName      string
	CaseType         string
	SLADays          int
	Context          json.RawMessage
	SourceDecisionID string
}

// Validate requires a company and case type and a non-negative SLA.
func (c RequestReview) Validate() error {
	if strings.TrimSpace(c.CompanyName) == "" {
		return errors.New("case-manager: company_name is required")
	}
	if strings.TrimSpace(c.CaseType) == "" {
		return errors.New("case-manager: case_type is required")
	}
	if c.SLADays < 0 || c.SLADays > MaxSLADays {
		return fmt.Errorf("case-manager: sla_days must be between 0 and %d, got %d", MaxSLADays, c.SLADays)
	}
	return nil
}

// AssignCase assigns a case to a reviewer.
type AssignCase struct {
	CaseID   string
	Assignee string
}

// Validate requires a case and an assignee.
func (c AssignCase) Validate() error {
	if strings.TrimSpace(c.CaseID) == "" {
		return errors.New("case-manager: case_id is required")
	}
	if strings.TrimSpace(c.Assignee) == "" {
		return errors.New("case-manager: assignee is required")
	}
	return nil
}

// SetStatus transitions a case to a new status.
type SetStatus struct {
	CaseID string
	Status CaseStatus
}

// Validate requires a case and a known status.
func (c SetStatus) Validate() error {
	if strings.TrimSpace(c.CaseID) == "" {
		return errors.New("case-manager: case_id is required")
	}
	if !c.Status.Valid() {
		return fmt.Errorf("case-manager: invalid status %q (needs_review|in_progress|completed)", c.Status)
	}
	return nil
}

// AddNote appends a note to a case.
type AddNote struct {
	CaseID string
	Text   string
}

// Validate requires a case and non-empty text.
func (c AddNote) Validate() error {
	if strings.TrimSpace(c.CaseID) == "" {
		return errors.New("case-manager: case_id is required")
	}
	if strings.TrimSpace(c.Text) == "" {
		return errors.New("case-manager: note text is required")
	}
	return nil
}
