// SPDX-License-Identifier: AGPL-3.0-or-later

package authoring

import (
	"context"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	platformscheduler "github.com/e6qu/intraktible/platform/scheduler"
	"github.com/e6qu/intraktible/platform/store"
)

const schedulerActor = "authoring-scheduler"

const (
	reviewReminderAfter = 24 * time.Hour
	staleDraftAfter     = 90 * 24 * time.Hour
)

// Scheduler resumes crash-interrupted changeset publications and expires
// disposable presence leases.
type Scheduler struct {
	handler *Handler
	store   store.Store
	now     func() time.Time
}

func NewScheduler(handler *Handler, st store.Store) *Scheduler {
	return &Scheduler{
		handler: handler, store: st,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *Scheduler) WithNow(now func() time.Time) *Scheduler {
	s.now = now
	return s
}

type TickSummary struct {
	Publications int
	Presence     int
	Reminders    int
	Archived     int
}

func (s *Scheduler) Tick(ctx context.Context) (TickSummary, error) {
	changeSets, err := store.ListDocs[ChangeSetView](ctx, s.store, ChangeSetCollection, "")
	if err != nil {
		return TickSummary{}, err
	}
	var summary TickSummary
	for _, changeSet := range changeSets {
		if changeSet.State != ChangeSetPublishing {
			continue
		}
		id := identity.Identity{
			Org: changeSet.Org, Workspace: changeSet.Workspace, Actor: schedulerActor,
		}
		if _, _, _, err := s.handler.PublishChangeSet(
			ctx, id, changeSet.ChangeSetID,
		); err != nil {
			return summary, err
		}
		summary.Publications++
	}
	now := s.now()
	for _, changeSet := range changeSets {
		if changeSet.State != ChangeSetInReview ||
			changeSet.SubmittedAt.IsZero() ||
			changeSet.SubmittedAt.After(now.Add(-reviewReminderAfter)) {
			continue
		}
		id := identity.Identity{
			Org: changeSet.Org, Workspace: changeSet.Workspace, Actor: schedulerActor,
		}
		reminded, err := s.handler.remindChangeSet(
			ctx, id, changeSet.ChangeSetID, now.UTC().Format("2006-01-02"),
		)
		if err != nil {
			return summary, err
		}
		if reminded {
			summary.Reminders++
		}
	}
	drafts, err := store.ListDocs[DraftView](ctx, s.store, DraftCollection, "")
	if err != nil {
		return summary, err
	}
	protected := make(map[string]bool)
	for _, changeSet := range changeSets {
		switch changeSet.State {
		case ChangeSetDraft, ChangeSetInReview, ChangeSetApproved, ChangeSetPublishing:
			protected[changeSet.DraftID] = true
		}
	}
	for _, draft := range drafts {
		if draft.State != DraftStateActive || protected[draft.DraftID] ||
			draft.UpdatedAt.After(now.Add(-staleDraftAfter)) {
			continue
		}
		id := identity.Identity{
			Org: draft.Org, Workspace: draft.Workspace, Actor: schedulerActor,
		}
		archived, err := s.handler.archiveStaleDraft(
			ctx, id, draft.DraftID, now.Add(-staleDraftAfter),
		)
		if err != nil {
			return summary, err
		}
		if archived {
			summary.Archived++
		}
	}
	expired, err := s.handler.SweepPresence(ctx)
	if err != nil {
		return summary, err
	}
	summary.Presence = expired
	return summary, nil
}

func (s *Scheduler) Run(
	ctx context.Context,
	interval time.Duration,
	report func(error),
) {
	platformscheduler.Run(
		ctx, interval, "authoring", "authoring",
		report, s.Tick,
		func(summary TickSummary) {
			if summary.Publications > 0 || summary.Presence > 0 ||
				summary.Reminders > 0 || summary.Archived > 0 {
				slog.Info(
					"authoring scheduler",
					"publications", summary.Publications,
					"expired_presence", summary.Presence,
					"review_reminders", summary.Reminders,
					"archived_drafts", summary.Archived,
				)
			}
		},
	)
}
