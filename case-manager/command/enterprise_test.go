// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/testutil"
)

func governedType() domain.CaseTypeDefinition {
	return domain.CaseTypeDefinition{
		Key: "edd", Name: "Enhanced due diligence", InitialState: "intake",
		Fields: []domain.FieldDefinition{{
			Key: "country", Label: "Country", Kind: domain.FieldString, Required: true,
		}},
		Transitions: []domain.Transition{
			{From: "intake", To: "investigating"},
			{From: "investigating", To: "resolved"},
		},
		Dispositions: []domain.DispositionDefinition{{
			Key: "clear", Label: "Clear", ReasonCodes: []string{"verified"}, TerminalState: "resolved",
		}},
		Priorities: []domain.Priority{domain.PriorityNormal, domain.PriorityHigh},
		Calendar: domain.ServiceCalendar{
			Timezone: "UTC", Weekdays: []int{1, 2, 3, 4, 5},
			StartHour: 9, EndHour: 17, SLAHours: 8,
		},
		Evidence: []domain.EvidenceRequirement{},
		Layouts:  []domain.RoleLayout{},
	}
}

func TestEvidenceDispositionAndIndependentQAReplay(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	primary := identity.Identity{Org: "demo", Workspace: "main", Actor: "primary"}
	handler := command.NewHandler(log)
	definition := governedType()
	definition.Evidence = []domain.EvidenceRequirement{{
		Key: "registry", Label: "Registry", Kinds: []string{"attachment"}, Required: true,
	}}
	if _, _, err := handler.PublishCaseType(ctx, primary, definition); err != nil {
		t.Fatal(err)
	}
	caseID, _, err := handler.RequestReview(ctx, primary, domain.RequestReview{
		CompanyName: "Acme", CaseType: "edd", Context: json.RawMessage(`{"country":"RO"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RecordDisposition(ctx, primary, caseID, "clear", "verified", "", false); err == nil {
		t.Fatal("disposition accepted without required evidence")
	}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := handler.RegisterAttachment(ctx, primary, events.CaseAttachmentRegistered{
		CaseID: caseID, AttachmentID: "registry-1", Name: "registry.pdf",
		MediaType: "application/pdf", Size: 42, SHA256: digest,
		StorageRef: "s3://approved/cases/registry-1", Requirement: "registry",
		Subject: "company/acme", LawfulBasis: "legal_obligation",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RecordDisposition(ctx, primary, caseID, "clear", "verified", "checked", false); err != nil {
		t.Fatal(err)
	}
	selected, _, err := handler.SelectQA(ctx, primary, caseID, "monthly-2026-07", "qa", 10000)
	if err != nil || !selected {
		t.Fatalf("select QA: selected=%v err=%v", selected, err)
	}
	qa := identity.Identity{Org: "demo", Workspace: "main", Actor: "qa"}
	if _, err := handler.ReviewQA(ctx, qa, caseID, "monthly-2026-07", "clear", "verified", "agreed", false); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	view, found, err := cases.Read(ctx, st, primary, caseID)
	if err != nil || !found {
		t.Fatalf("read replayed case: found=%v err=%v", found, err)
	}
	if view.CaseTypeVersion != 1 || view.Disposition != "clear" ||
		len(view.Attachments) != 1 || view.QA == nil || !view.QA.Agreement {
		t.Fatalf("replayed governed case = %+v", view)
	}
}

func TestGovernedOpenPinsDefinitionAndValidatesContext(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	handler := command.NewHandler(log).WithNow(func() time.Time { return now })
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "maker"}

	version, _, err := handler.PublishCaseType(ctx, id, governedType())
	if err != nil || version != 1 {
		t.Fatalf("publish: version=%d err=%v", version, err)
	}
	if _, _, err := handler.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Missing", CaseType: "edd", Priority: domain.PriorityHigh,
	}); err == nil {
		t.Fatal("governed open accepted missing required context")
	}
	caseID, _, err := handler.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "edd", Priority: domain.PriorityHigh,
		Jurisdiction: "eu", Subject: "company/acme",
		Context: json.RawMessage(`{"country":"RO"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var opened events.ReviewRequested
	for _, event := range recorded {
		if event.Type == events.TypeReviewRequested {
			if err := json.Unmarshal(event.Payload, &opened); err != nil {
				t.Fatal(err)
			}
		}
	}
	if opened.CaseID != caseID || opened.CaseTypeVersion != 1 ||
		opened.InitialState != "intake" || opened.Priority != "high" ||
		opened.Deadline == "" {
		t.Fatalf("governed open not pinned: %+v", opened)
	}
}

func TestTwoReplicasRouteOneCaseOnce(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "router"}
	setup := command.NewHandler(log)
	if _, err := setup.ConfigureQueue(ctx, id, domain.QueueDefinition{
		Key: "edd", Name: "EDD", CaseTypes: []string{"edd"}, RequiredSkills: []string{"aml"}, Capacity: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.ConfigureReviewer(ctx, id, domain.ReviewerProfile{
		Actor: "alice", Skills: []string{"aml"}, Capacity: 5, Active: true,
	}); err != nil {
		t.Fatal(err)
	}
	caseID, _, err := setup.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "edd", Priority: domain.PriorityHigh,
	})
	if err != nil {
		t.Fatal(err)
	}

	handlers := []*command.Handler{command.NewHandler(log), command.NewHandler(log)}
	var wg sync.WaitGroup
	successes := 0
	var resultMu sync.Mutex
	for _, handler := range handlers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if decision, _, err := handler.RouteCase(ctx, id, caseID); err == nil {
				if decision.Queue != "edd" || decision.Assignee != "alice" {
					t.Errorf("route = %+v", decision)
				}
				resultMu.Lock()
				successes++
				resultMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("successful routes = %d, want one", successes)
	}
	recorded, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	routes := 0
	for _, event := range recorded {
		if event.Type == events.TypeCaseRouted {
			routes++
		}
	}
	if routes != 1 {
		t.Fatalf("recorded routes = %d, want one", routes)
	}
}
