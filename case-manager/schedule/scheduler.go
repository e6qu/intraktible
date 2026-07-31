// SPDX-License-Identifier: AGPL-3.0-or-later

package schedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/store"
)

// sweepActor is the synthetic actor attributed to scheduler-driven SLA breaches in
// the audit trail (it bypasses HTTP role checks, like the other schedulers).
const sweepActor = "sla-sweeper"

// Cmd is the narrow command surface the scheduler drives (kept narrow to avoid a
// command↔schedule import cycle and to make the scheduler testable with a fake).
type Cmd interface {
	RoutePending(ctx context.Context, id identity.Identity) (map[string]string, []string, error)
	EscalateBreached(ctx context.Context, id identity.Identity, caseIDs []string) (map[string]string, []string, error)
	SweepSLA(ctx context.Context, id identity.Identity, now time.Time) ([]string, error)
	PendingSLAEscalations(ctx context.Context, id identity.Identity) ([]string, error)
	RecordSLADeliveryAttempt(ctx context.Context, id identity.Identity, caseID string, outcome events.SLADeliveryOutcome) (int, error)
	RecordSLAEscalation(ctx context.Context, id identity.Identity, caseID string, status events.SLAEscalationStatus) error
}

// Scheduler records SLA breaches for open cases whose deadline has passed, across
// every tenant, on a timer.
type Scheduler struct {
	store         store.Store
	cmd           Cmd
	now           func() time.Time
	notify        func(ctx context.Context, id identity.Identity, caseID string) (DeliveryOutcome, error)
	requestAssist AssistRequester
}

type AssistPolicySource struct {
	Kind             string
	Key              string
	ConfigurationSeq uint64
}

type AssistReconcileOutcome string

const (
	AssistEligible   AssistReconcileOutcome = "eligible"
	AssistWaiting    AssistReconcileOutcome = "waiting"
	AssistIneligible AssistReconcileOutcome = "ineligible"
)

type AssistRequester func(
	ctx context.Context,
	id identity.Identity,
	view cases.CaseView,
	policy domain.AssistAutomation,
	source AssistPolicySource,
) (AssistReconcileOutcome, error)

// NewScheduler builds an SLA-sweep scheduler over the store and command surface.
func NewScheduler(st store.Store, cmd Cmd) *Scheduler {
	return &Scheduler{store: st, cmd: cmd, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the clock (deterministic tests, the demo seeder) and
// returns the scheduler.
func (s *Scheduler) WithNow(now func() time.Time) *Scheduler {
	s.now = now
	return s
}

// WithNotify registers a delivery hook called (in the shell) with the cases that just
// breached their SLA, so an overdue human task can be pushed to an external channel
// (a webhook) — the reviewer-facing escalation. The in-app inbox is driven separately
// off the same events by the notifications projector.
func (s *Scheduler) WithNotify(fn func(ctx context.Context, id identity.Identity, caseID string) (DeliveryOutcome, error)) *Scheduler {
	s.notify = fn
	return s
}

// WithAssistRequester adds a cross-component admission shell. Case routing,
// SLA, and resolution remain independent: expected waiting/ineligible outcomes
// are counted, while malformed or failed admission returns an operational error.
func (s *Scheduler) WithAssistRequester(request AssistRequester) *Scheduler {
	s.requestAssist = request
	return s
}

// DeliveryOutcome tells the scheduler whether an external effect should be
// retried or durably completed for the case.
type DeliveryOutcome string

const (
	DeliveryRetry            DeliveryOutcome = "retry"
	DeliverySucceeded        DeliveryOutcome = "delivered"
	DeliveryNoChannel        DeliveryOutcome = "no_channel"
	DeliveryPermanentFailure DeliveryOutcome = "permanent_failure"
)

// TickSummary reports what one sweep did.
type TickSummary struct {
	Routed            int
	RoutingFailures   int
	Breached          int
	QueueEscalated    int
	Delivered         int
	NoChannel         int
	PermanentFailures int
	Retrying          int
	AssistEligible    int
	AssistWaiting     int
	AssistIneligible  int
}

const maxDeliveryAttempts = 5

// Tick sweeps every tenant's open cases once, recording SLA breaches. SweepSLA is
// idempotent per case, so repeated ticks do not double-emit. Exported for
// deterministic tests.
func (s *Scheduler) Tick(ctx context.Context) (TickSummary, error) {
	views, err := cases.ListAll(ctx, s.store)
	if err != nil {
		return TickSummary{}, err
	}
	tenants := make(map[identity.Identity]struct{})
	for _, v := range views {
		tenants[identity.Identity{Org: v.Org, Workspace: v.Workspace, Actor: sweepActor}] = struct{}{}
	}
	now := s.now()
	var sum TickSummary
	for id := range tenants {
		routed, failures, err := s.cmd.RoutePending(ctx, id)
		if err != nil {
			return sum, err
		}
		sum.Routed += len(routed)
		sum.RoutingFailures += len(failures)
		if len(failures) > 0 {
			return sum, fmt.Errorf("case router: %d case(s) could not be routed: %v", len(failures), failures)
		}
		breached, err := s.cmd.SweepSLA(ctx, id, now)
		if err != nil {
			return sum, err
		}
		sum.Breached += len(breached)
		// Reconcile every durable breach, not only the ones emitted above: a
		// prior process may have died after recording the breach and before
		// moving the queue.
		escalated, failures, err := s.cmd.EscalateBreached(ctx, id, nil)
		if err != nil {
			return sum, err
		}
		sum.QueueEscalated += len(escalated)
		if len(failures) > 0 {
			return sum, fmt.Errorf("case queue escalation: %d case(s) could not be escalated: %v", len(failures), failures)
		}
		pending, err := s.cmd.PendingSLAEscalations(ctx, id)
		if err != nil {
			return sum, err
		}
		for _, caseID := range pending {
			outcome := DeliveryNoChannel
			if s.notify != nil {
				outcome, err = s.notify(ctx, id, caseID)
				if err != nil {
					return sum, err
				}
			}
			var status events.SLAEscalationStatus
			var recordedOutcome events.SLADeliveryOutcome
			switch outcome {
			case DeliverySucceeded:
				status = events.SLAEscalationDelivered
				recordedOutcome = events.SLADeliveryDelivered
				sum.Delivered++
			case DeliveryNoChannel:
				status = events.SLAEscalationNoChannel
				recordedOutcome = events.SLADeliveryNoChannel
				sum.NoChannel++
			case DeliveryPermanentFailure:
				status = events.SLAEscalationPermanentFailure
				recordedOutcome = events.SLADeliveryPermanentFailure
				sum.PermanentFailures++
			case DeliveryRetry:
				recordedOutcome = events.SLADeliveryRetryable
			default:
				return sum, fmt.Errorf("sla sweeper: unknown delivery outcome %q", outcome)
			}
			attempt, err := s.cmd.RecordSLADeliveryAttempt(ctx, id, caseID, recordedOutcome)
			if err != nil {
				return sum, err
			}
			if outcome == DeliveryRetry {
				if attempt < maxDeliveryAttempts {
					sum.Retrying++
					continue
				}
				status = events.SLAEscalationPermanentFailure
				sum.PermanentFailures++
			}
			if err := s.cmd.RecordSLAEscalation(ctx, id, caseID, status); err != nil {
				return sum, err
			}
		}
	}
	if s.requestAssist != nil {
		if err := s.reconcileAssists(ctx, views, &sum); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// ReconcileAssists runs only the case-assist admission policies. It is the
// deterministic on-demand boundary used by hosts that need to advance this
// asynchronous loop without also routing cases or changing SLA state.
func (s *Scheduler) ReconcileAssists(ctx context.Context) (TickSummary, error) {
	if s.requestAssist == nil {
		return TickSummary{}, errors.New(
			"case scheduler: no assist requester is configured",
		)
	}
	views, err := cases.ListAll(ctx, s.store)
	if err != nil {
		return TickSummary{}, err
	}
	var sum TickSummary
	if err := s.reconcileAssists(ctx, views, &sum); err != nil {
		return sum, err
	}
	return sum, nil
}

func (s *Scheduler) reconcileAssists(
	ctx context.Context,
	views []cases.CaseView,
	sum *TickSummary,
) error {
	caseTypeCache := make(map[string]cases.CaseTypeView)
	queueCache := make(map[string]map[string]cases.QueueView)
	for _, view := range views {
		if view.CaseTypeVersion < 1 {
			continue
		}
		id := identity.Identity{
			Org: view.Org, Workspace: view.Workspace, Actor: sweepActor,
		}
		typeKey := view.Org + "\x00" + view.Workspace + "\x00" +
			view.CaseType + "\x00" + fmt.Sprint(view.CaseTypeVersion)
		caseType, cached := caseTypeCache[typeKey]
		if !cached {
			var found bool
			var err error
			caseType, found, err = cases.CaseTypeVersion(
				ctx, s.store, id, view.CaseType, view.CaseTypeVersion,
			)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf(
					"case scheduler: missing pinned case type %s@%d for %s",
					view.CaseType, view.CaseTypeVersion, view.CaseID,
				)
			}
			caseTypeCache[typeKey] = caseType
		}
		if caseType.Definition.IsTerminal(view.Status) {
			continue
		}
		for _, policy := range caseType.Definition.Assists {
			if err := s.requestOneAssist(
				ctx, id, view, policy,
				AssistPolicySource{
					Kind: "case_type", Key: view.CaseType,
					ConfigurationSeq: caseType.Seq,
				},
				sum,
			); err != nil {
				return err
			}
		}
		if view.Queue == "" {
			continue
		}
		tenantKey := view.Org + "\x00" + view.Workspace
		queuesByKey, cached := queueCache[tenantKey]
		if !cached {
			queues, err := cases.ListQueues(ctx, s.store, id)
			if err != nil {
				return err
			}
			queuesByKey = make(map[string]cases.QueueView, len(queues))
			for _, queue := range queues {
				queuesByKey[queue.Definition.Key] = queue
			}
			queueCache[tenantKey] = queuesByKey
		}
		queue, found := queuesByKey[view.Queue]
		if !found {
			return fmt.Errorf(
				"case scheduler: routed case %s references missing queue %q",
				view.CaseID, view.Queue,
			)
		}
		for _, policy := range queue.Definition.Assists {
			if err := validateQueueAssistEvidence(policy, caseType.Definition); err != nil {
				return fmt.Errorf(
					"case scheduler: queue %s assist policy %s is incompatible with pinned case type %s@%d: %w",
					view.Queue, policy.Key, view.CaseType, view.CaseTypeVersion, err,
				)
			}
			if err := s.requestOneAssist(
				ctx, id, view, policy,
				AssistPolicySource{
					Kind: "queue", Key: view.Queue,
					ConfigurationSeq: queue.Seq,
				},
				sum,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateQueueAssistEvidence(
	policy domain.AssistAutomation,
	caseType domain.CaseTypeDefinition,
) error {
	requirements := make(map[string]domain.EvidenceRequirement, len(caseType.Evidence))
	for _, requirement := range caseType.Evidence {
		requirements[requirement.Key] = requirement
	}
	for _, required := range policy.EvidenceRequirements {
		requirement, found := requirements[required]
		if !found {
			return fmt.Errorf("evidence requirement %q is not configured", required)
		}
		linkable := false
		for _, kind := range requirement.Kinds {
			if domain.EvidenceKind(kind).Linkable() {
				linkable = true
				break
			}
		}
		if !linkable {
			return fmt.Errorf("evidence requirement %q has no linkable kind", required)
		}
	}
	return nil
}

func (s *Scheduler) requestOneAssist(
	ctx context.Context,
	id identity.Identity,
	view cases.CaseView,
	policy domain.AssistAutomation,
	source AssistPolicySource,
	sum *TickSummary,
) error {
	outcome, err := s.requestAssist(ctx, id, view, policy, source)
	if err != nil {
		return fmt.Errorf(
			"case scheduler: reconcile assist policy %s/%s for case %s: %w",
			source.Kind, policy.Key, view.CaseID, err,
		)
	}
	switch outcome {
	case AssistEligible:
		sum.AssistEligible++
	case AssistWaiting:
		sum.AssistWaiting++
	case AssistIneligible:
		sum.AssistIneligible++
	default:
		return fmt.Errorf(
			"case scheduler: assist policy %s/%s returned unknown outcome %q",
			source.Kind, policy.Key, outcome,
		)
	}
	return nil
}

// Run sweeps on a timer until ctx is cancelled. Failed ticks remain visible in
// operational health until a later tick completes successfully.
func (s *Scheduler) Run(ctx context.Context, interval time.Duration, report func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			summary, err := s.Tick(ctx)
			if err != nil {
				report(err)
				slog.Error("sla sweeper: tick", "err", err)
				metrics.RecordSchedulerTick("case_sla", "error")
				continue
			}
			report(nil)
			if summary.Routed > 0 || summary.Breached > 0 || summary.QueueEscalated > 0 ||
				summary.Delivered > 0 || summary.Retrying > 0 ||
				summary.PermanentFailures > 0 || summary.AssistEligible > 0 ||
				summary.AssistWaiting > 0 || summary.AssistIneligible > 0 {
				slog.Info("case scheduler", "routed", summary.Routed, "breached", summary.Breached,
					"queue_escalated", summary.QueueEscalated, "delivered", summary.Delivered,
					"retrying", summary.Retrying, "permanent_failures", summary.PermanentFailures)
				slog.Info(
					"case assist reconciliation",
					"eligible", summary.AssistEligible,
					"waiting", summary.AssistWaiting,
					"ineligible", summary.AssistIneligible,
				)
			}
			metrics.RecordSchedulerTick("case_sla", "ok")
		}
	}
}
