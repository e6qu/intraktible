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

// Case statuses (PLAN.md §4.2 / data-models §4).
const (
	StatusNeedsReview = "needs_review"
	StatusInProgress  = "in_progress"
	StatusCompleted   = "completed"
)

var statuses = map[string]bool{
	StatusNeedsReview: true,
	StatusInProgress:  true,
	StatusCompleted:   true,
}

// ValidStatus reports whether s is a known case status.
func ValidStatus(s string) bool { return statuses[s] }

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
	if c.SLADays < 0 {
		return fmt.Errorf("case-manager: sla_days must be >= 0, got %d", c.SLADays)
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
	Status string
}

// Validate requires a case and a known status.
func (c SetStatus) Validate() error {
	if strings.TrimSpace(c.CaseID) == "" {
		return errors.New("case-manager: case_id is required")
	}
	if !ValidStatus(c.Status) {
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
