// SPDX-License-Identifier: AGPL-3.0-or-later

package schedule

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
)

func TestQueueAssistRequiresEvidenceSupportedByPinnedCaseType(t *testing.T) {
	policy := domain.AssistAutomation{
		Key: "priority", Kind: domain.CaseAssistPrioritization,
		TemplateID: "case-copilot", Environment: domain.CaseAssistProduction,
		EvidenceRequirements: []string{"source_decision"},
	}
	caseType := domain.CaseTypeDefinition{
		Evidence: []domain.EvidenceRequirement{{
			Key: "supporting_record", Kinds: []string{"decision"},
		}},
	}
	err := validateQueueAssistEvidence(policy, caseType)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("incompatible queue assist evidence error = %v", err)
	}
}

// TestTickBreachesOverdueCasesPerTenant proves the scheduler records SLA breaches
// for every tenant's open-and-overdue cases (and only those) without the on-demand
// endpoint being hit, and is idempotent across ticks.
func TestTickBreachesOverdueCasesPerTenant(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()

	h := command.NewHandler(log)
	a := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}
	b := identity.Identity{Org: "other", Workspace: "main", Actor: "beth"}

	overdueA, _, err := h.RequestReview(ctx, a, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 1})
	if err != nil {
		t.Fatal(err)
	}
	freshA, _, err := h.RequestReview(ctx, a, domain.RequestReview{CompanyName: "Beta", CaseType: "aml", SLADays: 30})
	if err != nil {
		t.Fatal(err)
	}
	overdueB, _, err := h.RequestReview(ctx, b, domain.RequestReview{CompanyName: "Gamma", CaseType: "kyb_kyc", SLADays: 1})
	if err != nil {
		t.Fatal(err)
	}

	st := store.NewMemory()
	liveCtx, stopLive := context.WithCancel(ctx)
	live := projection.New(log, st, cases.Projector{})
	if err := live.Start(liveCtx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().AddDate(0, 0, 10) // well past the 1-day SLA
	s := &Scheduler{store: st, cmd: h, now: func() time.Time { return now }}

	sum, err := s.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Breached != 2 {
		t.Fatalf("breached = %d, want 2 (one overdue per tenant)", sum.Breached)
	}

	stopLive()
	live.Wait()
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     identity.Identity
		caseID string
		want   bool
	}{
		{a, overdueA, true},
		{a, freshA, false},
		{b, overdueB, true},
	} {
		c, ok, err := cases.Read(ctx, st, tc.id, tc.caseID)
		if err != nil || !ok {
			t.Fatalf("read %s: ok=%v err=%v", tc.caseID, ok, err)
		}
		if c.SLABreached != tc.want {
			t.Fatalf("case %s SLABreached = %v, want %v", tc.caseID, c.SLABreached, tc.want)
		}
	}

	// A second tick is idempotent: already-breached cases are not re-emitted.
	sum, err = s.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Breached != 0 {
		t.Fatalf("second tick breached = %d, want 0 (idempotent)", sum.Breached)
	}
}

func TestReconcileAssistsUsesPinnedPolicyWithoutChangingCaseWork(t *testing.T) {
	ctx := context.Background()
	log := eventlog.NewMemory()
	handler := command.NewHandler(log)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "maker"}
	definition := domain.CaseTypeDefinition{
		Key: "edd", Name: "EDD", InitialState: "intake",
		Transitions: []domain.Transition{{From: "intake", To: "resolved"}},
		Dispositions: []domain.DispositionDefinition{{
			Key: "clear", Label: "Clear", ReasonCodes: []string{"verified"},
			TerminalState: "resolved",
		}},
		Priorities: []domain.Priority{domain.PriorityNormal},
		Calendar: domain.ServiceCalendar{
			Timezone: "UTC", Weekdays: []int{1, 2, 3, 4, 5},
			StartHour: 9, EndHour: 17, SLAHours: 8,
		},
		Evidence: []domain.EvidenceRequirement{{
			Key: "decision_record", Label: "Decision",
			Kinds: []string{"decision"}, Required: true,
		}},
		Assists: []domain.AssistAutomation{{
			Key: "opening_summary", Kind: domain.CaseAssistSummary,
			TemplateID: "case-copilot", Environment: domain.CaseAssistProduction,
			EvidenceRequirements: []string{"decision_record"},
		}},
	}
	if _, _, err := handler.PublishCaseType(ctx, id, definition); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ConfigureQueue(ctx, id, domain.QueueDefinition{
		Key: "edd", Name: "EDD", CaseTypes: []string{"edd"}, Capacity: 100,
	}); err != nil {
		t.Fatal(err)
	}
	caseID, _, err := handler.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "edd", Priority: domain.PriorityNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.LinkEvidence(ctx, id, events.CaseEvidenceLinked{
		CaseID: caseID, EvidenceID: "decision-1", Requirement: "decision_record",
		Kind: "decision", SubjectType: "decision", SubjectID: "decision-1",
		Label: "Decision record",
	}); err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	if _, err := projection.New(log, st, cases.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var gotView cases.CaseView
	var gotPolicy domain.AssistAutomation
	var gotSource AssistPolicySource
	scheduler := NewScheduler(st, handler).WithAssistRequester(
		func(
			_ context.Context,
			_ identity.Identity,
			view cases.CaseView,
			policy domain.AssistAutomation,
			source AssistPolicySource,
		) (AssistReconcileOutcome, error) {
			gotView, gotPolicy, gotSource = view, policy, source
			return AssistEligible, nil
		},
	)
	summary, err := scheduler.ReconcileAssists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.AssistEligible != 1 || gotView.CaseID != caseID ||
		gotPolicy.Key != "opening_summary" || gotSource.Kind != "case_type" ||
		gotSource.Key != "edd" || gotSource.ConfigurationSeq < 1 {
		t.Fatalf(
			"assist reconciliation summary=%+v view=%+v policy=%+v source=%+v",
			summary, gotView, gotPolicy, gotSource,
		)
	}
	projected, found, err := cases.Read(ctx, st, id, caseID)
	if err != nil || !found {
		t.Fatalf("read case after assist reconciliation: found=%v err=%v", found, err)
	}
	if projected.Queue != "" || projected.Status != domain.CaseStatus("intake") {
		t.Fatalf("assist reconciliation changed case workflow: %+v", projected)
	}
}

// TestTickDeliversBreachWebhook proves the SLA escalation reaches the webhook
// channel, records its terminal outcome, and is not re-called on a later tick.
func TestTickDeliversBreachWebhook(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	h := command.NewHandler(log)
	a := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}
	overdue, _, err := h.RequestReview(ctx, a, domain.RequestReview{CompanyName: "Acme", CaseType: "aml", SLADays: 1})
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().AddDate(0, 0, 10)
	var delivered []string
	s := (&Scheduler{store: st, cmd: h, now: func() time.Time { return now }}).WithNotify(
		func(_ context.Context, _ identity.Identity, caseID string) (DeliveryOutcome, error) {
			delivered = append(delivered, caseID)
			return DeliverySucceeded, nil
		})
	sum, err := s.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Delivered != 1 {
		t.Fatalf("delivered summary = %+v, want one", sum)
	}
	if len(delivered) != 1 || delivered[0] != overdue {
		t.Fatalf("breach webhook not delivered for the overdue case: %v (want [%s])", delivered, overdue)
	}
	delivered = nil
	if _, err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 0 {
		t.Fatalf("second tick re-delivered: %v", delivered)
	}
}

func TestRetryableBreachDeliverySurvivesRestartWithoutDuplicatingSuccess(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "adam"}
	h := command.NewHandler(log)
	caseID, _, err := h.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "aml", SLADays: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().AddDate(0, 0, 10)
	rebuild := func() store.Store {
		st := store.NewMemory()
		if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
			t.Fatal(err)
		}
		return st
	}

	attempts := 0
	first := (&Scheduler{store: rebuild(), cmd: h, now: func() time.Time { return now }}).WithNotify(
		func(_ context.Context, _ identity.Identity, got string) (DeliveryOutcome, error) {
			attempts++
			if got != caseID {
				t.Fatalf("delivered case %q, want %q", got, caseID)
			}
			return DeliveryRetry, nil
		})
	sum, err := first.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Breached != 1 || sum.Retrying != 1 || attempts != 1 {
		t.Fatalf("retry tick = %+v attempts=%d", sum, attempts)
	}

	// New command/scheduler/store instances simulate a process restart and replay.
	second := (&Scheduler{
		store: rebuild(), cmd: command.NewHandler(log), now: func() time.Time { return now },
	}).WithNotify(func(_ context.Context, _ identity.Identity, _ string) (DeliveryOutcome, error) {
		attempts++
		return DeliverySucceeded, nil
	})
	sum, err = second.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Breached != 0 || sum.Delivered != 1 || attempts != 2 {
		t.Fatalf("restart delivery = %+v attempts=%d", sum, attempts)
	}

	projected := rebuild()
	view, ok, err := cases.Read(ctx, projected, id, caseID)
	if err != nil || !ok {
		t.Fatalf("read delivered case: ok=%v err=%v", ok, err)
	}
	if view.SLAEscalationStatus != events.SLAEscalationDelivered {
		t.Fatalf("projected delivery status = %q", view.SLAEscalationStatus)
	}

	if _, err := second.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("successful escalation was delivered again: attempts=%d", attempts)
	}
}

func TestRetryableDeliveryDeadLettersAndExplicitRetryStartsNewRound(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	handler := command.NewHandler(log)
	caseID, _, err := handler.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "aml", SLADays: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	rebuild := func() store.Store {
		st := store.NewMemory()
		if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
			t.Fatal(err)
		}
		return st
	}
	now := time.Now().UTC().AddDate(0, 0, 10)
	outcome := DeliveryRetry
	scheduler := (&Scheduler{
		store: rebuild(), cmd: handler, now: func() time.Time { return now },
	}).WithNotify(func(_ context.Context, _ identity.Identity, _ string) (DeliveryOutcome, error) {
		return outcome, nil
	})
	for attempt := 1; attempt <= maxDeliveryAttempts; attempt++ {
		summary, err := scheduler.Tick(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if attempt < maxDeliveryAttempts && summary.Retrying != 1 {
			t.Fatalf("attempt %d summary = %+v, want retrying", attempt, summary)
		}
		if attempt == maxDeliveryAttempts && summary.PermanentFailures != 1 {
			t.Fatalf("dead-letter summary = %+v", summary)
		}
	}
	if _, err := handler.RetrySLAEscalation(ctx, id, caseID, "webhook endpoint repaired"); err != nil {
		t.Fatal(err)
	}
	outcome = DeliverySucceeded
	scheduler.store = rebuild()
	summary, err := scheduler.Tick(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Delivered != 1 {
		t.Fatalf("explicit retry delivery summary = %+v", summary)
	}
	projected := rebuild()
	view, found, err := cases.Read(ctx, projected, id, caseID)
	if err != nil || !found {
		t.Fatalf("read case: found=%v err=%v", found, err)
	}
	if view.SLAEscalationStatus != events.SLAEscalationDelivered ||
		view.SLAEscalationRound != 1 || len(view.SLADeliveryAttempts) != maxDeliveryAttempts+1 {
		t.Fatalf("replayed SLA attempts = %+v", view)
	}
	last := view.SLADeliveryAttempts[len(view.SLADeliveryAttempts)-1]
	if last.Round != 1 || last.Attempt != 1 || last.Outcome != events.SLADeliveryDelivered {
		t.Fatalf("new-round attempt = %+v", last)
	}
}
