// SPDX-License-Identifier: AGPL-3.0-or-later

package casemanager_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestCaseLifecycleReplay(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}

	h := command.NewHandler(log)
	caseID, _, err := h.RequestReview(ctx, id, domain.RequestReview{CompanyName: "Acme Corp", CaseType: "aml", SLADays: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.AssignCase(ctx, id, domain.AssignCase{CaseID: caseID, Assignee: "adam"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetStatus(ctx, id, domain.SetStatus{CaseID: caseID, Status: domain.StatusInProgress}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AddNote(ctx, id, domain.AddNote{CaseID: caseID, Text: "called the customer"}); err != nil {
		t.Fatal(err)
	}

	st := store.NewMemory()
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	c, ok, err := cases.Read(ctx, st, id, caseID)
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if c.CompanyName != "Acme Corp" || c.CaseType != "aml" || c.Assignee != "adam" || c.Status != domain.StatusInProgress {
		t.Fatalf("case: %+v", c)
	}
	if len(c.Notes) != 1 || c.Notes[0].Author != "adam" || c.Notes[0].Text != "called the customer" {
		t.Fatalf("notes: %+v", c.Notes)
	}
	// Audit log is built entirely from events: requested, assigned, status, note.
	wantAudit := []string{"requested", "assigned", "status_changed", "note_added"}
	if len(c.Audit) != len(wantAudit) {
		t.Fatalf("audit length: %+v", c.Audit)
	}
	for i, typ := range wantAudit {
		if c.Audit[i].Type != typ {
			t.Fatalf("audit[%d]=%q, want %q", i, c.Audit[i].Type, typ)
		}
	}
}

func TestCaseQueueFilterAndTenantIsolation(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	a := identity.Identity{Org: "a", Workspace: "main", Actor: "x"}
	b := identity.Identity{Org: "b", Workspace: "main", Actor: "y"}
	h := command.NewHandler(log)

	c1, _, _ := h.RequestReview(ctx, a, domain.RequestReview{CompanyName: "One", CaseType: "aml", SLADays: 1})
	_, _, _ = h.RequestReview(ctx, a, domain.RequestReview{CompanyName: "Two", CaseType: "kyb_kyc", SLADays: 1})
	_, _, _ = h.RequestReview(ctx, b, domain.RequestReview{CompanyName: "Three", CaseType: "aml", SLADays: 1})
	if _, err := h.SetStatus(ctx, a, domain.SetStatus{CaseID: c1, Status: domain.StatusCompleted}); err != nil {
		t.Fatal(err)
	}

	st := store.NewMemory()
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Tenant isolation: a sees 2 cases, b sees 1.
	la, _ := cases.List(ctx, st, a, cases.Filter{})
	lb, _ := cases.List(ctx, st, b, cases.Filter{})
	if len(la) != 2 || len(lb) != 1 {
		t.Fatalf("tenant isolation: a=%d b=%d, want 2/1", len(la), len(lb))
	}
	// Filter by type and status.
	if got, _ := cases.List(ctx, st, a, cases.Filter{CaseType: "aml"}); len(got) != 1 {
		t.Fatalf("filter type=aml: %d, want 1", len(got))
	}
	if got, _ := cases.List(ctx, st, a, cases.Filter{Status: string(domain.StatusNeedsReview)}); len(got) != 1 {
		t.Fatalf("filter status=needs_review: %d, want 1", len(got))
	}
}

func TestCommandsOnUnknownCaseFail(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "o", Workspace: "w", Actor: "a"}
	h := command.NewHandler(log)
	if _, err := h.AssignCase(ctx, id, domain.AssignCase{CaseID: "ghost", Assignee: "x"}); err == nil {
		t.Fatal("expected error assigning an unknown case")
	}
}
