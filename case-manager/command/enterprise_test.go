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

func TestRoleEditableFieldsAreValidatedAtomicAndReplayable(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "reviewer"}
	definition := governedType()
	definition.Fields = append(definition.Fields, domain.FieldDefinition{
		Key: "risk_score", Label: "Risk score", Kind: domain.FieldNumber,
	})
	definition.Layouts = []domain.RoleLayout{{
		Role: "operator", Sections: []string{"country", "risk_score"},
		Editable: []string{"country", "risk_score"},
	}}
	handler := command.NewHandler(log)
	if _, _, err := handler.PublishCaseType(ctx, id, definition); err != nil {
		t.Fatal(err)
	}
	caseID, _, err := handler.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "edd", Context: json.RawMessage(`{"country":"RO"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.UpdateFields(
		ctx, id, caseID, json.RawMessage(`{"country":"DE"}`), "viewer",
	); err == nil {
		t.Fatal("viewer edited an operator-only field")
	}
	if _, err := handler.UpdateFields(
		ctx, id, caseID, json.RawMessage(`{"risk_score":"high"}`), "operator",
	); err == nil {
		t.Fatal("wrong field kind was accepted")
	}

	nodes := []*command.Handler{command.NewHandler(log), command.NewHandler(log)}
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, err := nodes[0].UpdateFields(
			ctx, id, caseID, json.RawMessage(`{"country":"DE"}`), "operator",
		)
		errs <- err
	}()
	go func() {
		<-start
		_, err := nodes[1].UpdateFields(
			ctx, id, caseID, json.RawMessage(`{"risk_score":87}`), "operator",
		)
		errs <- err
	}()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	view, found, err := cases.Read(ctx, st, id, caseID)
	if err != nil || !found {
		t.Fatalf("read updated case: found=%v err=%v", found, err)
	}
	var contextValues map[string]any
	if err := json.Unmarshal(view.Context, &contextValues); err != nil {
		t.Fatal(err)
	}
	if contextValues["country"] != "DE" || contextValues["risk_score"] != float64(87) {
		t.Fatalf("updated context = %#v", contextValues)
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
	if _, err := handler.SetStatus(ctx, primary, domain.SetStatus{
		CaseID: caseID, Status: "investigating", Role: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RecordDisposition(ctx, primary, caseID, "clear", "verified", "", "operator", false); err == nil {
		t.Fatal("disposition accepted without required evidence")
	}
	if _, err := handler.LinkEvidence(ctx, primary, events.CaseEvidenceLinked{
		CaseID: caseID, EvidenceID: "untyped", Kind: "url",
		SubjectType: "url", SubjectID: "https://example.invalid", Label: "untyped",
	}); err == nil {
		t.Fatal("untyped evidence relationship was accepted")
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
	if _, err := handler.RecordDisposition(ctx, primary, caseID, "clear", "verified", "checked", "operator", false); err != nil {
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
		len(view.Attachments) != 1 || view.QA == nil || !view.QA.Agreement || !view.QA.Validated {
		t.Fatalf("replayed governed case = %+v", view)
	}
	validated, err := cases.ListValidatedOutcomes(ctx, st, primary)
	if err != nil || len(validated) != 1 || validated[0].EffectiveDisposition != "clear" {
		t.Fatalf("validated outcomes = %+v, err=%v", validated, err)
	}
}

func TestRequiredSecondReviewKeepsCaseOpenUntilIndependentValidation(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	primary := identity.Identity{Org: "demo", Workspace: "main", Actor: "primary"}
	handler := command.NewHandler(log)
	definition := governedType()
	definition.Dispositions[0].RequiresSecondReview = true
	if _, _, err := handler.PublishCaseType(ctx, primary, definition); err != nil {
		t.Fatal(err)
	}
	caseID, _, err := handler.RequestReview(ctx, primary, domain.RequestReview{
		CompanyName: "Acme", CaseType: "edd", Context: json.RawMessage(`{"country":"RO"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.SetStatus(ctx, primary, domain.SetStatus{
		CaseID: caseID, Status: "investigating", Role: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RecordDisposition(ctx, primary, caseID, "clear", "verified", "", "operator", false); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.SetStatus(ctx, primary, domain.SetStatus{
		CaseID: caseID, Status: "resolved", Role: "operator",
	}); err == nil {
		t.Fatal("status change bypassed required second review")
	}
	selected, _, err := handler.SelectQA(ctx, primary, caseID, "required-qa", "qa", 10000)
	if err != nil || !selected {
		t.Fatalf("select QA: selected=%v err=%v", selected, err)
	}
	qa := identity.Identity{Org: "demo", Workspace: "main", Actor: "qa"}
	if _, err := handler.ReviewQA(ctx, qa, caseID, "required-qa", "clear", "verified", "", false); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	view, found, err := cases.Read(ctx, st, primary, caseID)
	if err != nil || !found || view.Status != "resolved" || view.ResolvedAt.IsZero() {
		t.Fatalf("second-reviewed case = %+v, found=%v err=%v", view, found, err)
	}
	if breached, err := handler.SweepSLA(ctx, primary, time.Now().UTC().Add(365*24*time.Hour)); err != nil {
		t.Fatal(err)
	} else if len(breached) != 0 {
		t.Fatalf("governed terminal case breached after resolution: %v", breached)
	}
	if _, err := handler.SetStatus(ctx, primary, domain.SetStatus{
		CaseID: caseID, Status: "investigating", Role: "operator",
	}); err == nil {
		t.Fatal("governed terminal case reopened")
	}
	if _, _, err := handler.RouteCase(ctx, primary, caseID); err == nil {
		t.Fatal("governed terminal case routed")
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

func TestQueueCapacityIsAtomicAcrossCases(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "router"}
	setup := command.NewHandler(log)
	if _, err := setup.ConfigureQueue(ctx, id, domain.QueueDefinition{
		Key: "limited", Name: "Limited", CaseTypes: []string{"legacy"}, Capacity: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.ConfigureReviewer(ctx, id, domain.ReviewerProfile{
		Actor: "alice", Capacity: 10, Active: true,
	}); err != nil {
		t.Fatal(err)
	}
	var caseIDs []string
	for _, company := range []string{"A", "B"} {
		caseID, _, err := setup.RequestReview(ctx, id, domain.RequestReview{
			CompanyName: company, CaseType: "legacy",
		})
		if err != nil {
			t.Fatal(err)
		}
		caseIDs = append(caseIDs, caseID)
	}

	nodes := []*command.Handler{command.NewHandler(log), command.NewHandler(log)}
	var wg sync.WaitGroup
	successes := 0
	var resultMu sync.Mutex
	wg.Add(2)
	for index, node := range nodes {
		go func() {
			defer wg.Done()
			if _, _, err := node.RouteCase(ctx, id, caseIDs[index]); err == nil {
				resultMu.Lock()
				successes++
				resultMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("successful capacity-one routes = %d, want one", successes)
	}
	for _, caseID := range caseIDs {
		if _, _, err := command.NewHandler(log).RouteCase(ctx, id, caseID); err == nil {
			t.Fatal("capacity-one queue accepted a second route")
		}
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
		t.Fatalf("route events = %d, want one capacity winner", routes)
	}
}

func TestTwoReplicasEscalateBreachedCaseOnce(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "scheduler"}
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	setup := command.NewHandler(log).WithNow(func() time.Time { return now })
	for _, queue := range []domain.QueueDefinition{
		{
			Key: "primary", Name: "Primary", CaseTypes: []string{"legacy"},
			MaxAgeHours: 23, RequiredSkills: []string{"review"}, Capacity: 100,
			EscalationQueue: "urgent",
		},
		{
			Key: "urgent", Name: "Urgent", CaseTypes: []string{"legacy"},
			MinAgeHours: 24, RequiredSkills: []string{"senior"}, Capacity: 100,
		},
	} {
		if _, err := setup.ConfigureQueue(ctx, id, queue); err != nil {
			t.Fatal(err)
		}
	}
	for _, reviewer := range []domain.ReviewerProfile{
		{Actor: "alice", Skills: []string{"review"}, Capacity: 5, Active: true},
		{Actor: "zoe", Skills: []string{"senior"}, Capacity: 5, Active: true},
	} {
		if _, err := setup.ConfigureReviewer(ctx, id, reviewer); err != nil {
			t.Fatal(err)
		}
	}
	caseID, _, err := setup.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "legacy", SLADays: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision, _, err := setup.RouteCase(ctx, id, caseID); err != nil ||
		decision.Queue != "primary" || decision.Assignee != "alice" {
		t.Fatalf("initial route = %+v, err=%v", decision, err)
	}
	now = now.Add(48 * time.Hour)
	breached, err := setup.SweepSLA(ctx, id, now)
	if err != nil || len(breached) != 1 || breached[0] != caseID {
		t.Fatalf("breached = %v, err=%v", breached, err)
	}

	nodes := []*command.Handler{
		command.NewHandler(log).WithNow(func() time.Time { return now }),
		command.NewHandler(log).WithNow(func() time.Time { return now }),
	}
	var wg sync.WaitGroup
	wg.Add(len(nodes))
	for _, node := range nodes {
		go func() {
			defer wg.Done()
			// An empty list deliberately reconciles durable breaches from the
			// log, simulating restart after the breach append.
			if _, failures, err := node.EscalateBreached(ctx, id, nil); err != nil || len(failures) > 0 {
				t.Errorf("escalate breached: failures=%v err=%v", failures, err)
			}
		}()
	}
	wg.Wait()
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	view, found, err := cases.Read(ctx, st, id, caseID)
	if err != nil || !found || view.Queue != "urgent" || view.Assignee != "zoe" {
		t.Fatalf("escalated case = %+v, found=%v err=%v", view, found, err)
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
	if routes != 2 {
		t.Fatalf("route events = %d, want initial route plus one escalation", routes)
	}
}

func TestStatusAndDispositionShareOneLifecycleClaim(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "primary"}
	setup := command.NewHandler(log)
	if _, _, err := setup.PublishCaseType(ctx, id, governedType()); err != nil {
		t.Fatal(err)
	}
	caseID, _, err := setup.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "edd", Context: json.RawMessage(`{"country":"RO"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.SetStatus(ctx, id, domain.SetStatus{
		CaseID: caseID, Status: "investigating", Role: "operator",
	}); err != nil {
		t.Fatal(err)
	}

	nodes := []*command.Handler{command.NewHandler(log), command.NewHandler(log)}
	var wg sync.WaitGroup
	successes := 0
	var resultMu sync.Mutex
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := nodes[0].SetStatus(ctx, id, domain.SetStatus{
			CaseID: caseID, Status: "resolved", Role: "operator",
		}); err == nil {
			resultMu.Lock()
			successes++
			resultMu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := nodes[1].RecordDisposition(ctx, id, caseID, "clear", "verified", "", "operator", false); err == nil {
			resultMu.Lock()
			successes++
			resultMu.Unlock()
		}
	}()
	wg.Wait()
	if successes != 1 {
		t.Fatalf("successful terminal lifecycle writes = %d, want one", successes)
	}
	recorded, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, event := range recorded {
		if event.Type == events.TypeCaseDispositionRecorded {
			terminal++
		}
		if event.Type == events.TypeCaseStatusChanged {
			var payload events.CaseStatusChanged
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Status == "resolved" {
				terminal++
			}
		}
	}
	if terminal != 1 {
		t.Fatalf("recorded terminal lifecycle events = %d, want one", terminal)
	}
}

func TestBulkIsIdempotentPartialFailureAndReplayStable(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	handler := command.NewHandler(log)
	first, _, err := handler.RequestReview(ctx, id, domain.RequestReview{CompanyName: "A", CaseType: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := handler.RequestReview(ctx, id, domain.RequestReview{CompanyName: "B", CaseType: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	request := command.BulkRequest{
		Operation: command.BulkAssign, CaseIDs: []string{first, second, "missing"},
		Target: "alice",
	}
	result, err := handler.Bulk(ctx, id, "bulk-assign-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Succeeded != 2 || result.Failed != 1 || len(result.Items) != 3 {
		t.Fatalf("bulk result = %+v", result)
	}
	retried, err := command.NewHandler(log).Bulk(ctx, id, "bulk-assign-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if retried.BatchID != result.BatchID || retried.Succeeded != 2 || retried.Failed != 1 {
		t.Fatalf("idempotent retry = %+v, want batch %s", retried, result.BatchID)
	}
	conflict := request
	conflict.Target = "bob"
	if _, err := handler.Bulk(ctx, id, "bulk-assign-1", conflict); err == nil {
		t.Fatal("conflicting idempotency-key reuse was accepted")
	}
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	view, found, err := cases.ReadBulk(ctx, st, id, result.BatchID)
	if err != nil || !found {
		t.Fatalf("read bulk projection: found=%v err=%v", found, err)
	}
	if view.Status != "completed" || view.Succeeded != 2 || view.Failed != 1 || len(view.Items) != 3 {
		t.Fatalf("replayed bulk view = %+v", view)
	}
}

func TestRebalanceMovesOnlyCapacityOverflow(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "admin"}
	handler := command.NewHandler(log)
	if _, err := handler.ConfigureQueue(ctx, id, domain.QueueDefinition{
		Key: "ops", Name: "Operations", CaseTypes: []string{"legacy"}, Capacity: 100,
	}); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []domain.ReviewerProfile{
		{Actor: "alice", Capacity: 1, Active: true},
		{Actor: "bob", Capacity: 2, Active: true},
	} {
		if _, err := handler.ConfigureReviewer(ctx, id, profile); err != nil {
			t.Fatal(err)
		}
	}
	var caseIDs []string
	for _, company := range []string{"A", "B", "C"} {
		caseID, _, err := handler.RequestReview(ctx, id, domain.RequestReview{
			CompanyName: company, CaseType: "legacy",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := handler.AssignCase(ctx, id, domain.AssignCase{CaseID: caseID, Assignee: "alice"}); err != nil {
			t.Fatal(err)
		}
		caseIDs = append(caseIDs, caseID)
	}
	moved, failures, err := handler.Rebalance(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 0 || len(moved) != 2 {
		t.Fatalf("rebalance moved=%v failures=%v", moved, failures)
	}
	alice, bob := 0, 0
	for _, caseID := range caseIDs {
		recorded, readErr := log.Read(ctx, 0)
		if readErr != nil {
			t.Fatal(readErr)
		}
		assignee := ""
		for _, event := range recorded {
			if event.Type == events.TypeCaseAssigned {
				var payload events.CaseAssigned
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.CaseID == caseID {
					assignee = payload.Assignee
				}
			}
			if event.Type == events.TypeCaseRouted {
				var payload events.CaseRouted
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.CaseID == caseID && payload.Assignee != "" {
					assignee = payload.Assignee
				}
			}
		}
		if assignee == "alice" {
			alice++
		} else if assignee == "bob" {
			bob++
		}
	}
	if alice != 1 || bob != 2 {
		t.Fatalf("post-rebalance loads alice=%d bob=%d", alice, bob)
	}
}
